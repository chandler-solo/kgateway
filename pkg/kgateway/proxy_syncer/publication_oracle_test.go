//go:build formal_oracle

package proxy_syncer

// The Lean process receives only independently generated inputs and schedules.
// This adapter invokes production Go helpers; it contains no expected selection
// algorithm. Full protobuf comparison also checks that inputs remain immutable.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	cluster "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpoint "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listener "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	route "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	aggregate "github.com/envoyproxy/go-control-plane/envoy/extensions/clusters/aggregate/v3"
	jwt "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/jwt_authn/v3"
	hcm "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	tls "github.com/envoyproxy/go-control-plane/envoy/extensions/transport_sockets/tls/v3"
	ct "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/proxy_syncer/sharedproto"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

type oracleCluster struct {
	Name       string   `json:"name"`
	EDS        bool     `json:"eds"`
	Assignment string   `json:"assignment"`
	Payload    uint64   `json:"payload"`
	Hash       string   `json:"hash"`
	Deps       []string `json:"deps"`
}
type oracleEndpoint struct {
	Name      string `json:"name"`
	Payload   uint64 `json:"payload"`
	Unhealthy bool   `json:"unhealthy"`
	Hash      string `json:"hash"`
}
type oracleConfig struct {
	Refs            []string `json:"refs"`
	Ancillary       []string `json:"ancillary"`
	Opaque          bool     `json:"opaqueConfig"`
	Dynamic         bool     `json:"dynamic"`
	Weighted        bool     `json:"weighted"`
	RouteVersion    string   `json:"routeVersion"`
	ListenerVersion string   `json:"listenerVersion"`
	SecretVersion   string   `json:"secretVersion"`
}
type oracleInput struct {
	Gateway   bool             `json:"gateway"`
	Clusters  []oracleCluster  `json:"clusters"`
	Endpoints []oracleEndpoint `json:"endpoints"`
	Bootstrap []string         `json:"bootstrap"`
	Errored   []string         `json:"errored"`
	Config    oracleConfig     `json:"config"`
}
type oracleSnapshot struct {
	Clusters   []oracleCluster  `json:"clusters"`
	Endpoints  []oracleEndpoint `json:"endpoints"`
	Config     oracleConfig     `json:"config"`
	CDSVersion string           `json:"cdsVersion"`
	EDSVersion string           `json:"edsVersion"`
}
type oracleCandidate struct {
	Snapshot         oracleSnapshot `json:"snapshot"`
	MissingClusters  []string       `json:"missingClusters"`
	MissingEndpoints []string       `json:"missingEndpoints"`
	Errored          []string       `json:"errored"`
}
type oracleEvent struct {
	Kind   string       `json:"kind"`
	Client string       `json:"client"`
	ID     int          `json:"id"`
	Other  int          `json:"other"`
	Input  *oracleInput `json:"input"`
}
type oracleRequest struct {
	Schema  int           `json:"schema"`
	Profile string        `json:"profile"`
	Events  []oracleEvent `json:"events"`
}
type oracleObservation struct {
	Candidate *oracleCandidate `json:"candidate"`
	Installed *oracleSnapshot  `json:"installed"`
	Wrote     bool             `json:"wrote"`
	Equal     *bool            `json:"equal"`
}
type oracleResponse struct {
	OK           bool                `json:"ok"`
	Error        string              `json:"error"`
	Observations []oracleObservation `json:"observations"`
}

type publicationOracle struct {
	mu     sync.Mutex
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr bytes.Buffer
	cancel context.CancelFunc
}

func startPublicationOracle(t testing.TB) *publicationOracle {
	t.Helper()
	path := os.Getenv("XDS_ORACLE_BIN")
	if path == "" {
		t.Fatal("XDS_ORACLE_BIN is required; run make formal-oracle")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &publicationOracle{cmd: exec.CommandContext(ctx, abs), cancel: cancel}
	o.cmd.Stderr = &o.stderr
	o.in, err = o.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	stdout, err := o.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	o.out = bufio.NewReaderSize(stdout, 65536)
	if err := o.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = o.in.Close()
		// EOF normally exits immediately. Bound cleanup if the executable hangs.
		timer := time.AfterFunc(3*time.Second, cancel)
		err := o.cmd.Wait()
		timer.Stop()
		cancel()
		if err != nil {
			t.Errorf("oracle process: %v: %s", err, o.stderr.String())
		}
	})
	return o
}

