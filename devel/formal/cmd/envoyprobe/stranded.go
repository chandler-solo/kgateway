package main

import (
	"bufio"
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
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Scenario "stranded" (RF-035): one resource Envoy rejects from a CDS or LDS
// response freezes the proxy's EDS or RDS, and the freeze outlives the
// resource. Envoy applies the valid resources of the NACKed response and never
// subscribes to the rejected one's assignment or route configuration. The
// pinned ADS SnapshotCache then declines every named response carrying that
// name, and declines Envoy's next request on the request path, which leaves no
// watch. It requires -snapshot-cache: the subject is the dependency.
//
//	phase 0  cluster a, endpoint port 10009 (refuses connections); listener
//	         front on 10000 with routes r1: /a -> a, /b -> b, / -> "r1-v1"
//	phase 1  adds cluster b (endpoint 10001), cluster x (CLUSTER_PROVIDED on an
//	         EDS cluster, which Envoy rejects), listener added on 10002 (r2) and
//	         listener rejected on 10004 (r3, an HTTP filter config of a type
//	         with no filter factory)
//	phase 2  a's endpoint moves to the working port 10001; r1 / -> "r1-v2"
//	phase 3  removes x and rejected; r1 / -> "r1-v3"
//	phase 4  adds cluster c and listener later on 10006 (r4), which changes the
//	         EDS and RDS names Envoy requests
//
// Passing means the defect reproduced: a stays on the refusing endpoint and r1
// on its first version after phase 3, until phase 4 changes the names. Admin
// state and HTTP are checked independently of the wire log, which is used only
// to require that no EDS or RDS response was sent in the stranded windows.
func strandedResources(phase int) map[string][]*anypb.Any {
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	}
	cla := func(name string, port uint32) *envoyendpointv3.ClusterLoadAssignment {
		return &envoyendpointv3.ClusterLoadAssignment{ClusterName: name, Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(port)}}}}}}}
	}
	toCluster := func(prefix, cluster string) *envoyroutev3.Route {
		return &envoyroutev3.Route{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: prefix}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: cluster}}}}
	}
	direct := func(body string) *envoyroutev3.Route {
		return &envoyroutev3.Route{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_DirectResponse{DirectResponse: &envoyroutev3.DirectResponseAction{Status: 200, Body: &envoycorev3.DataSource{Specifier: &envoycorev3.DataSource_InlineString{InlineString: body}}}}}
	}
	routes := func(name string, rs ...*envoyroutev3.Route) *envoyroutev3.RouteConfiguration {
		return &envoyroutev3.RouteConfiguration{Name: name, VirtualHosts: []*envoyroutev3.VirtualHost{{Name: name, Domains: []string{"*"}, Routes: rs}}}
	}
	listener := func(name, routeConfig string, port uint32, rejected bool) *envoylistenerv3.Listener {
		filters := []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}
		if rejected {
			// A known message type with no HTTP filter factory, so the listener
			// fails while Envoy builds it. An unknown type would fail the same
			// way but would also make /config_dump unserializable, because
			// Envoy keeps the rejected listener in its error_state.
			filters = append([]*hcm.HttpFilter{{Name: "probe.not_a_filter", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&envoyclusterv3.Cluster{Name: "not-a-filter"})}}}, filters...)
		}
		hm := &hcm.HttpConnectionManager{StatPrefix: name, RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: routeConfig, ConfigSource: ads()}}, HttpFilters: filters}
		return &envoylistenerv3.Listener{Name: name, Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: port}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	}

	portA := uint32(10009)
	if phase >= 2 {
		portA = 10001
	}
	body := fmt.Sprintf("r1-v%d", max(1, phase))
	if phase == 4 {
		body = "r1-v3"
	}
	clusters := []*anypb.Any{packed(edsCluster("a"))}
	endpoints := []*anypb.Any{packed(cla("a", portA))}
	listeners := []*anypb.Any{packed(listener("front", "r1", 10000, false))}
	routeConfigs := []*anypb.Any{packed(routes("r1", toCluster("/a", "a"), toCluster("/b", "b"), direct(body)))}
	if phase >= 1 {
		clusters = append(clusters, packed(edsCluster("b")))
		endpoints = append(endpoints, packed(cla("b", 10001)))
		listeners = append(listeners, packed(listener("added", "r2", 10002, false)))
		routeConfigs = append(routeConfigs, packed(routes("r2", direct("r2-v1"))))
	}
	if phase == 1 || phase == 2 {
		x := edsCluster("x")
		x.LbPolicy = envoyclusterv3.Cluster_CLUSTER_PROVIDED
		clusters = append(clusters, packed(x))
		endpoints = append(endpoints, packed(cla("x", 10001)))
		listeners = append(listeners, packed(listener("rejected", "r3", 10004, true)))
		routeConfigs = append(routeConfigs, packed(routes("r3", direct("r3-v1"))))
	}
	if phase >= 4 {
		clusters = append(clusters, packed(edsCluster("c")))
		endpoints = append(endpoints, packed(cla("c", 10001)))
		listeners = append(listeners, packed(listener("later", "r4", 10006, false)))
		routeConfigs = append(routeConfigs, packed(routes("r4", direct("r4-v1"))))
	}
	return map[string][]*anypb.Any{
		resource.ClusterType:  clusters,
		resource.EndpointType: endpoints,
		resource.ListenerType: listeners,
		resource.RouteType:    routeConfigs,
	}
}

