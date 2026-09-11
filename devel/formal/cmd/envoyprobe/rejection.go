package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	matcher "github.com/envoyproxy/go-control-plane/envoy/type/matcher/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Scenario "rejection" (RF-026): does partial acceptance of a NACKed
// response extend to EDS and RDS, the two types kgateway updates most often?
//
//	phase 0  two listeners, two route configurations, two EDS clusters, ready
//	phase 1  EDS response {a with load_balancing_weight 2, b with port 70000}
//	phase 2  EDS response {a with weight 2, b valid}
//	phase 3  RDS response {routes-a with an added route, routes-b with an
//	         uncompilable regex match}
//	phase 4  RDS response {routes-a with the added route, routes-b valid}
//
// The invalid CLA fails proto constraint validation (port above 65535); the
// invalid route fails regex compilation. Both are rejected by Envoy before
// application, so the observation isolates whether the valid sibling in the
// same response is applied.
func rejectionResources(phase int) map[string][]*anypb.Any {
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	}
	lbEndpoint := func(port uint32, weight uint32) *envoyendpointv3.LbEndpoint {
		ep := &envoyendpointv3.LbEndpoint{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(port)}}}
		if weight > 0 {
			ep.LoadBalancingWeight = wrapperspb.UInt32(weight)
		}
		return ep
	}
	cla := func(name string, eps ...*envoyendpointv3.LbEndpoint) *envoyendpointv3.ClusterLoadAssignment {
		return &envoyendpointv3.ClusterLoadAssignment{ClusterName: name, Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: eps}}}
	}
	prefixRoute := func(name, prefix, clusterName string) *envoyroutev3.Route {
		return &envoyroutev3.Route{Name: name, Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: prefix}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: clusterName}}}}
	}
	httpListener := func(name, routeConfig string, port uint32) *envoylistenerv3.Listener {
		hm := &hcm.HttpConnectionManager{StatPrefix: name, RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: routeConfig, ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
		return &envoylistenerv3.Listener{Name: name, Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: port}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	}

	weightA := uint32(0)
	if phase >= 1 {
		weightA = 2
	}
	portB := uint32(10001)
	if phase == 1 {
		portB = 70000 // fails the port_value <= 65535 constraint
	}
	routesA := []*envoyroutev3.Route{prefixRoute("root", "/", "a")}
	if phase >= 3 {
		routesA = append([]*envoyroutev3.Route{prefixRoute("added", "/added", "a")}, routesA...)
	}
	routesB := []*envoyroutev3.Route{prefixRoute("root", "/", "b")}
	if phase == 3 {
		routesB = append([]*envoyroutev3.Route{{Name: "broken", Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_SafeRegex{SafeRegex: &matcher.RegexMatcher{Regex: "("}}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "b"}}}}}, routesB...)
	}
	return map[string][]*anypb.Any{
		resource.ClusterType:  {packed(edsCluster("a")), packed(edsCluster("b"))},
		resource.EndpointType: {packed(cla("a", lbEndpoint(10001, weightA))), packed(cla("b", lbEndpoint(portB, 0)))},
		resource.RouteType: {
			packed(&envoyroutev3.RouteConfiguration{Name: "routes-a", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "a", Domains: []string{"*"}, Routes: routesA}}}),
			packed(&envoyroutev3.RouteConfiguration{Name: "routes-b", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "b", Domains: []string{"*"}, Routes: routesB}}}),
		},
		resource.ListenerType: {packed(httpListener("front", "routes-a", 10000)), packed(httpListener("second", "routes-b", 10002))},
	}
}

// endpointWeights reads cluster -> first endpoint load_balancing_weight from a
// config dump with include_eds.
func endpointWeights(body string) map[string]uint32 {
	var dump struct {
		Configs []struct {
			Endpoints []struct {
				Config struct {
					Name       string `json:"cluster_name"`
					Localities []struct {
						Hosts []struct {
							Weight uint32 `json:"load_balancing_weight"`
						} `json:"lb_endpoints"`
					} `json:"endpoints"`
				} `json:"endpoint_config"`
			} `json:"dynamic_endpoint_configs"`
		} `json:"configs"`
	}
	out := map[string]uint32{}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return out
	}
	for _, config := range dump.Configs {
		for _, entry := range config.Endpoints {
			for _, locality := range entry.Config.Localities {
				for _, host := range locality.Hosts {
					out[entry.Config.Name] = host.Weight
				}
			}
		}
	}
	return out
}

// routeNames reads route configuration -> route names from a config dump.
func routeNames(body string) map[string][]string {
	var dump struct {
		Configs []struct {
			Dynamic []struct {
				RouteConfig struct {
					Name         string `json:"name"`
					VirtualHosts []struct {
						Routes []struct {
							Name string `json:"name"`
						} `json:"routes"`
					} `json:"virtual_hosts"`
				} `json:"route_config"`
			} `json:"dynamic_route_configs"`
		} `json:"configs"`
	}
	out := map[string][]string{}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return out
	}
	for _, config := range dump.Configs {
		for _, entry := range config.Dynamic {
			for _, vh := range entry.RouteConfig.VirtualHosts {
				for _, r := range vh.Routes {
					out[entry.RouteConfig.Name] = append(out[entry.RouteConfig.Name], r.Name)
				}
			}
		}
	}
	return out
}

func contains(list []string, want string) bool {
	return slices.Contains(list, want)
}

