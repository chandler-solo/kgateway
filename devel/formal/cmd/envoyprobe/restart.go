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

// Scenario "restart" (RF-001, RF-004, GCP-A2): a warm Envoy whose control
// plane restarts with an empty SnapshotCache. This is the shape of a kgateway
// controller restart before its KRT collections have re-derived a client's
// snapshot: the cache has no entry, the proxy is still serving, and the
// stream reconnects.
//
//	phase 0  full ready configuration, published before Envoy connects
//	restart  the ADS server stops; a new server with an empty cache listens
//	         on the same port; Envoy reconnects on its own
//	phase 1  the same content is republished under the same versions
//	phase 2  CDS content and every version change
//
// Only the SnapshotCache mode is meaningful here; the scripted server has no
// cache to be empty.
func restartResources(phase int) map[string][]*anypb.Any {
	c := &envoyclusterv3.Cluster{Name: "a", ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	if phase >= 2 {
		c.ConnectTimeout = durationpb.New(2 * time.Second)
	}
	rc := &envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "a"}}}}}}}}
	hm := &hcm.HttpConnectionManager{StatPrefix: "front", RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
	l := &envoylistenerv3.Listener{Name: "front", Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 10000}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	ep := &envoyendpointv3.ClusterLoadAssignment{ClusterName: "a", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}}
	return map[string][]*anypb.Any{resource.ClusterType: {packed(c)}, resource.EndpointType: {packed(ep)}, resource.RouteType: {packed(rc)}, resource.ListenerType: {packed(l)}}
}

// restartVersion keeps phase 1 at the initial versions so the republish after
// the restart is an equal-version installation.
func restartVersion(phase int) string {
	if phase == 1 {
		return "r0"
	}
	return fmt.Sprintf("r%d", phase)
}

// requestSummary is what the probe keeps about each DiscoveryRequest it sees.
type requestSummary struct {
	typeURL, version, nonce string
	names                   []string
}

func (s *probeServer) noteRequest(typeURL, version, nonce string, names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, requestSummary{typeURL, version, nonce, names})
}

func (s *probeServer) requestsSince(n int) []requestSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n > len(s.requests) {
		return nil
	}
	return append([]requestSummary(nil), s.requests[n:]...)
}

func (s *probeServer) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func runRestart(ctx context.Context, dir, admin, front string, p *probeServer, restart func() (func(int) error, error)) error {
	save := func(name string) error {
		_, body, err := get(admin + "/config_dump")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	serving := func(label string) error {
		code, b, err := get(front + "/")
		if err != nil || code != 200 || b != "probe-upstream" {
			return fmt.Errorf("%s: traffic status=%d err=%w", label, code, err)
		}
		code, _, err = get(admin + "/ready")
		if err != nil || code != 200 {
			return fmt.Errorf("%s: readiness status=%d err=%w", label, code, err)
		}
		return nil
	}

	// Phase 0: warm.
	if err := await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("initial configuration did not serve: %w", err)
	}
	if err := save("restart-before-config.json"); err != nil {
		return err
	}
	before := p.requestCount()
	responsesBefore := p.responseCount.Load()
	p.record(map[string]any{"event": "observation", "phase": "warm", "traffic": 200, "requests": before})

	// Controller restart with an empty cache.
	advance, err := restart()
	if err != nil {
		return err
	}
	p.record(map[string]any{"event": "controller-restart", "cache": "empty"})
	// Envoy reconnects with its own backoff; wait for the first requests on
	// the new server and inspect what they carry.
	if err = await(ctx, func() bool { return p.requestCount()-before >= 4 }); err != nil {
		return fmt.Errorf("Envoy did not reconnect and re-request all four types: %w", err)
	}
	reconnect := p.requestsSince(before)
	var carried []map[string]any
	for _, r := range reconnect {
		carried = append(carried, map[string]any{"type": r.typeURL, "version": r.version, "nonce": r.nonce, "names": r.names})
		if r.nonce != "" {
			return fmt.Errorf("reconnect request for %s carried a nonce %q from the previous stream", r.typeURL, r.nonce)
		}
		if r.version != "r0" {
			return fmt.Errorf("reconnect request for %s carried version %q, expected the accepted r0", r.typeURL, r.version)
		}
	}
	// The empty cache answers nothing; the warm proxy keeps serving.
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	if got := p.responseCount.Load() - responsesBefore; got != 0 {
		return fmt.Errorf("empty cache sent %d responses", got)
	}
	if err = serving("after restart with empty cache"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "reconnected-empty-cache", "requests": carried, "responses": 0, "traffic": 200, "ready": 200})

	// Phase 1: the same content under the same versions. Equal versions park
	// every watch; nothing is sent, and nothing needs to be.
	if err = advance(1); err != nil {
		return err
	}
	select {
	case <-time.After(400 * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	if got := p.responseCount.Load() - responsesBefore; got != 0 {
		return fmt.Errorf("equal-version republish after restart sent %d responses", got)
	}
	if err = serving("after equal-version republish"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "equal-version-republish", "responses": 0, "traffic": 200})

	// Phase 2: a real revision reaches the reconnected proxy.
	if err = advance(2); err != nil {
		return err
	}
	if err = await(ctx, func() bool { _, d, _ := get(admin + "/config_dump"); return activeClusters(d)["a"] == "2s" }); err != nil {
		return fmt.Errorf("revision after restart was not applied: %w", err)
	}
	if err = serving("after revision"); err != nil {
		return err
	}
	if err = save("restart-after-revision-config.json"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "revision-after-restart", "responses": p.responseCount.Load() - responsesBefore, "traffic": 200})
	fmt.Println("PASS Envoy restart characterization: warm proxy keeps serving across a controller restart with an empty cache; reconnect requests carry accepted versions and no nonce; equal-version republish is silent; a revision applies")
	return nil
}