// strandedEndpointPorts reads cluster -> endpoint ports from a config dump with
// include_eds.
func strandedEndpointPorts(body string) map[string][]uint32 {
	var dump struct {
		Configs []struct {
			Endpoints []struct {
				Config struct {
					Name       string `json:"cluster_name"`
					Localities []struct {
						Hosts []struct {
							Endpoint struct {
								Address struct {
									Socket struct {
										Port uint32 `json:"port_value"`
									} `json:"socket_address"`
								} `json:"address"`
							} `json:"endpoint"`
						} `json:"lb_endpoints"`
					} `json:"endpoints"`
				} `json:"endpoint_config"`
			} `json:"dynamic_endpoint_configs"`
		} `json:"configs"`
	}
	out := map[string][]uint32{}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return out
	}
	for _, config := range dump.Configs {
		for _, entry := range config.Endpoints {
			ports := []uint32{}
			for _, locality := range entry.Config.Localities {
				for _, host := range locality.Hosts {
					ports = append(ports, host.Endpoint.Address.Socket.Port)
				}
			}
			out[entry.Config.Name] = ports
		}
	}
	return out
}

// strandedClusters reads the active and warming dynamic cluster names.
func strandedClusters(body string) (active, warming []string) {
	type entry struct {
		Cluster struct {
			Name string `json:"name"`
		} `json:"cluster"`
	}
	var dump struct {
		Configs []struct {
			Warming []entry `json:"dynamic_warming_clusters"`
			Active  []entry `json:"dynamic_active_clusters"`
		} `json:"configs"`
	}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return nil, nil
	}
	for _, config := range dump.Configs {
		for _, e := range config.Active {
			active = append(active, e.Cluster.Name)
		}
		for _, e := range config.Warming {
			warming = append(warming, e.Cluster.Name)
		}
	}
	slices.Sort(active)
	slices.Sort(warming)
	return active, warming
}

// strandedListeners reads dynamic listener names by state.
func strandedListeners(body string) (active, warming []string) {
	var dump struct {
		Configs []struct {
			Listeners []struct {
				Name    string          `json:"name"`
				Active  json.RawMessage `json:"active_state"`
				Warming json.RawMessage `json:"warming_state"`
			} `json:"dynamic_listeners"`
		} `json:"configs"`
	}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return nil, nil
	}
	for _, config := range dump.Configs {
		for _, l := range config.Listeners {
			if len(l.Active) > 0 {
				active = append(active, l.Name)
			}
			if len(l.Warming) > 0 {
				warming = append(warming, l.Name)
			}
		}
	}
	slices.Sort(active)
	slices.Sort(warming)
	return active, warming
}

