//go:build formal_oracle

package proxy_syncer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"testing"
	"time"

	core "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	discovery "github.com/envoyproxy/go-control-plane/envoy/service/discovery/v3"
	ct "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	stream "github.com/envoyproxy/go-control-plane/pkg/server/stream/v3"
	"google.golang.org/protobuf/proto"
)

type oracleCacheEvent struct {
	Kind        string       `json:"kind"`
	Client      string       `json:"client"`
	Stream      int          `json:"stream"`
	Type        string       `json:"typ"`
	Names       []string     `json:"names"`
	Version     string       `json:"version"`
	Publication *oracleEvent `json:"publication"`
}
type oracleCacheRequest struct {
	Schema  int                `json:"schema"`
	Profile string             `json:"profile"`
	Events  []oracleCacheEvent `json:"events"`
}
type oracleCacheVersion struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type oracleCacheResponse struct {
	Stream   int                  `json:"stream"`
	Type     string               `json:"typ"`
	Version  string               `json:"version"`
	Names    []string             `json:"names"`
	Returned []oracleCacheVersion `json:"returned"`
	Snapshot oracleSnapshot       `json:"snapshot"`
}
type oracleCacheChannel struct {
	Stream         int                  `json:"stream"`
	Client         string               `json:"client"`
	Type           string               `json:"typ"`
	Names          []string             `json:"names"`
	Wildcard       bool                 `json:"wildcard"`
	Legacy         bool                 `json:"legacy"`
	Returned       []oracleCacheVersion `json:"returned"`
	RequestVersion string               `json:"requestVersion"`
	Watch          bool                 `json:"watch"`
}
type oracleCacheInstalled struct {
	Client   string         `json:"client"`
	Snapshot oracleSnapshot `json:"snapshot"`
}
type oracleCacheObservation struct {
	Publication *oracleObservation     `json:"publication"`
	Responses   []oracleCacheResponse  `json:"responses"`
	Channels    []oracleCacheChannel   `json:"channels"`
	Installed   []oracleCacheInstalled `json:"installed"`
	Failed      bool                   `json:"failed"`
}

func startCacheOracle(t testing.TB) *publicationOracle {
	t.Helper()
	path := os.Getenv("XDS_CACHE_ORACLE_BIN")
	if path == "" {
		t.Fatal("XDS_CACHE_ORACLE_BIN is required; run make formal-oracle")
	}
	ctx, cancel := context.WithCancel(context.Background())
	o := &publicationOracle{cmd: exec.CommandContext(ctx, path), cancel: cancel}
	o.cmd.Stderr = &o.stderr
	var err error
	o.in, err = o.cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	out, err := o.cmd.StdoutPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	o.out = bufio.NewReaderSize(out, 65536)
	if err := o.cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = o.in.Close()
		timer := time.AfterFunc(3*time.Second, cancel)
		err := o.cmd.Wait()
		timer.Stop()
		cancel()
		if err != nil {
			t.Errorf("cache oracle: %v: %s", err, o.stderr.String())
		}
	})
	return o
}

func predictCache(o *publicationOracle, r oracleCacheRequest) ([]oracleCacheObservation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	timer := time.AfterFunc(10*time.Second, o.cancel)
	defer timer.Stop()
	if _, err = o.in.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := o.out.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var answer struct {
		OK           bool                     `json:"ok"`
		Error        string                   `json:"error"`
		Observations []oracleCacheObservation `json:"observations"`
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&answer); err != nil {
		return nil, err
	}
	if !answer.OK {
		return nil, fmt.Errorf("cache oracle rejected input: %s", answer.Error)
	}
	if len(answer.Observations) != len(r.Events) {
		return nil, errors.New("cache oracle observation count mismatch")
	}
	return answer.Observations, nil
}

var oracleTypes = map[string]struct {
	url   string
	index ct.ResponseType
}{
	"cds": {resource.ClusterType, ct.Cluster}, "eds": {resource.EndpointType, ct.Endpoint},
	"lds": {resource.ListenerType, ct.Listener}, "rds": {resource.RouteType, ct.Route}, "sds": {resource.SecretType, ct.Secret},
}

