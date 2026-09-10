// envoyprobe characterizes a real Envoy without kgateway or SnapshotCache.
// RF-004: receipt, initialization and traffic are distinct observations.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	percent "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
)

func packed(p proto.Message) *anypb.Any {
	a, err := anypb.New(p)
	if err != nil {
		panic(err)
	}
	return a
}
func address(port uint32) *core.Address {
	return &core.Address{Address: &core.Address_SocketAddress{SocketAddress: &core.SocketAddress{Address: "127.0.0.1", PortSpecifier: &core.SocketAddress_PortValue{PortValue: port}}}}
}
func ads() *core.ConfigSource {
	return &core.ConfigSource{ResourceApiVersion: core.ApiVersion_V3, ConfigSourceSpecifier: &core.ConfigSource_Ads{Ads: &core.AggregatedConfigSource{}}, InitialFetchTimeout: durationpb.New(0)}
}
func resources(phase int, disablePanic bool) map[string][]*anypb.Any {
	c := &cluster.Cluster{Name: "a", ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &cluster.Cluster_Type{Type: cluster.Cluster_EDS}, EdsClusterConfig: &cluster.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	if disablePanic {
		c.CommonLbConfig = &cluster.Cluster_CommonLbConfig{HealthyPanicThreshold: &percent.Percent{Value: 0}}
	}
	if phase >= 3 {
		c.ConnectTimeout = durationpb.New(2 * time.Second)
	}
	rc := &route.RouteConfiguration{Name: "routes", VirtualHosts: []*route.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*route.Route{{Match: &route.RouteMatch{PathSpecifier: &route.RouteMatch_Prefix{Prefix: "/"}}, Action: &route.Route_Route{Route: &route.RouteAction{ClusterSpecifier: &route.RouteAction_Cluster{Cluster: "a"}}}}}}}}
	hm := &hcm.HttpConnectionManager{StatPrefix: "probe", RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
	l := &listener.Listener{Name: "front", Address: &core.Address{Address: &core.Address_SocketAddress{SocketAddress: &core.SocketAddress{Address: "0.0.0.0", PortSpecifier: &core.SocketAddress_PortValue{PortValue: 10000}}}}, FilterChains: []*listener.FilterChain{{Filters: []*listener.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &listener.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	ep := &endpoint.ClusterLoadAssignment{ClusterName: "a"}
	if phase >= 2 && phase != 6 {
		ep.Endpoints = []*endpoint.LocalityLbEndpoints{{LbEndpoints: []*endpoint.LbEndpoint{{HostIdentifier: &endpoint.LbEndpoint_Endpoint{Endpoint: &endpoint.Endpoint{Address: address(10001)}}}}}}
	}
	if phase == 5 {
		ep.Endpoints[0].LbEndpoints[0].HealthStatus = core.HealthStatus_UNHEALTHY
	}
	r := map[string][]*anypb.Any{resource.ClusterType: {packed(c)}, resource.ListenerType: {packed(l)}, resource.RouteType: {packed(rc)}}
	if phase > 0 {
		r[resource.EndpointType] = []*anypb.Any{packed(ep)}
	}
	return r
}

type probeServer struct {
	disablePanic bool
	discovery.UnimplementedAggregatedDiscoveryServiceServer
	updates chan int
	log     *json.Encoder
	mu      sync.Mutex
}

func (s *probeServer) record(v any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.log.Encode(v); err != nil {
		panic(err)
	}
}
func (s *probeServer) StreamAggregatedResources(st discovery.AggregatedDiscoveryService_StreamAggregatedResourcesServer) error {
	reqs := make(chan *discovery.DiscoveryRequest)
	errs := make(chan error, 1)
	go func() {
		for {
			r, err := st.Recv()
			if err != nil {
				errs <- err
				return
			}
			select {
			case reqs <- r:
			case <-st.Context().Done():
				return
			}
		}
	}()
	phase, nonce := 0, 0
	requests := map[string]*discovery.DiscoveryRequest{}
	sent := map[string]string{}
	send := func(typ string) error {
		r := requests[typ]
		if r == nil {
			return nil
		}
		rs, ok := resources(phase, s.disablePanic)[typ]
		if !ok {
			return nil
		}
		version := "base"
		if typ == resource.EndpointType {
			version = strconv.Itoa(phase)
			if phase == 3 || phase == 4 {
				version = "2"
			}
		}
		if typ == resource.ClusterType && phase >= 3 {
			version = "rewarm"
		}
		if sent[typ] == version {
			return nil
		}
		nonce++
		out := &discovery.DiscoveryResponse{TypeUrl: typ, Resources: rs, VersionInfo: version, Nonce: strconv.Itoa(nonce)}
		s.record(map[string]any{"event": "response", "type": typ, "version": version, "nonce": out.Nonce, "phase": phase})
		if err := st.Send(out); err != nil {
			return err
		}
		sent[typ] = version
		return nil
	}
	for {
		select {
		case <-st.Context().Done():
			return st.Context().Err()
		case err := <-errs:
			return err
		case phase = <-s.updates:
			if phase == 4 {
				delete(sent, resource.EndpointType)
			} // explicitly replay the unchanged CLA
			for _, typ := range []string{resource.ClusterType, resource.EndpointType, resource.ListenerType, resource.RouteType} {
				if err := send(typ); err != nil {
					return err
				}
			}
		case r := <-reqs:
			s.record(map[string]any{"event": "request", "type": r.TypeUrl, "version": r.VersionInfo, "nonce": r.ResponseNonce, "names": r.ResourceNames, "error": r.ErrorDetail})
			if r.ErrorDetail != nil {
				return fmt.Errorf("Envoy NACK: %v", r.ErrorDetail)
			}
			requests[r.TypeUrl] = r
			if err := send(r.TypeUrl); err != nil {
				return err
			}
		}
	}
}

func command(args ...string) (string, error) {
	b, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker %v: %w: %s", args, err, b)
	}
	return strings.TrimSpace(string(b)), nil
}
func get(url string) (int, string, error) {
	c := http.Client{Timeout: time.Second}
	r, err := c.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	return r.StatusCode, string(b), err
}
func await(ctx context.Context, f func() bool) error {
	for {
		if f() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// Inspect the actual dynamic-cluster section, not a name elsewhere in the dump.
func hasCluster(body string, warming bool) bool {
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
		return false
	}
	for _, config := range dump.Configs {
		entries := config.Active
		if warming {
			entries = config.Warming
		}
		for _, e := range entries {
			if e.Cluster.Name == "a" {
				return true
			}
		}
	}
	return false
}

func endpointState(body string, phase int) bool {
	var dump struct {
		Configs []struct {
			Endpoints []struct {
				Config struct {
					Name       string `json:"cluster_name"`
					Localities []struct {
						Hosts []struct {
							Health string `json:"health_status"`
						} `json:"lb_endpoints"`
					} `json:"endpoints"`
				} `json:"endpoint_config"`
			} `json:"dynamic_endpoint_configs"`
		} `json:"configs"`
	}
	if err := json.Unmarshal([]byte(body), &dump); err != nil {
		return false
	}
	for _, config := range dump.Configs {
		for _, entry := range config.Endpoints {
			if entry.Config.Name != "a" {
				continue
			}
			count := 0
			health := ""
			for _, locality := range entry.Config.Localities {
				for _, host := range locality.Hosts {
					count++
					health = host.Health
				}
			}
			if phase == 6 {
				return count == 0
			}
			if phase == 5 {
				return count == 1 && health == "UNHEALTHY"
			}
			return count == 1 && health == "HEALTHY"
		}
	}
	return false
}

func run() (runErr error) {
	disablePanic := flag.Bool("disable-panic", false, "set healthy panic threshold to zero")
	image := flag.String("image", "envoyproxy/envoy:v1.39.1@sha256:57e14a549d7bd43c8d3f6d03e8cfa653e037d4b38e133acd9b54f38c524401b4", "local Envoy image (pull explicitly first)")
	out := flag.String("out", "", "required artifact directory")
	flag.Parse()
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	dir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	info, err := command("image", "inspect", *image)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "image.json"), []byte(info), 0644); err != nil {
		return err
	}
	lis, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return err
	}
	defer lis.Close()
	logfile, err := os.Create(filepath.Join(dir, "wire.jsonl"))
	if err != nil {
		return err
	}
	defer logfile.Close()
	p := &probeServer{disablePanic: *disablePanic, updates: make(chan int, 1), log: json.NewEncoder(logfile)}
	srv := grpc.NewServer()
	discovery.RegisterAggregatedDiscoveryServiceServer(srv, p)
	defer srv.Stop()
	go func() {
		if err := srv.Serve(lis); err != nil {
			p.record(map[string]any{"event": "server-error", "error": err.Error()})
		}
	}()
	port := lis.Addr().(*net.TCPAddr).Port
	bootstrap := fmt.Sprintf(`{
 "node":{"id":"formal-probe","cluster":"formal-probe"},
 "admin":{"address":{"socket_address":{"address":"0.0.0.0","port_value":9901}}},
 "dynamic_resources":{"ads_config":{"api_type":"GRPC","transport_api_version":"V3","grpc_services":[{"envoy_grpc":{"cluster_name":"xds"}}]},"cds_config":{"ads":{},"resource_api_version":"V3","initial_fetch_timeout":"0s"},"lds_config":{"ads":{},"resource_api_version":"V3","initial_fetch_timeout":"0s"}},
 "static_resources":{"clusters":[{"name":"xds","type":"LOGICAL_DNS","connect_timeout":"1s","http2_protocol_options":{},"load_assignment":{"cluster_name":"xds","endpoints":[{"lb_endpoints":[{"endpoint":{"address":{"socket_address":{"address":"host.docker.internal","port_value":%d}}}}]}]}}],
 "listeners":[{"name":"upstream","address":{"socket_address":{"address":"127.0.0.1","port_value":10001}},"filter_chains":[{"filters":[{"name":"envoy.filters.network.http_connection_manager","typed_config":{"@type":"type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager","stat_prefix":"upstream","route_config":{"virtual_hosts":[{"name":"all","domains":["*"],"routes":[{"match":{"prefix":"/"},"direct_response":{"status":200,"body":{"inline_string":"probe-upstream"}}}]}]},"http_filters":[{"name":"envoy.filters.http.router","typed_config":{"@type":"type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"}}]}}]}]}]}}
`, port)
	cfg := filepath.Join(dir, "bootstrap.json")
	if err = os.WriteFile(cfg, []byte(bootstrap), 0644); err != nil {
		return err
	}
	dockerOS, err := command("info", "--format", "{{.OperatingSystem}}")
	if err != nil {
		return err
	}
	runArgs := []string{"run", "-d", "--rm"}
	if !strings.Contains(dockerOS, "Docker Desktop") {
		runArgs = append(runArgs, "--add-host", "host.docker.internal:host-gateway")
	}
	runArgs = append(runArgs, "-p", "127.0.0.1::9901", "-p", "127.0.0.1::10000", "-v", cfg+":/probe.json:ro", *image, "-c", "/probe.json", "--concurrency", "1", "--disable-hot-restart", "--log-level", "info")
	id, err := command(runArgs...)
	if err != nil {
		return err
	}
	defer func() {
		logs, logErr := command("logs", id)
		writeErr := os.WriteFile(filepath.Join(dir, "envoy.log"), []byte(logs), 0644)
		_, cleanupErr := command("rm", "-f", id)
		runErr = errors.Join(runErr, logErr, writeErr, cleanupErr)
	}()

	published := func(port string) (string, error) { s, err := command("port", id, port); return "http://" + s, err }
	admin, err := published("9901/tcp")
	if err != nil {
		return err
	}
	front, err := published("10000/tcp")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err = await(ctx, func() bool { code, _, _ := get(admin + "/server_info"); return code == 200 }); err != nil {
		return fmt.Errorf("admin unavailable: %w", err)
	}
	save := func(name, path string) string {
		_, body, e := get(admin + path)
		if e != nil {
			panic(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); e != nil {
			panic(e)
		}
		return body
	}
	save("server-info.json", "/server_info")
	// Wait for a concrete warming cluster rather than assuming elapsed time
	// means the missing-EDS state has been reached.
	if err = await(ctx, func() bool {
		_, b, _ := get(admin + "/config_dump")
		return hasCluster(b, true)
	}); err != nil {
		return fmt.Errorf("missing EDS did not warm: %w", err)
	}
	code, _, err := get(admin + "/ready")
	if err != nil || code != 503 {
		return fmt.Errorf("missing EDS ready status=%d err=%v", code, err)
	}
	save("missing-config.json", "/config_dump")
	p.record(map[string]any{"event": "observation", "phase": "missing", "ready": code})
	p.updates <- 1
	if err = await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
		return fmt.Errorf("empty EDS did not initialize: %w", err)
	}
	if err = await(ctx, func() bool { code, _, _ := get(front + "/"); return code == 503 }); err != nil {
		return fmt.Errorf("empty EDS traffic was not degraded: %w", err)
	}
	save("empty-config.json", "/config_dump")
	save("empty-clusters.json", "/clusters?format=json")
	p.record(map[string]any{"event": "observation", "phase": "empty", "ready": 200, "traffic": 503})
	p.updates <- 2
	if err = await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("ready EDS traffic did not recover: %w", err)
	}
	save("ready-config.json", "/config_dump")
	save("ready-clusters.json", "/clusters?format=json")
	p.record(map[string]any{"event": "observation", "phase": "ready", "ready": 200, "traffic": 200})
	p.updates <- 3 // changed CDS, unchanged EDS deliberately withheld
	if err = await(ctx, func() bool { _, b, _ := get(admin + "/config_dump"); return hasCluster(b, true) }); err != nil {
		return fmt.Errorf("changed CDS did not rewarm: %w", err)
	}
	code, body, err := get(front + "/")
	if err != nil || code != 200 || body != "probe-upstream" {
		return fmt.Errorf("rewarming did not preserve old traffic: status=%d err=%v", code, err)
	}
	save("rewarming-config.json", "/config_dump")
	p.record(map[string]any{"event": "observation", "phase": "rewarming-without-eds-replay", "traffic": 200})
	p.updates <- 4 // identical CLA content AND version, but a fresh wire response
	if err = await(ctx, func() bool {
		_, b, _ := get(admin + "/config_dump")
		return !hasCluster(b, true) && hasCluster(b, false)
	}); err != nil {
		return fmt.Errorf("same-version EDS replay did not finish rewarming: %w", err)
	}
	save("rewarmed-config.json", "/config_dump")
	p.record(map[string]any{"event": "observation", "phase": "same-version-eds-replay", "warming": false})
	for _, phase := range []int{5, 6, 7} {
		p.updates <- phase
		if err = await(ctx, func() bool {
			_, b, _ := get(admin + "/config_dump?include_eds")
			return endpointState(b, phase)
		}); err != nil {
			return fmt.Errorf("phase %d endpoint receipt missing: %w", phase, err)
		}
		want := 503
		if phase == 7 || (phase == 5 && !*disablePanic) {
			want = 200
		}
		if err = await(ctx, func() bool { code, _, _ := get(front + "/"); return code == want }); err != nil {
			return fmt.Errorf("phase %d: traffic did not reach %d: %w", phase, want, err)
		}
		code, _, err := get(admin + "/ready")
		if err != nil || code != 200 {
			return fmt.Errorf("endpoint loss changed process readiness: status=%d err=%v", code, err)
		}
		clusterDump := save(fmt.Sprintf("phase-%d-clusters.json", phase), "/clusters?format=json")
		if phase == 5 && !strings.Contains(clusterDump, `"eds_health_status": "UNHEALTHY"`) {
			return fmt.Errorf("unhealthy phase did not install unhealthy host")
		}
		p.record(map[string]any{"event": "observation", "phase": phase, "ready": 200, "traffic": want})
	}
	fmt.Println("PASS missing/empty/ready EDS, same-version rewarming, unhealthy/empty truth, recovery")

	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
