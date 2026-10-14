package proxy_syncer

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/onsi/gomega"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

func staticTestCluster(name string) *envoyclusterv3.Cluster {
	return &envoyclusterv3.Cluster{Name: name, ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_STATIC}}
}

// requiredSet flattens a requiredEDSNames, failing if removed is not a subset
// of base or extra repeats base.
func requiredSet(g *gomega.WithT, r requiredEDSNames) map[string]struct{} {
	out := maps.Clone(r.base)
	if out == nil {
		out = map[string]struct{}{}
	}
	for name := range r.removed {
		g.Expect(r.base).To(gomega.HaveKey(name), "removed must be a subset of base")
		delete(out, name)
	}
	for name := range r.extra {
		g.Expect(r.base).ToNot(gomega.HaveKey(name), "extra must not repeat a base name")
		out[name] = struct{}{}
	}
	g.Expect(r.len()).To(gomega.Equal(len(out)))
	return out
}

// TestClientRequiredEDSNamesMatchesMergedPayload pins the row-derived set to
// what resolving the merged CDS payload from scratch yields, including a
// gateway cluster that shadows a row cluster and service_name aliases.
func TestClientRequiredEDSNamesMatchesMergedPayload(t *testing.T) {
	ucc := ir.NewUniquelyConnectedClient("role", "ns", nil, ir.PodLocality{})
	row := assemblePerClientClusters(ucc, []uccWithCluster{
		publishedClusterRow(ucc, edsTestCluster("a", "")),
		publishedClusterRow(ucc, edsTestCluster("b", "shared")),
		publishedClusterRow(ucc, edsTestCluster("c", "shared")),
		publishedClusterRow(ucc, staticTestCluster("s")),
		{Client: ucc, Name: "broken", Error: errors.New("invalid"), PerClientError: true},
	})
	resource := func(c *envoyclusterv3.Cluster) envoycachetypes.ResourceWithTTL {
		return envoycachetypes.ResourceWithTTL{Resource: c}
	}

	cases := []struct {
		name      string
		extra     []envoycachetypes.ResourceWithTTL
		bootstrap string
		// drop names clusters a later step removes from the merged payload.
		drop []string
		// fallback is set when the payload must be resolved from scratch
		// rather than adjusted from the row's set.
		fallback bool
	}{
		{name: "row only"},
		{name: "bootstrap name", bootstrap: "local"},
		{name: "bootstrap name already required", bootstrap: "a"},
		{name: "gateway clusters", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("gw-eds", "")), resource(staticTestCluster("gw-static")),
		}},
		{name: "gateway cluster shadows an EDS row cluster", extra: []envoycachetypes.ResourceWithTTL{
			resource(staticTestCluster("a")),
		}},
		{name: "gateway cluster shadows one alias", extra: []envoycachetypes.ResourceWithTTL{
			resource(staticTestCluster("b")),
		}, bootstrap: "local"},
		{name: "gateway clusters shadow every alias", extra: []envoycachetypes.ResourceWithTTL{
			resource(staticTestCluster("b")), resource(staticTestCluster("c")),
		}},
		{name: "gateway cluster shadows a row cluster with the same assignment", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("a", "")),
		}},
		{name: "gateway cluster moves an alias onto another row cluster's assignment", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("b", "a")),
		}},
		{name: "gateway cluster renames a row cluster's assignment", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("s", "now-eds")),
		}},
		{name: "two gateway clusters share a name", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("gw", "gw-svc")), resource(staticTestCluster("gw")),
		}},
		{name: "two gateway clusters share a name, the later one EDS", extra: []envoycachetypes.ResourceWithTTL{
			resource(staticTestCluster("gw")), resource(edsTestCluster("gw", "gw-svc")),
		}},
		{name: "two gateway clusters shadow one row cluster", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("a", "a-svc")), resource(staticTestCluster("a")),
		}},
		{name: "a later step filters a row cluster out of the payload", drop: []string{"a"}, fallback: true},
		{name: "a later step filters a gateway cluster out of the payload", extra: []envoycachetypes.ResourceWithTTL{
			resource(edsTestCluster("gw-eds", "")),
		}, drop: []string{"gw-eds"}, fallback: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			before := maps.Clone(row.edsNames)

			merged := maps.Clone(row.clusters.Items)
			for _, item := range tc.extra {
				merged[envoycache.GetResourceName(item.Resource)] = item
			}
			for _, name := range tc.drop {
				delete(merged, name)
			}
			want := requiredSet(g, requiredEDSNames{base: edsNamesOf(merged)}.with(tc.bootstrap))

			resolved := clientRequiredEDSNames(row, tc.extra, merged, tc.bootstrap)
			g.Expect(reflect.ValueOf(resolved.base).UnsafePointer() != reflect.ValueOf(row.edsNames).UnsafePointer()).To(gomega.Equal(tc.fallback),
				"only a payload that is not the row merged with the gateway clusters may be resolved from scratch")
			got := requiredSet(g, resolved)
			g.Expect(got).To(gomega.Equal(want))
			g.Expect(row.edsNames).To(gomega.Equal(before), "the row's set is shared and must not be written to")
		})
	}
}

