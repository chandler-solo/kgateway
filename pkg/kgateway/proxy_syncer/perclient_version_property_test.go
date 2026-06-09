package proxy_syncer

import (
	"fmt"
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"google.golang.org/protobuf/proto"
)

// TestFilterEndpointResourcesForClusters_VersionDigestProperties discharges
// assumption IMPL-A1 (devel/formal/lean/ASSUMPTIONS.md): the formal spec
// models the EDS version as an injective digest of the filtered endpoint
// content, so the implementation's hash-based version must be deterministic
// for content-equal sets (regardless of resource order or proto instance
// identity) and must differ between content-different sets. A violation here
// would invalidate the EDSResourceSetChangeChangesVersion invariant that the
// Lean proof establishes for the model.
func TestFilterEndpointResourcesForClusters_VersionDigestProperties(t *testing.T) {
	cla := func(clusterName string, ports ...uint32) *envoyendpointv3.ClusterLoadAssignment {
		lbEndpoints := make([]*envoyendpointv3.LbEndpoint, 0, len(ports))
		for _, port := range ports {
			lbEndpoints = append(lbEndpoints, &envoyendpointv3.LbEndpoint{
				HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{
					Endpoint: &envoyendpointv3.Endpoint{
						Address: &envoycorev3.Address{
							Address: &envoycorev3.Address_SocketAddress{
								SocketAddress: &envoycorev3.SocketAddress{
									Address: "10.0.0.1",
									PortSpecifier: &envoycorev3.SocketAddress_PortValue{
										PortValue: port,
									},
								},
							},
						},
					},
				},
			})
		}
		return &envoyendpointv3.ClusterLoadAssignment{
			ClusterName: clusterName,
			Endpoints:   []*envoyendpointv3.LocalityLbEndpoints{{LbEndpoints: lbEndpoints}},
		}
	}

	// filteredVersion runs the production filter with a CDS set covering the
	// given CLAs plus one stale CLA, so the hash-versioned branch (not the
	// passthrough branch) is exercised.
	filteredVersion := func(class []*envoyendpointv3.ClusterLoadAssignment) string {
		clusterResources := make([]envoycachetypes.ResourceWithTTL, 0, len(class))
		seen := map[string]struct{}{}
		for _, c := range class {
			if _, ok := seen[c.GetClusterName()]; ok {
				continue
			}
			seen[c.GetClusterName()] = struct{}{}
			clusterResources = append(clusterResources, envoycachetypes.ResourceWithTTL{
				Resource: &envoyclusterv3.Cluster{
					Name:                 c.GetClusterName(),
					ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{Type: envoyclusterv3.Cluster_EDS},
				},
			})
		}
		endpointResources := make([]envoycachetypes.ResourceWithTTL, 0, len(class)+1)
		for _, c := range class {
			endpointResources = append(endpointResources, envoycachetypes.ResourceWithTTL{Resource: c})
		}
		endpointResources = append(endpointResources, envoycachetypes.ResourceWithTTL{
			Resource: cla("stale-not-in-cds", 80),
		})
		out := filterEndpointResourcesForClusters(
			envoycache.NewResourcesWithTTL("v-in", clusterResources),
			envoycache.NewResourcesWithTTL("v-in", endpointResources),
		)
		return out.Version
	}

	clone := func(class []*envoyendpointv3.ClusterLoadAssignment) []*envoyendpointv3.ClusterLoadAssignment {
		out := make([]*envoyendpointv3.ClusterLoadAssignment, 0, len(class))
		for _, c := range class {
			out = append(out, proto.CloneOf(c))
		}
		return out
	}

	// A corpus of pairwise content-different endpoint sets: different cluster
	// names, different cardinalities, and same names with different endpoint
	// payloads.
	corpus := map[string][]*envoyendpointv3.ClusterLoadAssignment{
		"empty":             {},
		"a":                 {cla("cluster-a", 8080)},
		"a-other-port":      {cla("cluster-a", 8081)},
		"a-two-endpoints":   {cla("cluster-a", 8080, 8081)},
		"b":                 {cla("cluster-b", 8080)},
		"a-and-b":           {cla("cluster-a", 8080), cla("cluster-b", 8080)},
		"a-and-b-and-c":     {cla("cluster-a", 8080), cla("cluster-b", 8080), cla("cluster-c", 8080)},
		"a-empty-endpoints": {cla("cluster-a")},
	}

	versions := map[string]string{}
	for name, class := range corpus {
		versions[name] = filteredVersion(class)
	}

	for name, class := range corpus {
		// Determinism across proto instances: a deep copy of the same content
		// must produce the same version string.
		if got := filteredVersion(clone(class)); got != versions[name] {
			t.Errorf("set %q: cloned content produced version %q, want %q", name, got, versions[name])
		}
		// Order invariance: reversing the resource order must not change the
		// version.
		reversed := make([]*envoyendpointv3.ClusterLoadAssignment, 0, len(class))
		for i := len(class) - 1; i >= 0; i-- {
			reversed = append(reversed, class[i])
		}
		if got := filteredVersion(reversed); got != versions[name] {
			t.Errorf("set %q: reversed order produced version %q, want %q", name, got, versions[name])
		}
	}

	// Injectivity over the corpus: content-different sets must get different
	// versions. (The XOR-of-hashes construction cannot be proven collision-
	// free; this pins the property on a representative corpus so an
	// accidental degenerate version scheme — e.g. a constant — fails loudly.)
	for nameA, versionA := range versions {
		for nameB, versionB := range versions {
			if nameA < nameB && versionA == versionB {
				t.Errorf("content-different sets %q and %q share version %q", nameA, nameB, versionA)
			}
		}
	}

	if testing.Verbose() {
		for name, v := range versions {
			fmt.Printf("corpus %-18s -> version %s\n", name, v)
		}
	}
}