// strandedResponsesSince counts responses of typ recorded in the wire log after
// the marker event named marker, or in the whole log when marker is empty.
func strandedResponsesSince(dir, marker, typ string) (int, error) {
	f, err := os.Open(filepath.Join(dir, "wire.jsonl"))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	seen, count := marker == "", 0
	for scanner.Scan() {
		var event struct {
			Event string `json:"event"`
			Name  string `json:"name"`
			Type  string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch {
		case event.Event == "marker" && event.Name == marker:
			seen, count = true, 0
		case seen && event.Event == "response" && event.Type == typ:
			count++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if !seen {
		return 0, fmt.Errorf("wire log has no marker %q", marker)
	}
	return count, nil
}

func runStranded(ctx context.Context, dir, admin, front, added, later string, p *probeServer, advance func(int) error) error {
	save := func(name, path string) (string, error) {
		_, body, err := get(admin + path)
		if err != nil {
			return "", err
		}
		return body, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	mark := func(name string) { p.record(map[string]any{"event": "marker", "name": name}) }
	portsOf := func(cluster string) []uint32 {
		_, body, _ := get(admin + "/config_dump?include_eds")
		return strandedEndpointPorts(body)[cluster]
	}
	status := func(url string) int { code, _, _ := get(url); return code }
	frontBody := func() string { _, body, _ := get(front + "/"); return body }
	requestedSince := func(n int, typ string) [][]string {
		var names [][]string
		for _, r := range p.requestsSince(n) {
			if r.typeURL == typ {
				names = append(names, r.names)
			}
		}
		return names
	}
	noNamedResponses := func(marker string) error {
		for _, typ := range []string{resource.EndpointType, resource.RouteType} {
			n, err := strandedResponsesSince(dir, marker, typ)
			if err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("%s: %d %s response(s) sent; the stranded schedule expects none (reassess RF-035)", marker, n, typ)
			}
		}
		return nil
	}
	// stranded samples a fixed window: a stays on the refusing endpoint, /a
	// keeps failing, and front keeps r1-v1. A finite window is a
	// characterization, not a proof that no later event answers.
	stranded := func(phase string, window time.Duration) error {
		deadline := time.Now().Add(window)
		for time.Now().Before(deadline) {
			if got := portsOf("a"); !slices.Equal(got, []uint32{10009}) {
				return fmt.Errorf("%s: cluster a endpoints %v, want the stale [10009]", phase, got)
			}
			if code := status(front + "/a"); code != 503 {
				return fmt.Errorf("%s: /a returned %d, want 503 from the stale endpoint", phase, code)
			}
			if body := frontBody(); body != "r1-v1" {
				return fmt.Errorf("%s: front served %q, want the stale r1-v1", phase, body)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
		}
		return nil
	}

	// Phase 0: a active on the refusing endpoint; front on r1-v1.
	if err := await(ctx, func() bool { return slices.Equal(portsOf("a"), []uint32{10009}) && frontBody() == "r1-v1" }); err != nil {
		return fmt.Errorf("baseline did not apply: %w", err)
	}
	if code := status(front + "/a"); code != 503 {
		return fmt.Errorf("baseline /a returned %d, want 503 from the refusing endpoint", code)
	}
	if _, err := save("baseline-config.json", "/config_dump?include_eds"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "baseline", "a_ports": []uint32{10009}, "front": "r1-v1", "a_status": 503})

	// Phase 1: add b and the rejected x; add listener added and the rejected listener.
	requests0 := p.requestCount()
	mark("added-with-rejected")
	if err := advance(1); err != nil {
		return err
	}
	if err := await(ctx, func() bool {
		for _, names := range requestedSince(requests0, resource.EndpointType) {
			if slices.Contains(names, "b") {
				return true
			}
		}
		return false
	}); err != nil {
		return fmt.Errorf("Envoy did not request endpoints for the applied cluster b: %w", err)
	}
	if err := await(ctx, func() bool {
		for _, names := range requestedSince(requests0, resource.RouteType) {
			if slices.Contains(names, "r2") {
				return true
			}
		}
		return false
	}); err != nil {
		return fmt.Errorf("Envoy did not request routes for the applied listener added: %w", err)
	}
	for _, names := range requestedSince(requests0, resource.EndpointType) {
		if slices.Contains(names, "x") {
			return fmt.Errorf("Envoy requested endpoints for the rejected cluster x (%v); reassess RF-035", names)
		}
	}
	for _, names := range requestedSince(requests0, resource.RouteType) {
		if slices.Contains(names, "r3") {
			return fmt.Errorf("Envoy requested routes for the rejected listener (%v); reassess RF-035", names)
		}
	}
	body, err := save("rejected-config.json", "/config_dump")
	if err != nil {
		return err
	}
	activeClusters, warmingClusters := strandedClusters(body)
	activeListeners, warmingListeners := strandedListeners(body)
	if slices.Contains(activeClusters, "x") || slices.Contains(warmingClusters, "x") {
		return errors.New("Envoy accepted the CLUSTER_PROVIDED EDS cluster x; the rejection premise no longer holds")
	}
	if slices.Contains(activeListeners, "rejected") || slices.Contains(warmingListeners, "rejected") {
		return errors.New("Envoy accepted the listener with an unregistered HTTP filter; the rejection premise no longer holds")
	}
	p.record(map[string]any{"event": "observation", "phase": "added-with-rejected", "active_clusters": activeClusters, "warming_clusters": warmingClusters, "active_listeners": activeListeners, "warming_listeners": warmingListeners, "eds_requests": requestedSince(requests0, resource.EndpointType), "rds_requests": requestedSince(requests0, resource.RouteType)})

	// Phase 2: the fix for a's endpoints and a route change, while x and the
	// rejected listener are still published.
	mark("withheld")
	if err = advance(2); err != nil {
		return err
	}
	if err = stranded("withheld", 3*time.Second); err != nil {
		return err
	}
	if err = noNamedResponses("added-with-rejected"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "withheld", "a_ports": portsOf("a"), "a_status": status(front + "/a"), "front": frontBody()})

	// b and listener added initialize on their fetch timeouts: empty, and with no routes.
	if err = await(ctx, func() bool {
		_, d, _ := get(admin + "/config_dump?include_eds")
		active, _ := strandedClusters(d)
		listenersUp, _ := strandedListeners(d)
		return slices.Contains(active, "b") && len(strandedEndpointPorts(d)["b"]) == 0 && slices.Contains(listenersUp, "added")
	}); err != nil {
		return fmt.Errorf("b and listener added did not initialize on their fetch timeouts: %w", err)
	}
	bStatus, addedStatus := status(front+"/b"), status(added+"/")
	if bStatus != 503 {
		return fmt.Errorf("/b returned %d, want 503 from the empty cluster", bStatus)
	}
	if addedStatus != 404 {
		return fmt.Errorf("listener added returned %d, want 404 with no routes", addedStatus)
	}
	if _, err = save("timed-out-config.json", "/config_dump?include_eds"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "timed-out", "b_status": bStatus, "added_status": addedStatus, "fetch_timeout": fetchTimeoutLabel()})

	// Phase 3: remove x and the rejected listener. Envoy accepts CDS and LDS,
	// but its EDS and RDS names do not change, and no watch remains to answer.
	requests3 := p.requestCount()
	mark("removed")
	if err = advance(3); err != nil {
		return err
	}
	// A NACK reports the previous version, so a request at r3 is the ACK.
	acked := func(typ string) bool {
		for _, r := range p.requestsSince(requests3) {
			if r.typeURL == typ && r.version == "r3" {
				return true
			}
		}
		return false
	}
	if err = await(ctx, func() bool { return acked(resource.ClusterType) && acked(resource.ListenerType) }); err != nil {
		return fmt.Errorf("Envoy did not ACK CDS and LDS without the rejected resources: %w", err)
	}
	// The dump's version_info stays at the last update that changed a
	// resource, so it does not show this ACK.
	removedDump, err := save("removed-config.json", "/config_dump?include_eds")
	if err != nil {
		return err
	}
	if err = stranded("removed", 5*time.Second); err != nil {
		return err
	}
	if err = noNamedResponses("removed"); err != nil {
		return err
	}
	if _, err = save("stranded-config.json", "/config_dump?include_eds"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "removed", "cds_ack": "r3", "lds_ack": "r3", "cds_dump_version": acceptedVersion(removedDump, "ClustersConfigDump"), "lds_dump_version": acceptedVersion(removedDump, "ListenersConfigDump"), "a_ports": portsOf("a"), "a_status": status(front + "/a"), "front": frontBody(), "window_ms": 5000})

	// Phase 4: a new cluster and listener change the requested names, which is
	// the only recovery observed short of reconnecting.
	mark("names-changed")
	if err = advance(4); err != nil {
		return err
	}
	if err = await(ctx, func() bool {
		code, upstream, _ := get(front + "/a")
		_, laterBody, _ := get(later + "/")
		return slices.Equal(portsOf("a"), []uint32{10001}) && code == 200 && upstream == "probe-upstream" && frontBody() == "r1-v3" && laterBody == "r4-v1"
	}); err != nil {
		return fmt.Errorf("changing the requested names did not recover EDS and RDS: %w", err)
	}
	if _, err = save("recovered-config.json", "/config_dump?include_eds"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "names-changed", "a_ports": portsOf("a"), "a_status": 200, "front": "r1-v3", "later": "r4-v1"})
	fmt.Println("PASS Envoy stranded characterization: a rejected cluster and listener froze EDS and RDS, and the freeze outlived their removal until the requested names changed")
	return nil
}