func (o *publicationOracle) predict(r oracleRequest) ([]oracleObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	// A hung/crashed oracle is a harness failure, never an empty passing trace.
	timer := time.AfterFunc(10*time.Second, o.cancel)
	defer timer.Stop()
	if _, err = o.in.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := o.out.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("oracle response: %w", err)
	}
	var response oracleResponse
	d := json.NewDecoder(bytes.NewReader(line))
	d.DisallowUnknownFields()
	if err := d.Decode(&response); err != nil {
		return nil, err
	}
	if !response.OK {
		return nil, fmt.Errorf("oracle rejected history: %s", response.Error)
	}
	if len(response.Observations) != len(r.Events) {
		return nil, errors.New("oracle observation count mismatch")
	}
	return response.Observations, nil
}

func oracleAny(m proto.Message) *anypb.Any {
	a := &anypb.Any{}
	err := anypb.MarshalFrom(a, m, proto.MarshalOptions{Deterministic: true})
	if err != nil {
		panic(err)
	}
	return a
}

func oracleClusterProto(c oracleCluster) *cluster.Cluster {
	p := &cluster.Cluster{Name: c.Name, AltStatName: fmt.Sprintf("payload-%d", c.Payload)}
	switch {
	case c.EDS:
		p.ClusterDiscoveryType = &cluster.Cluster_Type{Type: cluster.Cluster_EDS}
		p.EdsClusterConfig = &cluster.Cluster_EdsClusterConfig{ServiceName: c.Assignment}
	case len(c.Deps) != 0:
		p.ClusterDiscoveryType = &cluster.Cluster_ClusterType{ClusterType: &cluster.Cluster_CustomClusterType{
			Name: "envoy.clusters.aggregate", TypedConfig: oracleAny(&aggregate.ClusterConfig{Clusters: c.Deps}),
		}}
	default:
		p.ClusterDiscoveryType = &cluster.Cluster_Type{Type: cluster.Cluster_STATIC}
	}
	return p
}

func oracleEndpointProto(e oracleEndpoint) *endpoint.ClusterLoadAssignment {
	p := &endpoint.ClusterLoadAssignment{ClusterName: e.Name}
	if e.Payload != 0 {
		health := core.HealthStatus_HEALTHY
		if e.Unhealthy {
			health = core.HealthStatus_UNHEALTHY
		}
		p.Endpoints = []*endpoint.LocalityLbEndpoints{{LbEndpoints: []*endpoint.LbEndpoint{{
			HealthStatus: health, HostIdentifier: &endpoint.LbEndpoint_Endpoint{Endpoint: &endpoint.Endpoint{
				Address: &core.Address{Address: &core.Address_SocketAddress{SocketAddress: &core.SocketAddress{
					Address: "127.0.0.1", PortSpecifier: &core.SocketAddress_PortValue{PortValue: uint32(10000 + e.Payload)},
				}}},
			}},
		}}}}
	}
	return p
}

