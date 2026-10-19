//go:build formal_oracle

package proxy_syncer

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math/rand"
	"os"
	"slices"
	"strconv"
	"testing"
)

// These are fixture row contributions, not a claim to reproduce translation's
// hash inputs. Lean independently implements name hashing and set versioning.
func oracleFixtureHash(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	h := fnv.New64a()
	_, _ = h.Write(b)
	return strconv.FormatUint(h.Sum64(), 10)
}

func oracleNormalize(i oracleInput) oracleInput {
	if i.Clusters == nil {
		i.Clusters = []oracleCluster{}
	}
	if i.Endpoints == nil {
		i.Endpoints = []oracleEndpoint{}
	}
	if i.Errored == nil {
		i.Errored = []string{}
	}
	if i.Bootstrap == nil {
		i.Bootstrap = []string{}
	}
	if i.Config.Refs == nil {
		i.Config.Refs = []string{}
	}
	if i.Config.Ancillary == nil {
		i.Config.Ancillary = []string{}
	}
	slices.Sort(i.Errored)
	for k := range i.Clusters {
		c := &i.Clusters[k]
		if c.Deps == nil {
			c.Deps = []string{}
		}
		if c.Hash == "" {
			c.Hash = oracleFixtureHash(*c)
		}
	}
	for k := range i.Endpoints {
		if i.Endpoints[k].Hash == "" {
			i.Endpoints[k].Hash = oracleFixtureHash(i.Endpoints[k])
		}
	}
	// Versions of the input R/L/S resources are part of the fixture boundary.
	// They change with the bytes encoded by oracleConfigResources.
	i.Config.RouteVersion = oracleFixtureHash([]any{i.Config.Refs, i.Config.Weighted, i.Config.Dynamic})
	i.Config.ListenerVersion = oracleFixtureHash([]any{i.Config.Ancillary, i.Config.Opaque})
	if i.Config.SecretVersion == "" {
		i.Config.SecretVersion = "0"
	}
	return i
}

func oracleClone(i oracleInput) oracleInput {
	b, _ := json.Marshal(i)
	var out oracleInput
	if err := json.Unmarshal(b, &out); err != nil {
		panic(err)
	}
	return out
}

func oracleBase() oracleInput {
	return oracleNormalize(oracleInput{Gateway: true,
		Clusters:  []oracleCluster{{Name: "a", EDS: true, Payload: 1}, {Name: "b", EDS: true, Payload: 1}},
		Endpoints: []oracleEndpoint{{Name: "a", Payload: 1}, {Name: "b", Payload: 1}},
		Config:    oracleConfig{Refs: []string{"a"}},
	})
}

type oracleHistory struct {
	request oracleRequest
	next    int
	ids     map[string][]int
}

func newOracleHistory() *oracleHistory {
	return &oracleHistory{request: oracleRequest{Schema: 1, Profile: "research-publication-v1", Events: []oracleEvent{}}, ids: map[string][]int{}}
}
func (h *oracleHistory) build(client string, i oracleInput) int {
	h.next++
	copy := oracleNormalize(oracleClone(i))
	h.request.Events = append(h.request.Events, oracleEvent{Kind: "build", Client: client, ID: h.next, Input: &copy})
	h.ids[client] = append(h.ids[client], h.next)
	return h.next
}
func (h *oracleHistory) publish(client string, id int) {
	h.request.Events = append(h.request.Events, oracleEvent{Kind: "publish", Client: client, ID: id})
}
func (h *oracleHistory) send(client string, i oracleInput) int {
	id := h.build(client, i)
	h.publish(client, id)
	return id
}

