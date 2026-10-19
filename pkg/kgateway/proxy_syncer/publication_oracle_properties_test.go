//go:build formal_oracle

package proxy_syncer

import (
	"strings"
	"testing"

	ct "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"google.golang.org/protobuf/proto"
)

func TestPublicationOracleVersionCollision(t *testing.T) {
	o := startPublicationOracle(t)
	for _, intervening := range []bool{false, true} {
		t.Run(map[bool]string{false: "adjacent", true: "intervening-version"}[intervening], func(t *testing.T) {
			h := newOracleHistory()
			old := oracleBase()
			old.Clusters = old.Clusters[:1]
			old.Endpoints = []oracleEndpoint{{Name: "a", Payload: 1, Hash: "16"}}
			h.send("one", old)
			if intervening {
				middle := oracleClone(old)
				middle.Endpoints[0].Hash = "17"
				middle.Endpoints[0].Payload = 3
				h.send("one", middle)
			}
			next := oracleBase()
			next.Clusters = append(next.Clusters, oracleCluster{Name: "c", EDS: true})
			next.Endpoints = []oracleEndpoint{{Name: "a", Payload: 2, Hash: "18"}, {Name: "b", Payload: 1, Hash: "20"}, {Name: "c", Payload: 1, Hash: "22"}}
			h.send("one", next)
			want, err := o.predict(h.request)
			if err != nil {
				t.Fatal(err)
			}
			if want[1].Installed.EDSVersion != "16" || want[len(want)-1].Installed.EDSVersion != "16" {
				t.Fatal("oracle no longer reproduces the known XOR collision")
			}
			err = oracleCheckHistory(h.request, want)
			if err == nil || !strings.Contains(err.Error(), "version-reuse") {
				t.Fatalf("want independent version-reuse rejection despite differential agreement, got %v", err)
			}
		})
	}
}

func TestPublicationOracleComparatorRejectsCorruption(t *testing.T) {
	o := startPublicationOracle(t)
	h := newOracleHistory()
	h.send("one", oracleBase())
	want, err := o.predict(h.request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := want[1].Installed
	for _, kind := range []string{"resource", "version", "payload", "missing-install", "extra-install"} {
		t.Run(kind, func(t *testing.T) {
			actual := oracleExpectedSnapshot(*snapshot)
			expected := snapshot
			switch kind {
			case "resource":
				delete(actual.Resources[ct.Endpoint].Items, "a")
			case "version":
				actual.Resources[ct.Endpoint].Version = "bad-version"
			case "payload":
				e := actual.Resources[ct.Endpoint].Items["a"]
				e.Resource = proto.Clone(oracleEndpointProto(oracleEndpoint{Name: "a", Payload: 99}))
				actual.Resources[ct.Endpoint].Items["a"] = e
			case "missing-install":
				actual = nil
			case "extra-install":
				expected = nil
			}
			if err := oracleCompareSnapshot(actual, expected); err == nil {
				t.Fatal("corrupted observation accepted")
			}
		})
	}
}
