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
	"sync/atomic"
	"time"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoyroutev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	router "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	percent "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/utils"
)

func packed(p proto.Message) *anypb.Any {
	a, err := utils.MessageToAny(p)
	if err != nil {
		panic(err)
	}
	return a
}

func address(port uint32) *envoycorev3.Address {
	return &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "127.0.0.1", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: port}}}}
}

func ads() *envoycorev3.ConfigSource {
	return &envoycorev3.ConfigSource{ResourceApiVersion: envoycorev3.ApiVersion_V3, ConfigSourceSpecifier: &envoycorev3.ConfigSource_Ads{Ads: &envoycorev3.AggregatedConfigSource{}}, InitialFetchTimeout: durationpb.New(0)}
}

func resources(phase int, disablePanic bool) map[string][]*anypb.Any {
	c := &envoyclusterv3.Cluster{Name: "a", ConnectTimeout: durationpb.New(time.Second), ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS}, EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{EdsConfig: ads()}}
	if disablePanic {
		c.CommonLbConfig = &envoyclusterv3.Cluster_CommonLbConfig{HealthyPanicThreshold: &percent.Percent{Value: 0}}
	}
	if phase >= 3 {
		c.ConnectTimeout = durationpb.New(2 * time.Second)
	}
	rc := &envoyroutev3.RouteConfiguration{Name: "routes", VirtualHosts: []*envoyroutev3.VirtualHost{{Name: "all", Domains: []string{"*"}, Routes: []*envoyroutev3.Route{{Match: &envoyroutev3.RouteMatch{PathSpecifier: &envoyroutev3.RouteMatch_Prefix{Prefix: "/"}}, Action: &envoyroutev3.Route_Route{Route: &envoyroutev3.RouteAction{ClusterSpecifier: &envoyroutev3.RouteAction_Cluster{Cluster: "a"}}}}}}}}
	hm := &hcm.HttpConnectionManager{StatPrefix: "probe", RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "routes", ConfigSource: ads()}}, HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.router", ConfigType: &hcm.HttpFilter_TypedConfig{TypedConfig: packed(&router.Router{})}}}}
	l := &envoylistenerv3.Listener{Name: "front", Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{Address: "0.0.0.0", PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 10000}}}}, FilterChains: []*envoylistenerv3.FilterChain{{Filters: []*envoylistenerv3.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &envoylistenerv3.Filter_TypedConfig{TypedConfig: packed(hm)}}}}}}
	ep := &envoyendpointv3.ClusterLoadAssignment{ClusterName: "a"}
	if phase >= 2 && phase != 6 {
		ep.Endpoints = []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: []*envoyendpointv3.LbEndpoint{{HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{Address: address(10001)}}}}}}
	}
	if phase == 5 {
		ep.Endpoints[0].LbEndpoints[0].HealthStatus = envoycorev3.HealthStatus_UNHEALTHY
	}
	r := map[string][]*anypb.Any{resource.ClusterType: {packed(c)}, resource.ListenerType: {packed(l)}, resource.RouteType: {packed(rc)}}
	if phase > 0 {
		r[resource.EndpointType] = []*anypb.Any{packed(ep)}
	}
	return r
}

type probeServer struct {
	disablePanic bool
	// scenario selects the resource schedule: "warming" (default) or
	// "references" (references.go).
	scenario string
	discovery.UnimplementedAggregatedDiscoveryServiceServer
	updates chan int
	// nacks receives the type URL of each NACKed response in scenarios that
	// expect rejections; the warming scenario treats any NACK as fatal.
	nacks chan string
	// secrets holds the per-run certificates for the "secrets" scenario.
	secrets secretSchedule
	// requests summarizes every DiscoveryRequest seen, in order (guarded by mu).
	requests []requestSummary
	// nackCount and responseCount total every NACK request and every response
	// observed on the wire in either server mode.
	nackCount     atomic.Int64
	responseCount atomic.Int64
	log           *json.Encoder
	mu            sync.Mutex
}

// observeNack records a NACK. The warming scenario treats any NACK as fatal;
// the references scenario expects them and does not resend the rejected
// version from the scripted server (the cache path resends it, RF-012).
func (s *probeServer) observeNack(typ string) error {
	if s.scenario == "warming" {
		return fmt.Errorf("Envoy NACK of %s", typ)
	}
	s.nackCount.Add(1)
	select {
	case s.nacks <- typ:
	default:
	}
	return nil
}