type oracleChannelKey struct {
	stream int
	typ    string
}
type oracleActualChannel struct {
	client    string
	sub       stream.Subscription
	request   *discovery.DiscoveryRequest
	cancel    func()
	responses chan cache.Response
}
type oracleActualResponse struct {
	key      oracleChannelKey
	response cache.Response
}

func oracleReturnedEqual(actual map[string]string, want []oracleCacheVersion) bool {
	if len(actual) != len(want) {
		return false
	}
	for _, v := range want {
		if actual[v.Name] != v.Version {
			return false
		}
	}
	return true
}

func checkCacheHistory(r oracleCacheRequest, wants []oracleCacheObservation) error {
	c := &oracleRecordingCache{SnapshotCache: cache.NewSnapshotCache(true, cache.IDHash{}, nil)}
	publisher := NewProxyTranslator(c)
	channels := map[oracleChannelKey]*oracleActualChannel{}
	defer func() {
		for _, ch := range channels {
			ch.cancel()
		}
	}()
	streams := map[int]string{}
	builds := map[string]map[int]*XdsSnapWrapper{}
	held := map[string]string{}
	clients := map[string]bool{}
	for index, e := range r.Events {
		want := wants[index]
		clients[e.Client] = true
		actualResponses := []oracleActualResponse{}
		var failed bool
		var candidate *XdsSnapWrapper
		before := c.writes
		c.lastError = nil
		switch e.Kind {
		case "open":
			streams[e.Stream] = e.Client
		case "close":
			delete(streams, e.Stream)
			for key, ch := range channels {
				if key.stream == e.Stream {
					ch.cancel()
					delete(channels, key)
				}
			}
		case "restart":
			for _, ch := range channels {
				ch.cancel()
			}
			channels = map[oracleChannelKey]*oracleActualChannel{}
			streams = map[int]string{}
			c = &oracleRecordingCache{SnapshotCache: cache.NewSnapshotCache(true, cache.IDHash{}, nil)}
			publisher = NewProxyTranslator(c)
		case "cancel", "request", "blocked-request":
			key := oracleChannelKey{e.Stream, e.Type}
			ch := channels[key]
			if ch == nil {
				ch = &oracleActualChannel{client: streams[e.Stream], sub: stream.NewSotwSubscription(nil, true), cancel: func() {}}
				channels[key] = ch
			}
			ch.cancel()
			if e.Kind == "cancel" {
				break
			}
			ch.sub.SetResourceSubscription(e.Names)
			vs := e.Version
			if vs == "held" {
				vs = held[e.Client+"/"+e.Type]
			}
			ch.request = &discovery.DiscoveryRequest{Node: &core.Node{Id: oracleUCC(e.Client).ResourceName()}, TypeUrl: oracleTypes[e.Type].url, ResourceNames: e.Names, VersionInfo: vs}
			capacity := 1
			if e.Kind == "blocked-request" {
				capacity = 0
			}
			ch.responses = make(chan cache.Response, capacity)
			var err error
			ch.cancel, err = c.CreateWatch(ch.request, ch.sub, ch.responses)
			if err != nil {
				return err
			}
		case "publication", "publication-cancel":
			p := *e.Publication
			if builds[e.Client] == nil {
				builds[e.Client] = map[int]*XdsSnapWrapper{}
			}
			switch p.Kind {
			case "build":
				candidate = oracleBuildGo(*p.Input, e.Client)
				builds[e.Client][p.ID] = candidate
			case "publish":
				candidate = builds[e.Client][p.ID]
				if candidate != nil {
					if e.Kind == "publication-cancel" {
						// Deterministic partial fan-out: receive CDS, then cancel
						// while EDS has no receiver. Cache installation still stands.
						ctx, cancel := context.WithCancel(context.Background())
						done := make(chan struct{})
						go func() { publisher.syncXds(ctx, *candidate); close(done) }()
						var cds *oracleActualChannel
						var cdsKey oracleChannelKey
						for key, ch := range channels {
							if ch.client == e.Client && key.typ == "cds" {
								cds = ch
								cdsKey = key
							}
						}
						if cds == nil {
							cancel()
							return errors.New("partial cancellation needs CDS watch")
						}
						select {
						case response := <-cds.responses:
							actualResponses = append(actualResponses, oracleActualResponse{cdsKey, response})
						case <-time.After(time.Second):
							cancel()
							return errors.New("partial cancellation CDS response missing")
						}
						cancel()
						select {
						case <-done:
						case <-time.After(time.Second):
							return errors.New("canceled publication did not finish")
						}
						failed = errors.Is(c.lastError, context.Canceled)
						if c.lastError != nil && !failed {
							return fmt.Errorf("unexpected publication error: %w", c.lastError)
						}
					} else {
						// Unbuffered channels are used only in the cancellation
						// fixture; drain them on a later retry without re-requesting.
						ctx, cancel := context.WithCancel(context.Background())
						var receivers []chan *oracleActualResponse
						for key, ch := range channels {
							if ch.responses != nil && cap(ch.responses) == 0 {
								out := make(chan *oracleActualResponse, 1)
								receivers = append(receivers, out)
								go func() {
									select {
									case response := <-ch.responses:
										out <- &oracleActualResponse{key, response}
									case <-ctx.Done():
										out <- nil
									}
								}()
							}
						}
						publisher.syncXds(ctx, *candidate)
						cancel()
						for _, out := range receivers {
							if got := <-out; got != nil {
								actualResponses = append(actualResponses, *got)
							}
						}
						if c.lastError != nil {
							return fmt.Errorf("unexpected publication error: %w", c.lastError)
						}
					}
				}
			case "delete":
			case "equal":
				return errors.New("equality belongs to publication profile")
			default:
				return errors.New("unknown publication input")
			}
			if want.Publication == nil {
				return errors.New("missing publication observation")
			}
			if err := oracleCompareCandidate(candidate, want.Publication.Candidate); err != nil {
				return fmt.Errorf("event %d candidate: %w", index, err)
			}
			count := 0
			if want.Publication.Wrote {
				count = 1
			}
			if c.writes-before != count {
				return errors.New("cache composition installation mismatch")
			}
		default:
			return fmt.Errorf("unknown cache action %s", e.Kind)
		}
		for key, ch := range channels {
			if ch.responses != nil {
				select {
				case response := <-ch.responses:
					actualResponses = append(actualResponses, oracleActualResponse{key, response})
				default:
				}
			}
		}
		if failed != want.Failed {
			return fmt.Errorf("event %d cancellation result mismatch", index)
		}
		if len(actualResponses) != len(want.Responses) {
			return fmt.Errorf("event %d %s response count: got %d want %d", index, e.Kind, len(actualResponses), len(want.Responses))
		}
		for _, actual := range actualResponses {
			wi := slices.IndexFunc(want.Responses, func(v oracleCacheResponse) bool { return v.Stream == actual.key.stream && v.Type == actual.key.typ })
			if wi < 0 {
				return fmt.Errorf("event %d unexpected response %v", index, actual.key)
			}
			w := want.Responses[wi]
			if actual.response.GetResponseVersion() != w.Version || !oracleReturnedEqual(actual.response.GetReturnedResources(), w.Returned) {
				return fmt.Errorf("event %d response version/returned resources mismatch", index)
			}
			wire, err := actual.response.GetDiscoveryResponse()
			if err != nil {
				return err
			}
			if wire.TypeUrl != oracleTypes[w.Type].url || len(wire.Resources) != len(w.Names) {
				return fmt.Errorf("event %d response payload count/type mismatch", index)
			}
			expected := oracleExpectedSnapshot(w.Snapshot).Resources[oracleTypes[w.Type].index]
			seen := map[string]bool{}
			for _, a := range wire.Resources {
				p, err := a.UnmarshalNew()
				if err != nil {
					return err
				}
				n := cache.GetResourceName(p)
				if seen[n] || !slices.Contains(w.Names, n) || !proto.Equal(p, expected.Items[n].Resource) {
					return fmt.Errorf("event %d wrong response resource %s", index, n)
				}
				seen[n] = true
			}
			ch := channels[actual.key]
			ch.sub.SetReturnedResources(actual.response.GetReturnedResources())
			held[ch.client+"/"+actual.key.typ] = actual.response.GetResponseVersion()
		}
		if len(channels) != len(want.Channels) {
			return fmt.Errorf("event %d channel count mismatch", index)
		}
		watchCounts := map[string]int{}
		for _, w := range want.Channels {
			ch := channels[oracleChannelKey{w.Stream, w.Type}]
			if ch == nil || ch.client != w.Client || ch.sub.IsWildcard() != w.Wildcard || !oracleReturnedEqual(ch.sub.ReturnedResources(), w.Returned) {
				return fmt.Errorf("event %d channel state mismatch", index)
			}
			if len(ch.sub.SubscribedResources()) != len(w.Names) {
				return fmt.Errorf("event %d subscription count mismatch", index)
			}
			for _, name := range w.Names {
				if _, ok := ch.sub.SubscribedResources()[name]; !ok {
					return fmt.Errorf("event %d subscription name mismatch", index)
				}
			}
			if ch.request.GetVersionInfo() != w.RequestVersion {
				return fmt.Errorf("event %d request version mismatch", index)
			}
			if w.Watch {
				watchCounts[w.Client]++
			}
		}
		for client := range clients {
			key := oracleUCC(client).ResourceName()
			n := 0
			if status := c.GetStatusInfo(key); status != nil {
				n = status.GetNumWatches()
			}
			if n != watchCounts[client] {
				return fmt.Errorf("event %d client %s watch count: got %d want %d", index, client, n, watchCounts[client])
			}
			wi := slices.IndexFunc(want.Installed, func(v oracleCacheInstalled) bool { return v.Client == client })
			var expected *oracleSnapshot
			if wi >= 0 {
				expected = &want.Installed[wi].Snapshot
			}
			var actual *cache.Snapshot
			if s, err := c.GetSnapshot(key); err == nil {
				actual = s.(*cache.Snapshot)
			}
			if err := oracleCompareSnapshot(actual, expected); err != nil {
				return fmt.Errorf("event %d cache client %s: %w", index, client, err)
			}
		}
	}
	return nil
}