func oracleExamples() map[string]oracleRequest {
	out := map[string]oracleRequest{}
	for _, mode := range []string{"derived-empty", "underived", "errored", "shared", "bootstrap", "ancillary", "opaque", "dynamic", "queued", "equality", "missing-first", "filtered-version"} {
		h := newOracleHistory()
		base := oracleBase()
		if mode == "shared" {
			base.Clusters[0].Assignment = "shared"
			base.Clusters[0].Hash = ""
			base.Endpoints = append(base.Endpoints, oracleEndpoint{Name: "shared", Payload: 2})
		}
		if mode == "ancillary" {
			base.Config.Ancillary = []string{"aggregate"}
			base.Clusters = append(base.Clusters, oracleCluster{Name: "aggregate", Deps: []string{"leaf"}}, oracleCluster{Name: "leaf", EDS: true})
			base.Endpoints = append(base.Endpoints, oracleEndpoint{Name: "leaf", Payload: 3})
		}
		base.Config.Opaque = mode == "opaque"
		base.Config.Dynamic = mode == "dynamic"
		if mode != "missing-first" {
			h.send("one", base)
		}
		next := oracleClone(base)
		next.Config.Refs = []string{"new"}
		next.Config.SecretVersion = "rotated"
		next.Clusters = append(next.Clusters, oracleCluster{Name: "new", EDS: true})
		switch mode {
		case "derived-empty":
			next.Endpoints = append(next.Endpoints, oracleEndpoint{Name: "new"})
		case "errored":
			next.Clusters = nil
			next.Errored = []string{"a", "new"}
		case "shared":
			next.Clusters = []oracleCluster{{Name: "b", EDS: true, Assignment: "shared", Payload: 2}}
			next.Config.Refs = []string{"a", "b"}
			next.Endpoints = []oracleEndpoint{{Name: "shared", Payload: 0}}
		case "bootstrap":
			next.Bootstrap = []string{"local"}
			next.Endpoints = append(next.Endpoints, oracleEndpoint{Name: "local", Payload: 2})
		case "filtered-version":
			// Exercise the production fold after filtering/synthesis, as well
			// as the input-row fold on the passthrough path.
			next.Endpoints = append(next.Endpoints, oracleEndpoint{Name: "stale", Payload: 2})
		case "ancillary", "opaque", "dynamic":
			next.Clusters = []oracleCluster{{Name: "new", EDS: true}}
		case "missing-first":
			next.Clusters = nil
		}
		id := h.build("one", next)
		if mode == "queued" {
			newer := oracleClone(base)
			newer.Config.SecretVersion = "latest"
			h.send("one", newer)
		}
		h.publish("one", id)
		if mode == "equality" {
			// Derived empty has the same version as the synthesized empty, but
			// changes eligibility. Versions alone cannot suppress this change.
			nameHasher := fnv.New64a()
			_, _ = nameHasher.Write([]byte("new"))
			next.Endpoints = append(next.Endpoints, oracleEndpoint{Name: "new", Hash: strconv.FormatUint(nameHasher.Sum64(), 10)})
			other := h.build("one", next)
			h.request.Events = append(h.request.Events, oracleEvent{Kind: "equal", Client: "one", ID: id, Other: other})
			h.publish("one", other)
		}
		h.request.Events = append(h.request.Events, oracleEvent{Kind: "delete", Client: "one"})
		h.send("two", oracleBase())
		h.send("one", base)
		out[mode] = h.request
	}
	return out
}

func TestPublicationOracleExamples(t *testing.T) {
	o := startPublicationOracle(t)
	for name, r := range oracleExamples() {
		t.Run(name, func(t *testing.T) { checkPublicationOracle(t, o, r) })
	}
}

var oracleNames = []string{"a", "b", "c", "d"}
var oracleEndpointNames = []string{"a", "b", "c", "d", "shared", "local"}

func oracleMask(b byte) []string {
	names := []string{}
	for k, n := range oracleNames {
		if b&(1<<k) != 0 {
			names = append(names, n)
		}
	}
	if b&16 != 0 {
		names = append(names, "blackhole-cluster")
	}
	return names
}