// acceptedVersion reads the version_info of one config dump section, such as
// ClustersConfigDump, which is the version Envoy last accepted for that type.
func acceptedVersion(body, section string) string {
	var dump struct {
		Configs []struct {
			Type    string `json:"@type"`
			Version string `json:"version_info"`
		} `json:"configs"`
	}
	if json.Unmarshal([]byte(body), &dump) != nil {
		return ""
	}
	for _, c := range dump.Configs {
		if c.Type == "type.googleapis.com/envoy.admin.v3."+section {
			return c.Version
		}
	}
	return ""
}

// Scenario "stranded-fresh" (RF-035): a new proxy connects while a cluster it
// rejects is already published. Requires -snapshot-cache.
//
//	phase 0  clusters a (endpoint 10001, serving) and x (CLUSTER_PROVIDED on an
//	         EDS cluster, rejected); listener front on 10000 with routes r1:
//	         /a -> a, / -> "r1-v1"
//	phase 1  removes x
//	phase 2  adds cluster c, which changes the EDS names Envoy requests
//
// Passing means the defect reproduced: Envoy's first EDS request is declined
// with no watch left, a initializes empty at its fetch timeout, readiness
// passes, /a returns 503, and removing x changes nothing until phase 2.
func strandedFreshResources(phase int) map[string][]*anypb.Any {
	base := strandedResources(0)
	edsCluster := func(name string) *envoyclusterv3.Cluster {
		return &envoyclusterv3.Cluster{Name: name, ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	}
	cla := func(name string, port uint32) *envoyendpointv3.ClusterLoadAssignment {
		return &envoyendpointv3.ClusterLoadAssignment{ClusterName: name, Endpoints: []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(port)}}}}}}}
	}
	clusters := []*anypb.Any{packed(edsCluster("a"))}
	endpoints := []*anypb.Any{packed(cla("a", 10001))}
	if phase == 0 {
		x := edsCluster("x")
		x.LbPolicy = envoyclusterv3.Cluster_CLUSTER_PROVIDED
		clusters = append(clusters, packed(x))
		endpoints = append(endpoints, packed(cla("x", 10001)))
	}
	if phase >= 2 {
		clusters = append(clusters, packed(edsCluster("c")))
		endpoints = append(endpoints, packed(cla("c", 10001)))
	}
	return map[string][]*anypb.Any{
		resource.ClusterType:  clusters,
		resource.EndpointType: endpoints,
		resource.ListenerType: base[resource.ListenerType],
		resource.RouteType:    base[resource.RouteType],
	}
}