func checkCacheOracle(t testing.TB, o *publicationOracle, r oracleCacheRequest) {
	t.Helper()
	want, err := predictCache(o, r)
	if err == nil {
		err = checkCacheHistory(r, want)
	}
	if err != nil {
		b, _ := json.Marshal(r)
		t.Fatalf("cache differential mismatch: %v\nhistory=%s", err, b)
	}
}

type oracleCacheHistory struct {
	r           oracleCacheRequest
	publication *oracleHistory
}

func newCacheHistory() *oracleCacheHistory {
	return &oracleCacheHistory{r: oracleCacheRequest{Schema: 1, Profile: "research-cache-1cd122661-v1", Events: []oracleCacheEvent{}}, publication: newOracleHistory()}
}
func (h *oracleCacheHistory) add(e oracleCacheEvent) {
	if e.Names == nil {
		e.Names = []string{}
	}
	h.r.Events = append(h.r.Events, e)
}
func (h *oracleCacheHistory) pub(client string, i oracleInput) int {
	id := h.publication.build(client, i)
	e := h.publication.request.Events[len(h.publication.request.Events)-1]
	h.add(oracleCacheEvent{Kind: "publication", Client: client, Publication: &e})
	h.send(client, id, false)
	return id
}
func (h *oracleCacheHistory) send(client string, id int, cancel bool) {
	kind := "publication"
	if cancel {
		kind = "publication-cancel"
	}
	h.add(oracleCacheEvent{Kind: kind, Client: client, Publication: &oracleEvent{Kind: "publish", Client: client, ID: id}})
}

