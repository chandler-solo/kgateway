package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// initialFetchTimeout is the per-resource initial_fetch_timeout used by the
// "timeouts" scenario, in the bootstrap and in every ADS config source.
const initialFetchTimeout = 2 * time.Second

func adsWithTimeout(d time.Duration) *envoycorev3.ConfigSource {
	return &envoycorev3.ConfigSource{ResourceApiVersion: envoycorev3.ApiVersion_V3, ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{Ads: &envoycorev3.AggregatedConfigSource{}}, InitialFetchTimeout: durationpb.New(d)}
}

// Scenario "timeouts" (RF-003, RF-004, RF-010): what Envoy's own
// initial_fetch_timeout does with dependencies the control plane never
// delivers, and what a route published ahead of its cluster serves.
//
//	phase 0  CDS {a} and a listener using RDS "routes"; the server never
//	         sends EDS for a or the route configuration
//	phase 1  RDS "routes" with "/" -> b, where b is absent from CDS
//	phase 2  CDS {a, b}; the server never sends EDS for b
//	phase 3  EDS for b with a reachable host
//
// Every config source carries a two-second initial_fetch_timeout, as does the
// bootstrap for CDS and LDS. The warming and other scenarios disable the
// timeout to isolate control-plane behavior; this one measures the timeout.
func timeoutResources(phase int) map[string][]*anypb.Any {
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: adsWithTimeout(initialFetchTimeout)}}
	}
	hm := &hcm.HttpConnectionManager{StatPrefix: "front", RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: adsWithTimeout(initialFetchTimeout)}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
	l := &envoylistenerv3.Listener{Name: "front", Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 10000}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	out := map[string][]*anypb.Any{
		resource.ClusterType:  {packed(edsCluster("a"))},
		resource.ListenerType: {packed(l)},
	}
	if phase >= 1 {
		out[resource.RouteType] = []*anypb.Any{packed(&envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "b"}}}}}}}})}
	}
	if phase >= 2 {
		out[resource.ClusterType] = append(out[resource.ClusterType], packed(edsCluster("b")))
	}
	if phase >= 3 {
		out[resource.EndpointType] = []*anypb.Any{packed(&envoyendpointv3.ClusterLoadAssignment{ClusterName: "b", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}})}
	}
	return out
}

func runTimeouts(ctx context.Context, dir, admin, front string, p *probeServer, advance func(int) error) error {
	save := func(name string) (string, error) {
		_, body, err := get(admin + "/config_dump?include_eds")
		if err != nil {
			return "", err
		}
		return body, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	start := time.Now()

	// Phase 0: dependencies never arrive; the timeouts decide when Envoy
	// stops waiting. Readiness requires every warming cluster and listener
	// to finish, so its arrival time measures the timeout.
	if err := await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
		return fmt.Errorf("readiness never came without EDS or RDS: %w", err)
	}
	readyAfter := time.Since(start)
	code, _, err := get(front + "/")
	if err != nil {
		return fmt.Errorf("front listener after timeouts: %w", err)
	}
	body, err := save("timeouts-initial-config.json")
	if err != nil {
		return err
	}
	clusters := activeClusters(body)
	p.record(map[string]any{"event": "observation", "phase": "dependencies-timed-out", "ready_after_ms": readyAfter.Milliseconds(), "route_status": code, "active_clusters": len(clusters), "nacks": p.nackCount.Load()})
	if readyAfter < initialFetchTimeout {
		return fmt.Errorf("readiness came after %s, before the %s initial fetch timeout", readyAfter, initialFetchTimeout)
	}
	if _, ok := clusters["a"]; !ok {
		return fmt.Errorf("cluster a did not become active after its EDS timeout: %v", clusters)
	}
	// RF-004: on the pinned Envoy a listener whose RDS never arrives becomes
	// active with no routes after the timeout and answers 404.
	if code != 404 {
		return fmt.Errorf("listener with timed-out RDS answered %d, expected 404; reassess the timeout characterization", code)
	}

	// Phase 1: a route to a cluster that CDS does not carry.
	if err = advance(1); err != nil {
		return err
	}
	if err = await(ctx, func() bool { code, _, _ := get(front + "/"); return code == 503 }); err != nil {
		return fmt.Errorf("route ahead of its cluster did not answer 503: %w", err)
	}
	p.record(map[string]any{"event": "observation", "phase": "route-ahead-of-cluster", "route_status": 503})

	// Phase 2: the cluster arrives but its EDS never does.
	if err = advance(2); err != nil {
		return err
	}
	warmStart := time.Now()
	if err = await(ctx, func() bool { _, d, _ := get(admin + "/config_dump"); _, ok := activeClusters(d)["b"]; return ok }); err != nil {
		return fmt.Errorf("cluster b never left warming: %w", err)
	}
	activeAfter := time.Since(warmStart)
	code, _, _ = get(front + "/")
	p.record(map[string]any{"event": "observation", "phase": "cluster-without-eds-timed-out", "active_after_ms": activeAfter.Milliseconds(), "route_status": code})
	if activeAfter < initialFetchTimeout {
		return fmt.Errorf("cluster b activated after %s, before the EDS initial fetch timeout", activeAfter)
	}
	if code != 503 {
		return fmt.Errorf("route to a timed-out empty cluster answered %d, expected 503", code)
	}

	// Phase 3: endpoints arrive; traffic recovers without any reconnect.
	if err = advance(3); err != nil {
		return err
	}
	if err = await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("late EDS did not recover traffic: %w", err)
	}
	if _, err = save("timeouts-recovered-config.json"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "late-eds-recovers", "route_status": 200, "nacks": p.nackCount.Load()})
	fmt.Println("PASS Envoy timeout characterization: initial fetch timeouts activate a cluster empty and a listener without routes; a route ahead of its cluster is 503; late EDS recovers")
	return nil
}