// Four bytes encode one stateful operation. No discard-heavy preconditions:
// every byte sequence decodes into a valid history, and Go's native minimizer
// can remove/reduce operations without leaving dangling candidate IDs.
func oracleDecode(data []byte) oracleRequest {
	if len(data) > 256 {
		data = data[:256]
	}
	h := newOracleHistory()
	worlds := []oracleInput{oracleBase(), oracleBase()}
	for k := range worlds {
		h.send(strconv.Itoa(k), worlds[k])
	}
	for pos := 0; pos+3 < len(data); pos += 4 {
		op, x, y, z := data[pos], data[pos+1], data[pos+2], data[pos+3]
		client := int(op>>4) & 1
		key := strconv.Itoa(client)
		i := &worlds[client]
		name := oracleNames[int(x)%len(oracleNames)]
		remove := func() {
			i.Clusters = slices.DeleteFunc(i.Clusters, func(c oracleCluster) bool { return c.Name == name })
			i.Errored = slices.DeleteFunc(i.Errored, func(n string) bool { return n == name })
		}
		switch op & 15 {
		case 0:
			remove()
		case 1, 2, 3:
			remove()
			c := oracleCluster{Name: name, Payload: uint64(z % 4)}
			if op&15 == 1 {
				c.EDS = true
				c.Assignment = oracleEndpointNames[int(y)%len(oracleEndpointNames)]
			}
			if op&15 == 3 {
				c.Deps = []string{oracleNames[int(y)%len(oracleNames)]}
			}
			i.Clusters = append(i.Clusters, c)
		case 4:
			remove()
			i.Errored = append(i.Errored, name)
		case 5, 6:
			n := oracleEndpointNames[int(x)%len(oracleEndpointNames)]
			i.Endpoints = slices.DeleteFunc(i.Endpoints, func(e oracleEndpoint) bool { return e.Name == n })
			i.Bootstrap = slices.DeleteFunc(i.Bootstrap, func(s string) bool { return s == n })
			if op&15 == 6 {
				e := oracleEndpoint{Name: n, Payload: uint64(z % 4)}
				e.Unhealthy = e.Payload > 0 && y&1 != 0
				i.Endpoints = append(i.Endpoints, e)
				if n == "local" && y&2 != 0 {
					i.Bootstrap = append(i.Bootstrap, n)
				}
			}
		case 7:
			i.Config.Refs = oracleMask(x)
			i.Config.Weighted = y&1 != 0
		case 8:
			i.Config.Ancillary = oracleMask(x)
			i.Config.Opaque = y&1 != 0
			i.Config.Dynamic = y&2 != 0
		case 9:
			i.Gateway = x&1 != 0
		case 10:
			i.Config.SecretVersion = fmt.Sprintf("rotation-%d", x)
		case 11:
			if len(i.Endpoints) > 0 {
				k := int(x) % len(i.Endpoints)
				i.Endpoints[k].Hash = oracleFixtureHash([]any{i.Endpoints[k], y})
			}
		case 12:
			ids := h.ids[key]
			h.publish(key, ids[int(x)%len(ids)])
			continue
		case 13:
			h.request.Events = append(h.request.Events, oracleEvent{Kind: "delete", Client: key})
			continue
		case 14:
			ids := h.ids[key]
			h.request.Events = append(h.request.Events, oracleEvent{Kind: "equal", Client: key, ID: ids[int(x)%len(ids)], Other: ids[int(y)%len(ids)]})
			continue
		case 15: // rebuild identical input (KRT may coalesce this candidate)
		}
		*i = oracleNormalize(*i)
		id := h.build(key, *i)
		if z&128 == 0 {
			h.publish(key, id)
		}
	}
	for k := range worlds {
		key := strconv.Itoa(k)
		ids := h.ids[key]
		h.publish(key, ids[len(ids)-1])
	}
	return h.request
}

func TestPublicationOracleRandomHistories(t *testing.T) {
	o := startPublicationOracle(t)
	count := 100
	if n, err := strconv.Atoi(os.Getenv("XDS_ORACLE_HISTORIES")); err == nil && n > 0 {
		count = n
	}
	for seed := range count {
		//nolint:gosec // Reproducible model-based test inputs.
		rng := rand.New(rand.NewSource(int64(seed)))
		data := make([]byte, 128)
		_, _ = rng.Read(data)
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) { checkPublicationOracle(t, o, oracleDecode(data)) })
	}
}

func FuzzPublicationOracle(f *testing.F) {
	o := startPublicationOracle(f)
	for _, seed := range [][]byte{{}, {7, 2, 0, 0, 5, 1, 0, 0}, {4, 0, 0, 0}, {1, 2, 4, 0, 6, 4, 0, 0}, {9, 0, 0, 0, 9, 1, 0, 0}, {8, 4, 3, 0, 7, 8, 1, 0}, {10, 1, 0, 128, 10, 2, 0, 0, 12, 2, 0, 0}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) { checkPublicationOracle(t, o, oracleDecode(data)) })
}