func cacheOracleExamples() map[string]oracleCacheRequest {
	out := map[string]oracleCacheRequest{}
	for _, name := range []string{"immediate-decline", "parked-decline", "equal-version-resubscribe", "unsubscribe-all", "reconnect", "restart", "partial-cancel"} {
		h := newCacheHistory()
		base := oracleBase()
		base.Clusters = base.Clusters[:1]
		base.Endpoints = base.Endpoints[:1]
		h.add(oracleCacheEvent{Kind: "open", Client: "one", Stream: 1})
		if name == "partial-cancel" {
			for _, typ := range []string{"cds", "eds"} {
				h.add(oracleCacheEvent{Kind: "blocked-request", Client: "one", Stream: 1, Type: typ, Names: []string{"*"}})
			}
			id := h.publication.build("one", base)
			e := h.publication.request.Events[len(h.publication.request.Events)-1]
			h.add(oracleCacheEvent{Kind: "publication", Client: "one", Publication: &e})
			h.send("one", id, true)
			h.send("one", id, false)
		} else {
			h.pub("one", base)
			request := func(names []string, version string) {
				h.add(oracleCacheEvent{Kind: "request", Client: "one", Stream: 1, Type: "eds", Names: names, Version: version})
			}
			if name == "immediate-decline" {
				request([]string{"b"}, "")
			} else {
				request([]string{"a"}, "")
				request([]string{"a"}, "held")
			}
			switch name {
			case "immediate-decline":
				base.Endpoints[0].Payload = 2
				base.Endpoints[0].Hash = ""
				h.pub("one", base)
			case "parked-decline":
				h.pub("one", oracleBase())
				base.Endpoints[0].Payload = 2
				base.Endpoints[0].Hash = ""
				h.pub("one", base)
			case "equal-version-resubscribe":
				request([]string{}, "held")
				request([]string{"a"}, "held")
			case "unsubscribe-all":
				request([]string{}, "held")
				h.pub("one", oracleBase())
			case "reconnect", "restart":
				if name == "restart" {
					h.add(oracleCacheEvent{Kind: "restart", Client: "one"})
				} else {
					h.add(oracleCacheEvent{Kind: "close", Client: "one", Stream: 1})
				}
				h.add(oracleCacheEvent{Kind: "open", Client: "one", Stream: 2})
				h.add(oracleCacheEvent{Kind: "request", Client: "one", Stream: 2, Type: "eds", Names: []string{"a"}, Version: "held"})
				h.pub("one", base)
			}
		}
		out[name] = h.r
	}
	return out
}

