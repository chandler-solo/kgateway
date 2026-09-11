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

// Scenario "init" (Envoy #21425, RF-004, RF-014): a partial named EDS
// response during initialization, followed by a CDS re-push and the missing
// assignment. The report says initialization stayed blocked with an infinite
// initial fetch timeout and LDS was never requested. Timeouts are disabled
// here, as in kgateway's bootstrap.
//
//	phase 0  CDS {a, b}; the EDS response carries only a; LDS/RDS ready
//	phase 1  CDS {a, b} re-pushed under a new version, same content
//	phase 2  EDS {a, b}
func initResources(phase int) map[string][]*anypb.Any {
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	}
	cla := func(name string) *envoyendpointv3.ClusterLoadAssignment {
		return &envoyendpointv3.ClusterLoadAssignment{ClusterName: name, Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}}
	}
	hm := &hcm.HttpConnectionManager{StatPrefix: "front", RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
	l := &envoylistenerv3.Listener{Name: "front", Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 10000}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	rc := &envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "a"}}}}}}}}
	endpoints := []*anypb.Any{packed(cla("a"))}
	if phase >= 2 {
		endpoints = append(endpoints, packed(cla("b")))
	}
	return map[string][]*anypb.Any{
		resource.ClusterType:  {packed(edsCluster("a")), packed(edsCluster("b"))},
		resource.EndpointType: endpoints,
		resource.ListenerType: {packed(l)},
		resource.RouteType:    {packed(rc)},
	}
}

// ldsRequested reports whether Envoy has requested listeners yet; under ADS
// initialization ordering that happens only after primary clusters initialize.
func (s *probeServer) ldsRequested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.requests {
		if r.typeURL == resource.ListenerType {
			return true
		}
	}
	return false
}

func runInit(ctx context.Context, dir, admin, front string, p *probeServer, advance func(int) error) error {
	save := func(name string) (string, error) {
		_, body, err := get(admin + "/config_dump")
		if err != nil {
			return "", err
		}
		return body, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	settle := func() error {
		select {
		case <-time.After(1500 * time.Millisecond):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Phase 0: b never receives its assignment; with timeouts disabled the
	// cluster-manager init phase cannot complete, so LDS is not requested and
	// readiness stays 503. This is the ADS initialization ordering.
	if err := await(ctx, func() bool { _, d, _ := get(admin + "/config_dump"); return hasCluster(d, false) }); err != nil {
		return fmt.Errorf("cluster a never activated from the partial EDS response: %w", err)
	}
	if err := settle(); err != nil {
		return err
	}
	code, _, _ := get(admin + "/ready")
	body, err := save("init-partial-eds-config.json")
	if err != nil {
		return err
	}
	ldsRequested := p.ldsRequested()
	p.record(map[string]any{"event": "observation", "phase": "partial-eds", "ready": code, "lds_requested": ldsRequested, "active_clusters": activeClusterNames(body)})
	if code == 200 || ldsRequested {
		return fmt.Errorf("initialization completed with cluster b lacking its assignment (ready=%d lds=%t)", code, ldsRequested)
	}

	// Phase 1: CDS re-pushed under a new version with identical content.
	if err = advance(1); err != nil {
		return err
	}
	if err = settle(); err != nil {
		return err
	}
	code, _, _ = get(admin + "/ready")
	p.record(map[string]any{"event": "observation", "phase": "cds-repush", "ready": code, "lds_requested": p.ldsRequested(), "nacks": p.nackCount.Load()})

	// Phase 2: the missing assignment arrives. Envoy #21425 reported that
	// initialization stayed blocked here.
	if err = advance(2); err != nil {
		return err
	}
	if err = await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
		body, _ = save("init-blocked-config.json")
		p.record(map[string]any{"event": "observation", "phase": "late-eds", "ready": 503, "lds_requested": p.ldsRequested(), "active_clusters": activeClusterNames(body)})
		return fmt.Errorf("initialization stayed blocked after the missing assignment arrived (Envoy #21425 shape reproduced): %w", err)
	}
	if err = await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("listener did not serve after initialization: %w", err)
	}
	if _, err = save("init-completed-config.json"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "late-eds", "ready": 200, "lds_requested": p.ldsRequested(), "traffic": 200, "nacks": p.nackCount.Load()})
	fmt.Println("PASS Envoy init characterization: a partial EDS response holds initialization and LDS until the missing assignment arrives; a CDS re-push in between does not block completion")
	return nil
}