func runStrandedFresh(ctx context.Context, dir, admin, front string, p *probeServer, advance func(int) error) error {
	save := func(name, path string) error {
		_, body, err := get(admin + path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}
	mark := func(name string) { p.record(map[string]any{"event": "marker", "name": name}) }
	status := func(url string) int { code, _, _ := get(url); return code }
	portsOf := func(cluster string) []uint32 {
		_, body, _ := get(admin + "/config_dump?include_eds")
		return strandedEndpointPorts(body)[cluster]
	}
	start := time.Now()

	// Envoy requests a's endpoints but never x's.
	if err := await(ctx, func() bool {
		for _, r := range p.requestsSince(0) {
			if r.typeURL == resource.EndpointType && slices.Contains(r.names, "a") {
				return true
			}
		}
		return false
	}); err != nil {
		return fmt.Errorf("Envoy never requested a's endpoints: %w", err)
	}
	for _, r := range p.requestsSince(0) {
		if r.typeURL == resource.EndpointType && slices.Contains(r.names, "x") {
			return fmt.Errorf("Envoy requested endpoints for the rejected cluster x (%v); reassess RF-035", r.names)
		}
	}

	// a initializes empty at its fetch timeout and the proxy reports ready.
	if err := await(ctx, func() bool {
		_, d, _ := get(admin + "/config_dump?include_eds")
		active, _ := strandedClusters(d)
		return slices.Contains(active, "a") && status(admin+"/ready") == 200
	}); err != nil {
		return fmt.Errorf("a did not initialize and the proxy did not become ready: %w", err)
	}
	initialized := time.Since(start)
	if got := portsOf("a"); len(got) != 0 {
		return fmt.Errorf("a has endpoints %v; the first EDS request was answered (reassess RF-035)", got)
	}
	aStatus := status(front + "/a")
	if aStatus != 503 {
		return fmt.Errorf("/a returned %d on the new proxy, want 503 from the empty cluster", aStatus)
	}
	if body := func() string { _, b, _ := get(front + "/"); return b }(); body != "r1-v1" {
		return fmt.Errorf("front served %q, want r1-v1: routes should arrive normally", body)
	}
	if err := noFreshEDS(dir, ""); err != nil {
		return err
	}
	if err := save("fresh-ready-config.json", "/config_dump?include_eds"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "ready-empty", "ready": 200, "a_status": aStatus, "a_ports": []uint32{}, "front": "r1-v1", "initialized_after_ms": initialized.Milliseconds(), "fetch_timeout": fetchTimeoutLabel()})

	// Phase 1: remove x. Envoy ACKs CDS, but no EDS watch remains.
	requests1 := p.requestCount()
	mark("removed")
	if err := advance(1); err != nil {
		return err
	}
	if err := await(ctx, func() bool {
		for _, r := range p.requestsSince(requests1) {
			if r.typeURL == resource.ClusterType && r.version == "r1" {
				return true
			}
		}
		return false
	}); err != nil {
		return fmt.Errorf("Envoy did not ACK CDS without x: %w", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := portsOf("a"); len(got) != 0 {
			return fmt.Errorf("a received endpoints %v after x was removed; reassess RF-035", got)
		}
		if code := status(front + "/a"); code != 503 {
			return fmt.Errorf("/a returned %d after x was removed, want the stranded 503", code)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err := noFreshEDS(dir, "removed"); err != nil {
		return err
	}
	if err := save("fresh-removed-config.json", "/config_dump?include_eds"); err != nil {
		return err
	}
	p.record(map[string]any{"event": "observation", "phase": "removed", "a_status": 503, "a_ports": []uint32{}, "window_ms": 5000, "ready": status(admin + "/ready")})

	// Phase 2: a new cluster changes the requested names.
	mark("names-changed")
	if err := advance(2); err != nil {
		return err
	}
	if err := await(ctx, func() bool {
		code, upstream, _ := get(front + "/a")
		return slices.Equal(portsOf("a"), []uint32{10001}) && code == 200 && upstream == "probe-upstream"
	}); err != nil {
		return fmt.Errorf("changing the requested names did not recover EDS: %w", err)
	}
	p.record(map[string]any{"event": "observation", "phase": "names-changed", "a_status": 200, "a_ports": []uint32{10001}})
	fmt.Println("PASS Envoy stranded-fresh characterization: a new proxy with a rejected cluster published initialized every EDS cluster empty, reported ready and served 503, and removing the cluster did not recover it")
	return nil
}

func noFreshEDS(dir, marker string) error {
	n, err := strandedResponsesSince(dir, marker, resource.EndpointType)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("%s: %d EDS response(s) sent; the stranded schedule expects none (reassess RF-035)", marker, n)
	}
	return nil
}
