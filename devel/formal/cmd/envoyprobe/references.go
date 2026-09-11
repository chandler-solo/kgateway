package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// Scenario "references" (RF-004, RF-003): how the pinned Envoy treats
// dynamic references that the control plane cannot or does not close.
//
//	phase 0  RDS route to "ghost", absent from CDS, beside a ready route to "a"
//	phase 1  CDS response {a, bad}; "bad" is an EDS cluster with no EDS config
//	phase 2  CDS response {a, b}; "b" valid with an empty CLA
//	phase 3  LDS adds a listener with an inline route to "ghost"
//	phase 4  that listener's inline route repointed to "a"
//
// Every phase resends every type at a new version so the observation is about
// Envoy acceptance, not about which type the scripted server chose to send.
func referenceResources(phase int) map[string][]*anypb.Any {
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	}
	routeTo := func(prefix, clusterName string) *envoyroutev3.Route {
		return &envoyroutev3.Route{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: prefix}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: clusterName}}}}
	}
	httpListener := func(name, statPrefix string, port uint32, inline *envoyroutev3.RouteConfiguration) *envoylistenerv3.Listener {
		hm := &hcm.HttpConnectionManager{StatPrefix: statPrefix, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
		if inline != nil {
			hm.RouteSpecifier = &hcm.HttpConnectionManager_RouteConfig{RouteConfig: inline}
		} else {
			hm.RouteSpecifier = &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}
		}
		return &envoylistenerv3.Listener{Name: name, Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: port}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	}

	a := edsCluster("a")
	if phase == 1 {
		// A valid change to "a" rides in the rejected response; whether it is
		// applied decides if SotW CDS rejection is atomic per response.
		a.ConnectTimeout = durationpb.New(3 * time.Second)
	}
	clusters := []*anypb.Any{packed(a)}
	endpoints := []*anypb.Any{packed(&envoyendpointv3.ClusterLoadAssignment{ClusterName: "a", Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}})}
	switch {
	case phase == 1:
		// An EDS cluster without eds_cluster_config fails CDS validation.
		clusters = append(clusters, packed(&envoyclusterv3.Cluster{Name: "bad", ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}}))
	case phase >= 2:
		clusters = append(clusters, packed(edsCluster("b")))
		endpoints = append(endpoints, packed(&envoyendpointv3.ClusterLoadAssignment{ClusterName: "b"}))
	}
	rc := &envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{routeTo("/ghost", "ghost"), routeTo("/", "a")}}}}
	listeners := []*anypb.Any{packed(httpListener("front", "front", 10000, nil))}
	if phase >= 3 {
		target := "ghost"
		if phase >= 4 {
			target = "a"
		}
		inline := &envoyroutev3.RouteConfiguration{Name: "inline", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{routeTo("/", target)}}}}
		listeners = append(listeners, packed(httpListener("inline", "inline", 10002, inline)))
		// A valid change to "front" rides in the response that "inline" makes
		// Envoy reject, to observe whether LDS rejection is atomic per response.
		listeners[0] = packed(httpListener("front", "front-r3", 10000, nil))
	}
	return map[string][]*anypb.Any{resource.ClusterType: clusters, resource.EndpointType: endpoints, resource.ListenerType: listeners, resource.RouteType: {packed(rc)}}
}

// activeClusters reads dynamic active cluster names and connect timeouts from
// a config dump.
func activeClusters(body string) map[string]string {
	var dump struct {
		Configs []struct {
			Active []struct {
				Cluster struct {
					Name           string `json:"name"`
					ConnectTimeout string `json:"connect_timeout"`
				} `json:"cluster"`
			} `json:"dynamic_active_clusters"`
		} `json:"configs"`
	}
	out := map[string]string{}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return out
	}
	for _, config := range dump.Configs {
		for _, e := range config.Active {
			out[e.Cluster.Name] = e.Cluster.ConnectTimeout
		}
	}
	return out
}

func activeClusterNames(body string) []string {
	var names []string
	for name := range activeClusters(body) {
		names = append(names, name)
	}
	return names
}

func hasNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			found = found || g == w
		}
		if !found {
			return false
		}
	}
	return true
}