func (s *probeServer) resourcesFor(phase int) map[string][]*anypb.Any {
	switch s.scenario {
	case "references":
		return referenceResources(phase)
	case "rejection":
		return rejectionResources(phase)
	case "secrets":
		return s.secrets.resources(phase)
	case "restart":
		return restartResources(phase)
	default:
		return resources(phase, s.disablePanic)
	}
}

func (s *probeServer) versionFor(phase int, typ string) string {
	if s.scenario == "restart" {
		return restartVersion(phase)
	}
	if s.scenario != "warming" {
		return fmt.Sprintf("r%d", phase)
	}
	return versionFor(phase, typ)
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
		rs, ok := s.resourcesFor(phase)[typ]
		if !ok {
			return nil
		}
		version := s.versionFor(phase, typ)
		if sent[typ] == version {
			return nil
		}
		nonce++
		out := &discovery.DiscoveryResponse{TypeUrl: typ, Resources: rs, VersionInfo: version, Nonce: strconv.Itoa(nonce)}
		s.responseCount.Add(1)
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
			for _, typ := range []string{resource.ClusterType, resource.EndpointType, resource.ListenerType, resource.RouteType, resource.SecretType} {
				if err := send(typ); err != nil {
					return err
				}
			}
		case r := <-reqs:
			s.record(map[string]any{"event": "request", "type": r.TypeUrl, "version": r.VersionInfo, "nonce": r.ResponseNonce, "names": r.ResourceNames, "error": r.ErrorDetail})
			s.noteRequest(r.TypeUrl, r.VersionInfo, r.ResponseNonce, r.ResourceNames)
			if r.ErrorDetail != nil {
				// The rejected version stays recorded as sent, so the scripted
				// server does not answer a NACK by resending the rejected content.
				if err := s.observeNack(r.TypeUrl); err != nil {
					return err
				}
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
	useCache := flag.Bool("snapshot-cache", false, "use the actual go-control-plane cache/server")
	ordered := flag.Bool("ordered", false, "use ordered ADS with -snapshot-cache")
	disablePanic := flag.Bool("disable-panic", false, "set healthy panic threshold to zero")
	image := flag.String("image", "envoyproxy/envoy:v1.39.1@sha256:57e14a549d7bd43c8d3f6d03e8cfa653e037d4b38e133acd9b54f38c524401b4", "local Envoy image (pull explicitly first)")
	out := flag.String("out", "", "required artifact directory")
	scenario := flag.String("scenario", "warming", "resource schedule: warming (default), references, rejection, secrets, or restart (requires -snapshot-cache)")
	flag.Parse()
	if *out == "" {
		return errors.New("-out is required")
	}
	if *scenario != "warming" && *scenario != "references" && *scenario != "rejection" && *scenario != "secrets" && *scenario != "restart" {
		return fmt.Errorf("unknown -scenario %q", *scenario)
	}
	if *scenario != "warming" && *disablePanic {
		return fmt.Errorf("-scenario %s runs with default panic settings", *scenario)
	}
	if *scenario == "restart" && !*useCache {
		return errors.New("-scenario restart requires -snapshot-cache")
	}
	dir, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	info, err := command("image", "inspect", *image)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "image.json"), []byte(info), 0o600); err != nil {
		return err
	}
	// The Envoy container reaches this scripted ADS server through the Docker
	// host gateway, so the listener must not be loopback-only.
	lis, err := net.Listen("tcp", "0.0.0.0:0") //nolint:gosec // G102: reachable from the probe container by design
	if err != nil {
		return err
	}
	defer lis.Close()
	logfile, err := os.Create(filepath.Join(dir, "wire.jsonl"))
	if err != nil {
		return err
	}
	defer logfile.Close()
	p := &probeServer{disablePanic: *disablePanic, scenario: *scenario, updates: make(chan int, 1), nacks: make(chan string, 8), log: json.NewEncoder(logfile)}
	if *scenario == "secrets" {
		if p.secrets.first, err = newProbeCertificate("cert-1"); err != nil {
			return err
		}
		if p.secrets.second, err = newProbeCertificate("cert-2"); err != nil {
			return err
		}
	}
	srv := grpc.NewServer()
	serverCtx, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	advance := func(phase int) error { p.updates <- phase; return nil }
	if *useCache {
		advance, err = installCacheServer(serverCtx, srv, p, *ordered, true)
		if err != nil {
			return err
		}
	} else {
		if *ordered {
			return errors.New("-ordered requires -snapshot-cache")
		}
		discovery.RegisterAggregatedDiscoveryServiceServer(srv, p)
	}
	p.record(map[string]any{"event": "profile", "scenario": *scenario, "snapshot_cache": *useCache, "ordered": *ordered, "disable_panic": *disablePanic})

	defer func() { srv.Stop() }()
	go func() {
		if err := srv.Serve(lis); err != nil {
			p.record(map[string]any{"event": "server-error", "error": err.Error()})
		}
	}()
	// restart simulates a controller restart: the server stops, a fresh empty
	// SnapshotCache and server take over the same port, and Envoy reconnects.
	restart := func() (func(int) error, error) {
		addr := lis.Addr().String()
		srv.Stop()
		fresh, err := net.Listen("tcp", addr) //nolint:gosec // G102: same reachable address as the original listener
		if err != nil {
			return nil, err
		}
		next := grpc.NewServer()
		adv, err := installCacheServer(serverCtx, next, p, *ordered, false)
		if err != nil {
			return nil, err
		}
		go func() {
			if err := next.Serve(fresh); err != nil {
				p.record(map[string]any{"event": "server-error", "error": err.Error()})
			}
		}()
		srv = next
		return adv, nil
	}
	port := lis.Addr().(*net.TCPAddr).Port
	bootstrap := fmt.Sprintf(`{
 "node":{"id":"formal-probe","cluster":"formal-probe"},
 "admin":{"address":{"socket_address":{"address":"0.0.0.0","port_value":9901}}},
 "dynamic_resources":{"ads_config":{"api_type":"GRPC","transport_api_version":"V3","grpc_services":[{"envoy_grpc":{"cluster_name":"xds"}}]},"cds_config":{"ads":{},"resource_api_version":"V3","initial_fetch_timeout":"0s"},"lds_config":{"ads":{},"resource_api_version":"V3","initial_fetch_timeout":"0s"}},
 "static_resources":{"clusters":[{"name":"xds","type":"LOGICAL_DNS","connect_timeout":"1s","http2_protocol_options":{},"load_assignment":{"cluster_name":"xds","endpoints":[{"lb_endpoints":[{"endpoint":{"address":{"socket_address":{"address":"host.docker.internal","port_value":%d}}}}]}]}}],
 "listeners":[{"name":"upstream","address":{"socket_address":{"address":"127.0.0.1","port_value":10001}},"filter_chains":[{"filters":[{"name":"envoy.filters.network.http_connection_manager","typed_config":{"@type":"type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager","stat_prefix":"upstream","route_config":{"virtual_hosts":[{"name":"all","domains":["*"],"routes":[{"match":{"prefix":"/"},"direct_response":{"status":200,"body":{"inline_string":"probe-upstream"}}}]}]},"http_filters":[{"name":"envoy.filters.http.router","typed_config":{"@type":"type.googleapis.com/envoy.extensions.filters.http.router.v3.Router"}}]}}]}]}]}}
`, port)
	cfg := filepath.Join(dir, "bootstrap.json")
	if err = os.WriteFile(cfg, []byte(bootstrap), 0o600); err != nil {
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
	runArgs = append(runArgs, "-p", "127.0.0.1::9901", "-p", "127.0.0.1::10000", "-p", "127.0.0.1::10002", "-p", "127.0.0.1::10004", "-p", "127.0.0.1::10006", "-v", cfg+":/probe.json:ro", *image, "-c", "/probe.json", "--concurrency", "1", "--disable-hot-restart", "--log-level", "info")
	id, err := command(runArgs...)
	if err != nil {
		return err
	}
	defer func() {
		logs, logErr := command("logs", id)
		writeErr := os.WriteFile(filepath.Join(dir, "envoy.log"), []byte(logs), 0o600)
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
	inline, err := published("10002/tcp")
	if err != nil {
		return err
	}
	tlsPublished, err := published("10004/tcp")
	if err != nil {
		return err
	}
	orphanPublished, err := published("10006/tcp")
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
		if e = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); e != nil {
			panic(e)
		}
		return body
	}
	save("server-info.json", "/server_info")
	if *scenario == "references" {
		return runReferences(ctx, dir, admin, front, inline, p, advance, *useCache)
	}
	if *scenario == "rejection" {
		return runRejection(ctx, dir, admin, front, inline, p, advance, *useCache)
	}
	if *scenario == "secrets" {
		return runSecrets(ctx, dir, admin, front, strings.TrimPrefix(tlsPublished, "http://"), strings.TrimPrefix(orphanPublished, "http://"), p, advance, p.secrets)
	}
	if *scenario == "restart" {
		return runRestart(ctx, dir, admin, front, p, restart)
	}
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
		return fmt.Errorf("missing EDS ready status=%d err=%w", code, err)
	}
	save("missing-config.json", "/config_dump")
	p.record(map[string]any{"event": "observation", "phase": "missing", "ready": code})
	if err = advance(1); err != nil {
		return err
	}
	if err = await(ctx, func() bool { code, _, _ := get(admin + "/ready"); return code == 200 }); err != nil {
		return fmt.Errorf("empty EDS did not initialize: %w", err)
	}
	if err = await(ctx, func() bool { code, _, _ := get(front + "/"); return code == 503 }); err != nil {
		return fmt.Errorf("empty EDS traffic was not degraded: %w", err)
	}
	save("empty-config.json", "/config_dump")
	save("empty-clusters.json", "/clusters?format=json")
	p.record(map[string]any{"event": "observation", "phase": "empty", "ready": 200, "traffic": 503})
	if err = advance(2); err != nil {
		return err
	}
	if err = await(ctx, func() bool { code, b, _ := get(front + "/"); return code == 200 && b == "probe-upstream" }); err != nil {
		return fmt.Errorf("ready EDS traffic did not recover: %w", err)
	}
	save("ready-config.json", "/config_dump")
	save("ready-clusters.json", "/clusters?format=json")
	p.record(map[string]any{"event": "observation", "phase": "ready", "ready": 200, "traffic": 200})
	if err = advance(3); err != nil {
		return err
	} // changed CDS, unchanged EDS deliberately withheld
	if err = await(ctx, func() bool { _, b, _ := get(admin + "/config_dump"); return hasCluster(b, true) }); err != nil {
		return fmt.Errorf("changed CDS did not rewarm: %w", err)
	}
	code, body, err := get(front + "/")
	if err != nil || code != 200 || body != "probe-upstream" {
		return fmt.Errorf("rewarming did not preserve old traffic: status=%d err=%w", code, err)
	}
	save("rewarming-config.json", "/config_dump")
	p.record(map[string]any{"event": "observation", "phase": "rewarming-without-eds-replay", "traffic": 200})
	if err = advance(4); err != nil {
		return err
	}
	if *useCache {
		// Finite stable-window characterization, not a proof of infinite silence.
		// Source eligibility plus absence of a future revision is a separate model obligation.
		for range 8 {
			_, b, e := get(admin + "/config_dump")
			if e != nil || !hasCluster(b, true) {
				return fmt.Errorf("unchanged cache republish unexpectedly completed rewarming: %w", e)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
		save("cache-republish-still-warming.json", "/config_dump")
		p.record(map[string]any{"event": "observation", "phase": "unchanged-cache-republish", "warming": true, "stable_window_ms": 400})
		// A changed EDS version is an explicit recovery event, not a cache fix.
		if err = advance(8); err != nil {
			return err
		}
	}

	if err = await(ctx, func() bool {
		_, b, _ := get(admin + "/config_dump")
		return !hasCluster(b, true) && hasCluster(b, false)
	}); err != nil {
		return fmt.Errorf("EDS recovery did not finish rewarming: %w", err)
	}
	save("rewarmed-config.json", "/config_dump")
	p.record(map[string]any{"event": "observation", "phase": "eds-recovery", "snapshot_cache": *useCache, "warming": false})
	for _, phase := range []int{5, 6, 7} {
		if err = advance(phase); err != nil {
			return err
		}
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
			return fmt.Errorf("endpoint loss changed process readiness: status=%d err=%w", code, err)
		}
		clusterDump := save(fmt.Sprintf("phase-%d-clusters.json", phase), "/clusters?format=json")
		if phase == 5 && !strings.Contains(clusterDump, `"eds_health_status": "UNHEALTHY"`) {
			return errors.New("unhealthy phase did not install unhealthy host")
		}
		p.record(map[string]any{"event": "observation", "phase": phase, "ready": 200, "traffic": want})
	}
	fmt.Printf("PASS Envoy characterization: cache=%t ordered=%t panic-disabled=%t\n", *useCache, *ordered, *disablePanic)

	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