func runRejection(ctx context.Context, dir, admin, front, second string, p *probeServer, advance func(int) error, useCache bool) error {
	save := func(name, path string) (string, error) {
		_, body, err := get(admin + path)
		if err != nil {
			return "", err
		}
		return body, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	awaitNack := func(typ string) error {
		for {
			select {
			case got := <-p.nacks:
				if got == typ {
					return nil
				}
			case <-ctx.Done():
				return fmt.Errorf("no NACK of %s observed: %w", typ, ctx.Err())
			}
		}
	}
	quiet := func(window time.Duration) error {
		for len(p.nacks) > 0 {
			<-p.nacks
		}
		before := p.nackCount.Load()
		select {
		case <-time.After(window):
		case <-ctx.Done():
			return ctx.Err()
		}
		if delta := p.nackCount.Load() - before; delta != 0 {
			return fmt.Errorf("unexpected %d NACK(s) after the corrected state was applied", delta)
		}
		return nil
	}
	expectOK := func(url string) error {
		return await(ctx, func() bool { got, _, _ := get(url); return got == 200 })
	}

	// Phase 0: both chains ready.
	if err := expectOK(front + "/"); err != nil {
		return fmt.Errorf("front chain did not serve: %w", err)
	}
	if err := expectOK(second + "/"); err != nil {
		return fmt.Errorf("second chain did not serve: %w", err)
	}
	p.record(map[string]any{"event": "observation", "phase": "ready", "front": 200, "second": 200})

	// Phase 1: EDS with a valid change to a and an invalid CLA for b.
	if err := advance(1); err != nil {
		return err
	}
	if err := awaitNack(resource.EndpointType); err != nil {
		return err
	}
	body, err := save("partial-eds-nack-config.json", "/config_dump?include_eds")
	if err != nil {
		return err
	}
	weights := endpointWeights(body)
	edsSiblingApplied := weights["a"] == 2
	p.record(map[string]any{"event": "observation", "phase": "partial-eds-nack", "a_weight": weights["a"], "valid_sibling_applied": edsSiblingApplied})
	// RF-026: unlike the CDS and LDS application-stage failures in the
	// references scenario, a proto constraint violation is caught while the
	// response is decoded, and the pinned Envoy rejects the whole EDS response:
	// the valid sibling CLA is not applied. A change here means rejection
	// semantics moved.
	if edsSiblingApplied {
		return fmt.Errorf("valid CLA in the constraint-rejected EDS response was applied (a weight %d); reassess RF-026", weights["a"])
	}
	if err = expectOK(second + "/"); err != nil {
		return fmt.Errorf("b lost its retained endpoints after the rejected CLA: %w", err)
	}
	if err = measureStorm(ctx, p, useCache, "partial-eds-nack"); err != nil {
		return err
	}

	// Phase 2: corrected EDS.
	if err = advance(2); err != nil {
		return err
	}
	if err = await(ctx, func() bool { _, d, _ := get(admin + "/config_dump?include_eds"); return endpointWeights(d)["a"] == 2 }); err != nil {
		return fmt.Errorf("corrected EDS not applied: %w", err)
	}
	if err = quiet(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 2: %w", err)
	}
	p.record(map[string]any{"event": "observation", "phase": "corrected-eds", "nack": false})

	// Phase 3: RDS with a valid change to routes-a and an invalid routes-b.
	if err = advance(3); err != nil {
		return err
	}
	if err = awaitNack(resource.RouteType); err != nil {
		return err
	}
	body, err = save("partial-rds-nack-config.json", "/config_dump")
	if err != nil {
		return err
	}
	names := routeNames(body)
	rdsSiblingApplied := contains(names["routes-a"], "added")
	p.record(map[string]any{"event": "observation", "phase": "partial-rds-nack", "routes_a": names["routes-a"], "routes_b": names["routes-b"], "valid_sibling_applied": rdsSiblingApplied})
	// RF-026: a route configuration that fails to build rejects the whole RDS
	// response on the pinned Envoy; the valid sibling is not applied and both
	// subscriptions log the same rejection. This differs from CDS and LDS, whose
	// API implementations apply resources one by one.
	if rdsSiblingApplied {
		return fmt.Errorf("valid route configuration in the rejected RDS response was applied (routes-a %v); reassess RF-026", names["routes-a"])
	}
	if contains(names["routes-b"], "broken") {
		return errors.New("the invalid route configuration was applied")
	}
	if err = expectOK(second + "/"); err != nil {
		return fmt.Errorf("second chain lost its retained routes after the rejected RDS: %w", err)
	}
	if err = measureStorm(ctx, p, useCache, "partial-rds-nack"); err != nil {
		return err
	}

	// Phase 4: corrected RDS.
	if err = advance(4); err != nil {
		return err
	}
	if err = await(ctx, func() bool {
		_, d, _ := get(admin + "/config_dump")
		return contains(routeNames(d)["routes-b"], "root") && !contains(routeNames(d)["routes-b"], "broken")
	}); err != nil {
		return fmt.Errorf("corrected RDS not applied: %w", err)
	}
	if err = quiet(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 4: %w", err)
	}
	if err = expectOK(second + "/"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "corrected-rds", "nack": false})
	fmt.Println("PASS Envoy rejection characterization: EDS and RDS NACKs reject the whole response, unlike CDS and LDS")
	return nil
}