// TestAssembledEDSNamesFollowThePublishedPayload covers two rows with one name:
// the payload keeps only the later one, and the row's EDS names and EDS
// cluster digest must agree with a row assembled without the dropped one.
func TestAssembledEDSNamesFollowThePublishedPayload(t *testing.T) {
	g := gomega.NewWithT(t)
	ucc := ir.NewUniquelyConnectedClient("role", "ns", nil, ir.PodLocality{})
	row := assemblePerClientClusters(ucc, []uccWithCluster{
		publishedClusterRow(ucc, edsTestCluster("dup", "dup-svc")),
		publishedClusterRow(ucc, staticTestCluster("dup")),
		publishedClusterRow(ucc, edsTestCluster("a", "")),
	})
	g.Expect(row.edsNames).To(gomega.Equal(edsNamesOf(row.clusters.Items)))
	g.Expect(row.edsNames).To(gomega.Equal(map[string]struct{}{"a": {}}))

	deduped := assemblePerClientClusters(ucc, []uccWithCluster{
		publishedClusterRow(ucc, staticTestCluster("dup")),
		publishedClusterRow(ucc, edsTestCluster("a", "")),
	})
	g.Expect(row.edsClustersDigest).To(gomega.Equal(deduped.edsClustersDigest), "a dropped duplicate must not move the EDS version")
}

// TestFilterEndpointsForEDSNamesHonorsExtraAndRemoved covers the parts of the
// required set beyond base: extra names, such as bootstrap or gateway
// assignments, are kept or synthesized, and removed names are neither.
func TestFilterEndpointsForEDSNamesHonorsExtraAndRemoved(t *testing.T) {
	cla := func(name string) envoycachetypes.ResourceWithTTL {
		return envoycachetypes.ResourceWithTTL{Resource: &envoyendpointv3.ClusterLoadAssignment{ClusterName: name}}
	}
	endpointsOf := func(names ...string) envoycache.Resources {
		items := make([]envoycachetypes.ResourceWithTTL, 0, len(names))
		for _, name := range names {
			items = append(items, cla(name))
		}
		return envoycache.NewResourcesWithTTL("v", items)
	}
	set := func(names ...string) map[string]struct{} {
		out := make(map[string]struct{}, len(names))
		for _, name := range names {
			out[name] = struct{}{}
		}
		return out
	}
	base := set("a", "b")

	cases := []struct {
		name            string
		required        requiredEDSNames
		endpoints       envoycache.Resources
		wantNames       map[string]struct{}
		wantSynthesized map[string]struct{}
		wantUnfiltered  bool
	}{
		{
			name:           "extra assignment present",
			required:       requiredEDSNames{base: base}.with("local"),
			endpoints:      endpointsOf("a", "b", "local"),
			wantNames:      set("a", "b", "local"),
			wantUnfiltered: true,
		},
		{
			name:            "extra assignment missing",
			required:        requiredEDSNames{base: base}.with("local"),
			endpoints:       endpointsOf("a", "b"),
			wantNames:       set("a", "b", "local"),
			wantSynthesized: set("local"),
		},
		{
			name:      "removed assignment present",
			required:  requiredEDSNames{base: base}.without("b"),
			endpoints: endpointsOf("a", "b"),
			wantNames: set("a"),
		},
		{
			name:      "removed assignment missing",
			required:  requiredEDSNames{base: base}.without("b"),
			endpoints: endpointsOf("a"),
			wantNames: set("a"),
			// "a" alone is exactly the required set.
			wantUnfiltered: true,
		},
		{
			name:            "removed assignment re-added as extra",
			required:        requiredEDSNames{base: base}.without("b").with("b"),
			endpoints:       endpointsOf("a"),
			wantNames:       set("a", "b"),
			wantSynthesized: set("b"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := gomega.NewWithT(t)
			const inputDigest = 42
			out, synthesized, digest := filterEndpointsForEDSNames(tc.required, tc.endpoints, nil, inputDigest)
			g.Expect(set(slices.Collect(maps.Keys(out.Items))...)).To(gomega.Equal(tc.wantNames))
			if tc.wantSynthesized == nil {
				g.Expect(synthesized).To(gomega.BeEmpty())
			} else {
				g.Expect(synthesized).To(gomega.Equal(tc.wantSynthesized))
			}
			g.Expect(digest == inputDigest).To(gomega.Equal(tc.wantUnfiltered), "only an unfiltered set keeps the input digest")
			g.Expect(base).To(gomega.Equal(set("a", "b")), "base is shared and must not be written to")
		})
	}
}