// runReferences drives the "references" scenario after Envoy's admin is up.
// measureStorm observes NACK and response counts over a window after a
// rejection. The scripted server never resends a rejected version, so the
// window must stay quiet; the SnapshotCache path answers every NACK with the
// same rejected version (RF-012) and the window measures that recurrence on a
// real Envoy. A rate from one machine is a characterization, not a budget.
func measureStorm(ctx context.Context, p *probeServer, useCache bool, phase string) error {
	const window = 2 * time.Second
	nacks0, responses0 := p.nackCount.Load(), p.responseCount.Load()
	select {
	case <-time.After(window):
	case <-ctx.Done():
		return ctx.Err()
	}
	nacks, responses := p.nackCount.Load()-nacks0, p.responseCount.Load()-responses0
	p.record(map[string]any{"event": "observation", "phase": phase + "-storm-window", "snapshot_cache": useCache, "window_ms": window.Milliseconds(), "nacks": nacks, "responses": responses})
	if !useCache && (nacks != 0 || responses != 0) {
		return fmt.Errorf("%s: scripted server saw %d NACKs and %d responses in a quiet window", phase, nacks, responses)
	}
	if useCache && nacks < 2 {
		return fmt.Errorf("%s: SnapshotCache path did not resend the rejected version (%d NACKs in %s); reassess RF-012", phase, nacks, window)
	}
	return nil
}

