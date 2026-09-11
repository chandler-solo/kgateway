package main

import (
	"context"
	"errors"
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
	if phase >= 3 {
		c.ConnectTimeout = durationpb.New(3 * time.Second)
	}
	rc := &envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "a"}}}}}}}}
	hm := &hcm.HttpConnectionManager{StatPrefix: "front", RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
	l := &envoylistenerv3.Listener{Name: "front", Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 10000}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	ep := &envoyendpointv3.ClusterLoadAssignment{ClusterName: "a", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}}
	return map[string][]*anypb.Any{resource.ClusterType: {packed(c)}, resource.EndpointType: {packed(ep)}, resource.RouteType: {packed(rc)}, resource.ListenerType: {packed(l)}}
}

// restartVersion keeps phase 1 at the initial versions so the republish after
// the restart is an equal-version installation.
func restartVersion(phase int, typ string) string {
	switch {
	case phase == 1:
		return "r0"
	case phase == 3 && typ == resource.EndpointType:
		// Phase 3 changes CDS only; EDS keeps the phase-2 version so the
		// same-name cluster rewarms against an equal-version EDS watch (RF-017).
		return "r2"
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

func runRestart(ctx context.Context, dir, admin, front string, p *probeServer, restart func() (func(int) error, error), restartProxy func() (string, string, error)) error {
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

	// Proxy restart against the warm cache: a fresh Envoy process connects with
	// no versions and must receive the whole configuration immediately.
	before = p.requestCount()
	responsesBefore = p.responseCount.Load()
	// Ephemeral published ports can move across a container restart, so the
	// admin and front addresses are re-resolved after it.
	if admin, front, err = restartProxy(); err != nil {
		return fmt.Errorf("proxy restart: %w", err)
	}
	if err = await(ctx, func() bool { code, _, _ := get(admin + "/server_info"); return code == 200 }); err != nil {
		return fmt.Errorf("restarted proxy admin unavailable: %w", err)
	}
	p.record(map[string]any{"event": "proxy-restart"})
	if err = await(ctx, func() bool { return p.requestCount()-before >= 4 }); err != nil {
		return fmt.Errorf("restarted proxy did not request all four types: %w", err)
	}
	for _, r := range p.requestsSince(before) {
		if r.version != "" && r.version != "r2" {
			return fmt.Errorf("restarted proxy request for %s carried version %q", r.typeURL, r.version)
		}
	}
	if err = await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("restarted proxy did not serve from the warm cache: %w", err)
	}
	if err = await(ctx, func() bool { _, d, _ := get(admin + "/config_dump"); return activeClusters(d)["a"] == "2s" }); err != nil {
		return fmt.Errorf("restarted proxy did not activate the cached revision: %w", err)
	}
	p.record(map[string]any{"event": "observation", "phase": "proxy-restart-warm-cache", "responses": p.responseCount.Load() - responsesBefore, "traffic": 200})
	// Restart during warming (Envoy #36951 over SotW): a same-name CDS change
	// with the EDS version unchanged leaves the candidate warming (RF-017);
	// the control plane then restarts with an empty cache.
	if err = advance(3); err != nil {
		return err
	}
	if err = await(ctx, func() bool { _, d, _ := get(admin + "/config_dump"); return hasCluster(d, true) }); err != nil {
		return fmt.Errorf("same-name CDS change did not rewarm: %w", err)
	}
	if err = serving("while rewarming"); err != nil {
		return err
	}
	before = p.requestCount()
	responsesBefore = p.responseCount.Load()
	advance, err = restart()
	if err != nil {
		return err
	}
	p.record(map[string]any{"event": "controller-restart", "cache": "empty", "during": "warming"})
	if err = await(ctx, func() bool { return p.requestCount()-before >= 3 }); err != nil {
		return fmt.Errorf("Envoy did not reconnect while warming: %w", err)
	}
	// With the EDS cache enabled the fallback fires on the EDS initial fetch
	// timeout; the window must outlast it to observe the CDS request.
	window := 5 * time.Second
	if edsCacheEnabled && effectiveResourceFetchTimeout() > 0 {
		window = effectiveResourceFetchTimeout() + 5*time.Second
	}
	// Observe for a bounded window which types the reconnect re-requests.
	select {
	case <-time.After(window):
	case <-ctx.Done():
		return ctx.Err()
	}
	var reconnectWhileWarming []map[string]any
	cdsRequested := false
	for _, r := range p.requestsSince(before) {
		reconnectWhileWarming = append(reconnectWhileWarming, map[string]any{"type": r.typeURL, "version": r.version, "nonce": r.nonce})
		if r.typeURL == resource.ClusterType {
			cdsRequested = true
		}
	}
	_, dump, _ := get(admin + "/config_dump")
	stillWarming := hasCluster(dump, true)
	if err = os.WriteFile(filepath.Join(dir, "restart-during-warming-config.json"), []byte(dump), 0o600); err != nil {
		return err
	}
	if err = serving("after restart during warming"); err != nil {
		return err
	}
	// kgateway's bootstrap enables use_eds_cache_for_ads. Measured: the cache
	// completes a warming cluster only when the EDS config source has a nonzero
	// initial_fetch_timeout, on its expiry; kgateway emits none, so the pause
	// below is its profile. With a timeout the fallback fires and CDS is
	// requested again (RF-028 repair candidate).
	expectFallback := edsCacheEnabled && effectiveResourceFetchTimeout() > 0
	p.record(map[string]any{"event": "observation", "phase": "restart-during-warming", "eds_cache": edsCacheEnabled, "resource_fetch_timeout": fetchTimeoutLabel(), "expect_eds_cache_fallback": expectFallback, "requests": reconnectWhileWarming, "window_ms": window.Milliseconds(), "cds_requested_within_window": cdsRequested, "still_warming": stillWarming, "responses": p.responseCount.Load() - responsesBefore, "traffic": 200})
	// RF-028 (Envoy #36951 and #34334 over SotW): with a cluster warming at
	// reconnect, the pinned Envoy re-requests EDS at its accepted version and
	// LDS and RDS, but not CDS, for the whole window; the empty cache answers
	// nothing, the candidate stays warming, and the old active cluster keeps
	// serving. A change here means the reconnect semantics moved.
	if expectFallback {
		if stillWarming {
			return errors.New("EDS cache fallback did not complete the warming candidate after the fetch timeout; reassess the RF-028 repair candidate")
		}
		if !cdsRequested {
			return errors.New("CDS was not re-requested after the EDS cache fallback; reassess RF-028")
		}
	} else {
		if !stillWarming {
			return errors.New("warming candidate did not survive the reconnect; reassess RF-017 and corpus #36951")
		}
		if cdsRequested {
			return errors.New("CDS was re-requested while warming after the reconnect; the #36951 SotW shape no longer holds, reassess RF-028")
		}
	}
	// An EDS revision from the new cache completes warming.
	if err = advance(4); err != nil {
		return err
	}
	if err = await(ctx, func() bool {
		_, d, _ := get(admin + "/config_dump")
		return !hasCluster(d, true) && activeClusters(d)["a"] == "3s"
	}); err != nil {
		return fmt.Errorf("EDS revision after the warming restart did not complete rewarming: %w", err)
	}
	if err = serving("after rewarming completes"); err != nil {
		return err
	}
	// Does completing the warming release a CDS request on the new stream?
	cdsAfter := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !cdsAfter {
		for _, r := range p.requestsSince(before) {
			if r.typeURL == resource.ClusterType {
				cdsAfter = true
			}
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.record(map[string]any{"event": "observation", "phase": "rewarmed-after-restart", "traffic": 200, "cds_requested_after_warming": cdsAfter})
	tail := "a candidate warming across a controller restart stays warming until an EDS revision"
	if edsCacheEnabled && effectiveResourceFetchTimeout() > 0 {
		tail = "a candidate warming across a controller restart completes from the EDS cache when the fetch timeout expires and CDS is requested again"
	}
	fmt.Println("PASS Envoy restart characterization: warm proxy keeps serving across a controller restart with an empty cache; reconnect requests carry accepted versions and no nonce; equal-version republish is silent; a revision applies; a restarted proxy is served from the warm cache; " + tail)
	return nil
}

// effectiveResourceFetchTimeout is the EDS/RDS initial_fetch_timeout Envoy
// applies: the flag value, or Envoy's 15 s default when the field is unset.
func effectiveResourceFetchTimeout() time.Duration {
	if resourceFetchTimeout < 0 {
		return 15 * time.Second
	}
	return resourceFetchTimeout
}

// fetchTimeoutLabel renders the configured EDS/RDS initial_fetch_timeout for
// observations: "unset" (Envoy default 15 s), "disabled" (explicit 0s), or the
// duration.
func fetchTimeoutLabel() string {
	switch {
	case resourceFetchTimeout < 0:
		return "unset"
	case resourceFetchTimeout == 0:
		return "disabled"
	default:
		return resourceFetchTimeout.String()
	}
}