func TestPublicationOracleCacheExamples(t *testing.T) {
	o := startCacheOracle(t)
	for n, r := range cacheOracleExamples() {
		t.Run(n, func(t *testing.T) { checkCacheOracle(t, o, r) })
	}
}

func decodeCacheHistory(data []byte) oracleCacheRequest {
	h := newCacheHistory()
	base := oracleDecode(data)
	generations := []int{1, 2}
	next := 2
	for client, id := range generations {
		h.add(oracleCacheEvent{Kind: "open", Client: strconv.Itoa(client), Stream: id})
	}
	for k, p := range base.Events {
		if p.Kind == "equal" {
			continue
		}
		h.add(oracleCacheEvent{Kind: "publication", Client: p.Client, Publication: &p})
		b := byte(0)
		if len(data) > 0 {
			b = data[k%len(data)]
		}
		client, _ := strconv.Atoi(p.Client)
		id := generations[client]
		if b&31 == 0 {
			h.add(oracleCacheEvent{Kind: "close", Client: p.Client, Stream: id})
			next++
			id = next
			generations[client] = id
			h.add(oracleCacheEvent{Kind: "open", Client: p.Client, Stream: id})
		}
		typ := []string{"cds", "eds", "lds", "rds", "sds"}[int(b)%5]
		names := []string{"*"}
		if b&8 != 0 {
			names = oracleMask(b)
		}
		if b&16 != 0 {
			names = []string{}
		}
		vs := ""
		if b&4 != 0 {
			vs = "held"
		}
		kind := "request"
		if b&63 == 3 {
			kind = "cancel"
		}
		h.add(oracleCacheEvent{Kind: kind, Client: p.Client, Stream: id, Type: typ, Names: names, Version: vs})
	}
	return h.r
}

func FuzzPublicationCacheOracle(f *testing.F) {
	o := startCacheOracle(f)
	for _, s := range [][]byte{{}, {7, 2, 0, 0, 5, 1, 0, 0}, {4, 0, 0, 0}, {1, 2, 4, 0, 6, 4, 0, 0}, {9, 0, 0, 0, 9, 1, 0, 0}, {8, 4, 3, 0, 7, 8, 1, 0}} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkCacheOracle(t, o, decodeCacheHistory(data)) })
}