func runReferences(ctx context.Context, dir, admin, front, inline string, p *probeServer, advance func(int) error, useCache bool) error {
	save := func(name string) (string, error) {
		_, body, err := get(admin + "/config_dump")
		if err != nil {
			return "", err
		}
		return body, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	// awaitNack skips NACKs of other types still buffered from an earlier
	// rejection storm (cache mode) and waits for the expected type.
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
	// noNack is count based: it starts after the caller has observed the
	// corrected state applied, so a storm from the previous phase has ended.
	noNack := func(window time.Duration) error {
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

	// Phase 0: RDS reference to a cluster absent from CDS.
	if err := await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
		return fmt.Errorf("RDS with a dangling reference did not initialize: %w", err)
	}
	if err := await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("ready route beside a dangling route did not serve: %w", err)
	}
	code, _, err := get(front + "/ghost")
	if err != nil || code != 503 {
		return fmt.Errorf("dangling RDS route status=%d err=%w", code, err)
	}
	if err = noNack(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 0: %w", err)
	}
	if _, err = save("dangling-rds-config.json"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "dangling-rds-reference", "ready": 200, "ready_route": 200, "dangling_route": 503, "nack": false})

	// Phase 1: CDS with one valid and one invalid cluster.
	if err = advance(1); err != nil {
		return err
	}
	if err = awaitNack(resource.ClusterType); err != nil {
		return err
	}
	body, err := save("partial-cds-nack-config.json")
	if err != nil {
		return err
	}
	if names := activeClusterNames(body); !hasNames(names, "a") {
		return fmt.Errorf("after a partial CDS NACK the active set is %v, expected only a", names)
	}
	// RF-026: the pinned Envoy applies the valid cluster change carried by the
	// rejected response. A change here means the rejection semantics moved.
	if timeout := activeClusters(body)["a"]; timeout != "3s" {
		return fmt.Errorf("valid cluster change in the rejected CDS response was not applied (connect_timeout %q); rejection semantics changed, reassess RF-026", timeout)
	}
	code, b, err := get(front + "/")
	if err != nil || code != 200 || b != "probe-upstream" {
		return fmt.Errorf("traffic through the retained cluster after NACK: status=%d err=%w", code, err)
	}
	p.record(map[string]any{"event": "observation", "phase": "partial-cds-nack", "active_clusters": []string{"a"}, "valid_change_in_rejected_response_applied": true, "traffic": 200})
	if err = measureStorm(ctx, p, useCache, "partial-cds-nack"); err != nil {
		return err
	}
	// The storm must not change what is applied: a again, still 3s, still 200.
	if code, b, err = get(front + "/"); err != nil || code != 200 || b != "probe-upstream" {
		return fmt.Errorf("traffic after the rejection window: status=%d err=%w", code, err)
	}

	// Phase 2: corrected CDS with a second valid cluster and an empty CLA.
	if err = advance(2); err != nil {
		return err
	}
	if err = await(ctx, func() bool { _, d, _ := get(admin + "/config_dump"); return hasNames(activeClusterNames(d), "a", "b") }); err != nil {
		return fmt.Errorf("corrected CDS did not activate both clusters: %w", err)
	}
	if err = noNack(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 2: %w", err)
	}
	if _, err = save("corrected-cds-config.json"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "corrected-cds", "active_clusters": []string{"a", "b"}, "nack": false})

	// Phase 3: LDS with an inline route to the absent cluster.
	if err = advance(3); err != nil {
		return err
	}
	if err = awaitNack(resource.ListenerType); err != nil {
		return err
	}
	body, err = save("inline-dangling-lds-nack-config.json")
	if err != nil {
		return err
	}
	states := listenerStates(body)
	if !states["front"].active || states["front"].statPrefix != "front-r3" {
		return fmt.Errorf("valid listener change in the rejected LDS response was not applied: %v; reassess RF-026", states["front"])
	}
	if !states["front"].active || states["inline"].active || !strings.Contains(states["inline"].err, "unknown cluster 'ghost'") {
		return fmt.Errorf("after the inline LDS NACK, listener states were %v; expected front active and inline held in error_state only", states)
	}
	code, b, err = get(front + "/")
	if err != nil || code != 200 || b != "probe-upstream" {
		return fmt.Errorf("front listener after LDS NACK: status=%d err=%w", code, err)
	}
	if _, _, err = get(inline + "/"); err == nil {
		return errors.New("rejected inline listener accepted a connection")
	}
	p.record(map[string]any{"event": "observation", "phase": "inline-dangling-lds", "nack": true, "front_traffic": 200, "inline_listener": "absent", "valid_change_in_rejected_response_applied": true})
	if err = measureStorm(ctx, p, useCache, "inline-dangling-lds"); err != nil {
		return err
	}

	// Phase 4: the inline route repointed to a present cluster.
	if err = advance(4); err != nil {
		return err
	}
	if err = await(ctx, func() bool { code, b, _ := get(inline + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("corrected inline listener did not serve: %w", err)
	}
	if err = noNack(300 * time.Millisecond); err != nil {
		return fmt.Errorf("phase 4: %w", err)
	}
	body, err = save("inline-corrected-config.json")
	if err != nil {
		return err
	}
	if states := listenerStates(body); !states["inline"].active || states["inline"].err != "" {
		return fmt.Errorf("corrected inline listener state %v; expected active without error", states["inline"])
	}
	p.record(map[string]any{"event": "observation", "phase": "inline-corrected", "nack": false, "inline_traffic": 200})
	fmt.Println("PASS Envoy reference characterization: dangling RDS 503 without NACK; partial CDS and LDS NACKs apply their valid siblings; inline dangling LDS rejected")
	return nil
}

type listenerState struct {
	active     bool
	err        string
	statPrefix string
}

// listenerStates reads dynamic listener activation and error state from a
// config dump. Envoy keeps a rejected listener's failed configuration under
// error_state, so presence of its name in the dump is not acceptance.
func listenerStates(body string) map[string]listenerState {
	var dump struct {
		Configs []struct {
			Dynamic []struct {
				Name   string `json:"name"`
				Active struct {
					Listener struct {
						FilterChains []struct {
							Filters []struct {
								TypedConfig struct {
									StatPrefix string `json:"stat_prefix"`
								} `json:"typed_config"`
							} `json:"filters"`
						} `json:"filter_chains"`
					} `json:"listener"`
				} `json:"active_state"`
				Error struct {
					Details string `json:"details"`
				} `json:"error_state"`
			} `json:"dynamic_listeners"`
		} `json:"configs"`
	}
	states := map[string]listenerState{}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return states
	}
	for _, config := range dump.Configs {
		for _, l := range config.Dynamic {
			if l.Name == "" {
				continue
			}
			state := listenerState{err: l.Error.Details}
			for _, fc := range l.Active.Listener.FilterChains {
				for _, f := range fc.Filters {
					state.active = true
					state.statPrefix = f.TypedConfig.StatPrefix
				}
			}
			states[l.Name] = state
		}
	}
	return states
}