func oracleConfigResources(c oracleConfig) (cache.Resources, cache.Resources, cache.Resources) {
	routes := []*route.Route{}
	if c.Weighted && len(c.Refs) > 0 {
		weights := []*route.WeightedCluster_ClusterWeight{}
		for _, n := range c.Refs {
			weights = append(weights, &route.WeightedCluster_ClusterWeight{Name: n, Weight: wrapperspb.UInt32(1)})
		}
		routes = append(routes, &route.Route{Action: &route.Route_Route{Route: &route.RouteAction{
			ClusterSpecifier: &route.RouteAction_WeightedClusters{WeightedClusters: &route.WeightedCluster{Clusters: weights}},
		}}})
	} else {
		for _, n := range c.Refs {
			routes = append(routes, &route.Route{Action: &route.Route_Route{Route: &route.RouteAction{
				ClusterSpecifier: &route.RouteAction_Cluster{Cluster: n},
			}}})
		}
	}
	if c.Dynamic {
		routes = append(routes, &route.Route{Action: &route.Route_Route{Route: &route.RouteAction{
			ClusterSpecifier: &route.RouteAction_ClusterHeader{ClusterHeader: "x-oracle-cluster"},
		}}})
	}
	r := &route.RouteConfiguration{Name: "oracle-routes", VirtualHosts: []*route.VirtualHost{{Name: "route-" + c.RouteVersion, Routes: routes}}}
	providers := map[string]*jwt.JwtProvider{}
	for k, n := range c.Ancillary {
		providers[strconv.Itoa(k)] = &jwt.JwtProvider{
			JwksSourceSpecifier: &jwt.JwtProvider_RemoteJwks{RemoteJwks: &jwt.RemoteJwks{HttpUri: &core.HttpUri{
				HttpUpstreamType: &core.HttpUri_Cluster{Cluster: n},
			}}},
		}
	}
	manager := &hcm.HttpConnectionManager{
		StatPrefix:     "listener-" + c.ListenerVersion,
		RouteSpecifier: &hcm.HttpConnectionManager_Rds{Rds: &hcm.Rds{RouteConfigName: "oracle-routes"}},
		HttpFilters: []*hcm.HttpFilter{{Name: "envoy.filters.http.jwt_authn", ConfigType: &hcm.HttpFilter_TypedConfig{
			TypedConfig: oracleAny(&jwt.JwtAuthentication{Providers: providers}),
		}}},
	}
	filters := []*listener.Filter{{Name: "envoy.filters.network.http_connection_manager", ConfigType: &listener.Filter_TypedConfig{TypedConfig: oracleAny(manager)}}}
	if c.Opaque {
		filters = append(filters, &listener.Filter{Name: "oracle-opaque", ConfigType: &listener.Filter_TypedConfig{
			TypedConfig: &anypb.Any{TypeUrl: "type.googleapis.com/oracle.Unregistered"},
		}})
	}
	l := &listener.Listener{Name: "oracle-listener", FilterChains: []*listener.FilterChain{{Filters: filters}}}
	s := &tls.Secret{Name: "oracle-secret", Type: &tls.Secret_GenericSecret{GenericSecret: &tls.GenericSecret{
		Secret: &core.DataSource{Specifier: &core.DataSource_InlineString{InlineString: "public-fixture-" + c.SecretVersion}},
	}}}
	return cache.NewResources(c.RouteVersion, []ct.Resource{r}), cache.NewResources(c.ListenerVersion, []ct.Resource{l}), cache.NewResources(c.SecretVersion, []ct.Resource{s})
}

func oracleUCC(client string) ir.UniquelyConnectedClient {
	return ir.NewUniquelyConnectedClient("kgateway-kube-gateway-api~oracle~"+client, "", nil, ir.PodLocality{})
}

// Fixture input conversion and production helper pipeline. No output from
// these helpers is passed to Lean as an expectation.
func oracleBuildGo(i oracleInput, client string) *XdsSnapWrapper {
	if !i.Gateway {
		return nil
	}
	ucc := oracleUCC(client)
	rows := []uccWithCluster{}
	for _, c := range i.Clusters {
		h, _ := strconv.ParseUint(c.Hash, 10, 64)
		rows = append(rows, uccWithCluster{Client: ucc, Name: c.Name, Cluster: sharedproto.Wrap(oracleClusterProto(c)), ClusterVersion: h})
	}
	for _, n := range i.Errored {
		rows = append(rows, uccWithCluster{Client: ucc, Name: n, Error: errors.New("fixture translation error")})
	}
	cs := assemblePerClientClusters(ucc, rows)
	eps := []ct.Resource{}
	hashes := map[string]uint64{}
	var fold uint64
	for _, e := range i.Endpoints {
		eps = append(eps, oracleEndpointProto(e))
		h, _ := strconv.ParseUint(e.Hash, 10, 64)
		hashes[e.Name] = h
		fold ^= h
	}
	r, l, s := oracleConfigResources(i.Config)
	refs := collectReferencedClusters(r, l)
	raw := cache.NewResources(strconv.FormatUint(fold, 10), eps)
	filtered, contributions := filterEndpointResourcesForClusters(cs.clusters, raw, hashes, stringSet(i.Bootstrap))
	missing := findMissingReferencedClusters(refs, cs.clusters.Items, cs.erroredClusters)
	missingEP := findMissingReferencedEndpointResources(refs, cs.clusters.Items, raw.Items, cs.erroredClusters)
	snap := &cache.Snapshot{}
	snap.Resources[ct.Cluster], snap.Resources[ct.Endpoint] = cs.clusters, filtered
	snap.Resources[ct.Route], snap.Resources[ct.Listener], snap.Resources[ct.Secret] = r, l, s
	return &XdsSnapWrapper{snap: snap, proxyKey: ucc.ResourceName(), deferred: len(missing)+len(missingEP) > 0,
		missingReferenced: missing, missingEndpointsReferenced: missingEP, erroredClusters: cs.erroredClusters, endpointHashes: contributions}
}

func oracleExpectedSnapshot(s oracleSnapshot) *cache.Snapshot {
	p := &cache.Snapshot{}
	cs, es := []ct.Resource{}, []ct.Resource{}
	for _, c := range s.Clusters {
		cs = append(cs, oracleClusterProto(c))
	}
	for _, e := range s.Endpoints {
		es = append(es, oracleEndpointProto(e))
	}
	p.Resources[ct.Cluster] = cache.NewResources(s.CDSVersion, cs)
	p.Resources[ct.Endpoint] = cache.NewResources(s.EDSVersion, es)
	p.Resources[ct.Route], p.Resources[ct.Listener], p.Resources[ct.Secret] = oracleConfigResources(s.Config)
	return p
}

func oracleCompareSnapshot(got *cache.Snapshot, want *oracleSnapshot) error {
	if want == nil {
		if got != nil {
			return errors.New("unexpected cache installation")
		}
		return nil
	}
	if got == nil {
		return errors.New("missing cache installation")
	}
	expected := oracleExpectedSnapshot(*want)
	for _, typ := range []ct.ResponseType{ct.Cluster, ct.Endpoint, ct.Route, ct.Listener, ct.Secret} {
		g, w := got.Resources[typ], expected.Resources[typ]
		if g.Version != w.Version {
			return fmt.Errorf("type %d version: got %q want %q", typ, g.Version, w.Version)
		}
		if len(g.Items) != len(w.Items) {
			return fmt.Errorf("type %d resources: got %v want %v", typ, mapKeys(g.Items), mapKeys(w.Items))
		}
		for n, item := range w.Items {
			actual, ok := g.Items[n]
			if !ok || actual.TTL != item.TTL || !proto.Equal(actual.Resource, item.Resource) {
				return fmt.Errorf("type %d resource %q: got %v want %v", typ, n, actual.Resource, item.Resource)
			}
		}
	}
	return nil
}

func oracleCompareCandidate(got *XdsSnapWrapper, want *oracleCandidate) error {
	if want == nil {
		if got != nil {
			return errors.New("unexpected candidate")
		}
		return nil
	}
	if got == nil {
		return errors.New("missing candidate")
	}
	for _, pair := range [][2][]string{{got.missingReferenced, want.MissingClusters}, {got.missingEndpointsReferenced, want.MissingEndpoints}, {got.erroredClusters, want.Errored}} {
		a, b := slices.Clone(pair[0]), slices.Clone(pair[1])
		slices.Sort(a)
		slices.Sort(b)
		if !slices.Equal(a, b) {
			return fmt.Errorf("candidate metadata: got %v want %v", a, b)
		}
	}
	if got.deferred != (len(want.MissingClusters)+len(want.MissingEndpoints) > 0) {
		return errors.New("wrong deferral decision")
	}
	if err := oracleCompareSnapshot(got.snap, &want.Snapshot); err != nil {
		return err
	}
	if len(got.endpointHashes) != len(want.Snapshot.Endpoints) {
		return errors.New("contribution count mismatch")
	}
	for _, e := range want.Snapshot.Endpoints {
		v, ok := got.endpointHashes[e.Name]
		if !ok || strconv.FormatUint(v, 10) != e.Hash {
			return fmt.Errorf("contribution mismatch for %s", e.Name)
		}
	}
	return nil
}

// Independent property check: differential agreement with the XOR algorithm
// does not establish its collision freedom. Deliberate collisions are checked
// separately as required negative controls, not erased by the reference model.
func oracleVersionRelation(previous, current *cache.Snapshot) error {
	if previous == nil || current == nil {
		return nil
	}
	a, b := previous.Resources[ct.Endpoint], current.Resources[ct.Endpoint]
	if a.Version != b.Version {
		return nil // Input-only changes and rewarming can legitimately move EDS.
	}
	if len(a.Items) != len(b.Items) {
		return fmt.Errorf("version-reuse: EDS version %q has different resource sets", a.Version)
	}
	for name, item := range a.Items {
		other, ok := b.Items[name]
		if !ok || !proto.Equal(item.Resource, other.Resource) {
			return fmt.Errorf("version-reuse: EDS version %q changed resource %q", a.Version, name)
		}
	}
	return nil
}

type oracleRecordingCache struct {
	cache.SnapshotCache
	writes    int
	lastError error
}

func (c *oracleRecordingCache) SetSnapshot(ctx context.Context, node string, s cache.ResourceSnapshot) error {
	c.writes++
	c.lastError = c.SnapshotCache.SetSnapshot(ctx, node, s)
	return c.lastError
}

func oracleCheckHistory(r oracleRequest, expected []oracleObservation) error {
	c := &oracleRecordingCache{SnapshotCache: cache.NewSnapshotCache(true, cache.IDHash{}, nil)}
	publisher := NewProxyTranslator(c)
	builds := map[string]map[int]*XdsSnapWrapper{}
	installed := map[string]*oracleSnapshot{}
	seenVersions := map[string]map[string]*cache.Snapshot{}
	for k, e := range r.Events {
		want := expected[k]
		if builds[e.Client] == nil {
			builds[e.Client] = map[int]*XdsSnapWrapper{}
		}
		before := c.writes
		var err error
		switch e.Kind {
		case "build":
			got := oracleBuildGo(*e.Input, e.Client)
			builds[e.Client][e.ID] = got
			err = oracleCompareCandidate(got, want.Candidate)
		case "publish":
			w := builds[e.Client][e.ID]
			if w != nil {
				publisher.syncXds(context.Background(), *w)
			}
			err = oracleCompareCandidate(w, want.Candidate) // catches input mutation
		case "delete": // the separately tested production Delete callback is a no-op
		case "equal":
			a, b := builds[e.Client][e.ID], builds[e.Client][e.Other]
			equal := a == nil && b == nil
			if a != nil && b != nil {
				equal = a.Equals(*b)
			}
			if want.Equal == nil || equal != *want.Equal {
				err = errors.New("wrapper equality mismatch")
			}
		default:
			return fmt.Errorf("unknown driver event %s", e.Kind)
		}
		if err != nil {
			return fmt.Errorf("event %d %s client %s id %d: %w", k, e.Kind, e.Client, e.ID, err)
		}
		writes := 0
		if want.Wrote {
			writes = 1
		}
		if c.writes-before != writes {
			return fmt.Errorf("event %d: installation decision mismatch", k)
		}
		installed[e.Client] = want.Installed
		// Observe every key after every event, including quiet sibling clients.
		for client, expected := range installed {
			var actual *cache.Snapshot
			if s, err := c.GetSnapshot(oracleUCC(client).ResourceName()); err == nil {
				actual = s.(*cache.Snapshot)
			}
			if err := oracleCompareSnapshot(actual, expected); err != nil {
				return fmt.Errorf("event %d installed client %s: %w", k, client, err)
			}
			if actual != nil {
				if seenVersions[client] == nil {
					seenVersions[client] = map[string]*cache.Snapshot{}
				}
				version := actual.Resources[ct.Endpoint].Version
				if err := oracleVersionRelation(seenVersions[client][version], actual); err != nil {
					return fmt.Errorf("event %d client %s: %w", k, client, err)
				}
				seenVersions[client][version] = actual
			}
		}
		var actual *cache.Snapshot
		if s, err := c.GetSnapshot(oracleUCC(e.Client).ResourceName()); err == nil {
			actual = s.(*cache.Snapshot)
		}
		if err := oracleCompareSnapshot(actual, want.Installed); err != nil {
			return fmt.Errorf("event %d installed: %w", k, err)
		}
		if want.Installed != nil {
			lookup := publisher.published.lookup(oracleUCC(e.Client).ResourceName())
			for _, ep := range want.Installed.Endpoints {
				if lookup == nil {
					return fmt.Errorf("event %d missing remembered contributions", k)
				}
				h, ok := lookup(ep.Name)
				if !ok || strconv.FormatUint(h, 10) != ep.Hash {
					return fmt.Errorf("event %d wrong remembered contribution for %s", k, ep.Name)
				}
			}
		}
	}
	return nil
}

func checkPublicationOracle(t testing.TB, o *publicationOracle, r oracleRequest) {
	t.Helper()
	want, err := o.predict(r)
	if err == nil {
		err = oracleCheckHistory(r, want)
	}
	if err != nil {
		b, _ := json.Marshal(r)
		t.Fatalf("differential mismatch: %v\nhistory=%s", err, b)
	}
}
