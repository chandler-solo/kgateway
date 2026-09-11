package proxy_syncer

import (
	"testing"
	"time"

	envoycorev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	envoylistenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	envoycachetypes "github.com/envoyproxy/go-control-plane/pkg/cache/types"
	envoycache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	envoyresourcev3 "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/onsi/gomega"
	"google.golang.org/protobuf/proto"
	"istio.io/istio/pkg/kube/krt"
	"k8s.io/apimachinery/pkg/types"

	kgtranslator "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/translator"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/xds"
	sdk "github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/krtutil"
	krtpkg "github.com/kgateway-dev/kgateway/v2/pkg/utils/krtutil"
)

// RF-025 / RF-008: the trace version relation was developed against unit
// fixtures that fabricate EndpointsHash. This scenario runs the real endpoint
// translation (translateEndpoints, the EndpointsHash it produces, and the
// synthesized CLA) through snapshotPerClient and syncXds, so the recorded
// trace relates a production-shaped EDS version to the CLA content digest.
// Two publications with different endpoint content must carry different EDS
// versions; the checker's version-reuse rule sees real versions here.
func TestSnapshotPerClientRealEndpointTranslationVersionRelation(t *testing.T) {
	g := gomega.NewWithT(t)
	ctx := t.Context()
	krtopts := krtutil.NewKrtOptions(ctx.Done(), nil)

	translator := kgtranslator.NewCombinedTranslator(ctx, sdk.Plugin{}, nil, nil)
	backend := ir.NewBackendObjectIR(ir.ObjectSource{Kind: "Service", Namespace: "ns", Name: "backend"}, 80, "", "")
	clusterName := backend.ClusterName()
	podEndpoint := func(address string) ir.EndpointWithMd {
		return ir.EndpointWithMd{LbEndpoint: &envoyendpointv3.LbEndpoint{
			HostIdentifier: &envoyendpointv3.LbEndpoint_Endpoint{Endpoint: &envoyendpointv3.Endpoint{
				Address: &envoycorev3.Address{Address: &envoycorev3.Address_SocketAddress{SocketAddress: &envoycorev3.SocketAddress{
					Address: address, PortSpecifier: &envoycorev3.SocketAddress_PortValue{PortValue: 8080},
				}}},
			}},
		}}
	}
	sourceWith := func(addresses ...string) ir.EndpointsForBackend {
		source := ir.NewEndpointsForBackend(backend)
		for _, address := range addresses {
			source.Add(ir.PodLocality{}, podEndpoint(address))
		}
		return *source
	}

	role := xds.OwnerNamespaceNameID(wellknown.GatewayApiProxyValue, "ns", "gw")
	ucc := ir.NewUniquelyConnectedClient(role, "", nil, ir.PodLocality{})
	uccs := krt.NewStaticCollection(nil, []ir.UniquelyConnectedClient{ucc}, krtopts.ToOptions("RealEndpointClients")...)
	sources := krt.NewStaticCollection(nil, []ir.EndpointsForBackend{sourceWith("10.0.0.1")}, krtopts.ToOptions("RealEndpointSources")...)
	perClientEndpoints := NewPerClientEnvoyEndpoints(krtopts, uccs, sources, translator.TranslateEndpoints)

	listeners := sliceToResources([]*envoylistenerv3.Listener{httpListenerWithRDS(t, "listener", "route-config")})
	routes := routeResourcesForClusters(clusterName)
	mostXdsSnapshots := krt.NewStaticCollection(nil, []GatewayXdsResources{{
		NamespacedName:     types.NamespacedName{Namespace: "ns", Name: "gw"},
		Routes:             routes,
		Listeners:          listeners,
		ReferencedClusters: collectReferencedClusters(routes, listeners),
	}}, krtopts.ToOptions("RealEndpointGateways")...)
	clusterCol := krt.NewStaticCollection(nil, []uccWithCluster{edsClusterForClient(ucc, clusterName, 1)}, krtopts.ToOptions("RealEndpointClusters")...)
	snapshots := snapshotPerClient(krtopts, uccs, mostXdsSnapshots, perClientEndpoints, PerClientEnvoyClusters{
		clusters: clusterCol,
		index:    krtpkg.UnnamedIndex(clusterCol, func(c uccWithCluster) []string { return []string{c.Client.ResourceName()} }),
	})

	c := envoycache.NewSnapshotCache(true, envoycache.IDHash{}, nil)
	proxyTranslator := NewProxyTranslator(c)
	installCurrent := func(previousVersion string) envoycache.ResourceSnapshot {
		t.Helper()
		var wrapper XdsSnapWrapper
		g.Eventually(func() bool {
			wrappers := snapshots.List()
			if len(wrappers) != 1 || wrappers[0].snap == nil {
				return false
			}
			wrapper = wrappers[0]
			return wrapper.snap.Resources[envoycachetypes.Endpoint].Version != previousVersion
		}, 2*time.Second, 20*time.Millisecond).Should(gomega.BeTrue(), "a snapshot with a new EDS version must be derived")
		g.Expect(wrapper.deferred).To(gomega.BeFalse(), "a usable real endpoint must not defer publication")
		proxyTranslator.syncXds(ctx, wrapper)
		served, err := c.GetSnapshot(ucc.ResourceName())
		g.Expect(err).NotTo(gomega.HaveOccurred())
		return served
	}

	first := installCurrent("")
	firstVersion := first.GetVersion(envoyresourcev3.EndpointType)
	firstCLA := first.GetResources(envoyresourcev3.EndpointType)[clusterName]
	g.Expect(firstCLA).NotTo(gomega.BeNil(), "the real translation must publish a CLA for the referenced cluster")

	// Endpoint content changes: a second pod. The published EDS version must move.
	sources.UpdateObject(sourceWith("10.0.0.1", "10.0.0.2"))
	second := installCurrent(firstVersion)
	secondVersion := second.GetVersion(envoyresourcev3.EndpointType)
	secondCLA := second.GetResources(envoyresourcev3.EndpointType)[clusterName]
	g.Expect(secondVersion).NotTo(gomega.Equal(firstVersion), "changed endpoint content must change the EDS version")
	g.Expect(proto.Equal(firstCLA, secondCLA)).To(gomega.BeFalse(), "the published CLA must reflect the added endpoint")

	// Endpoint content changes back: the version must move again and the
	// content must equal the first publication's (the digest relation in the
	// trace sees equal content with a version that may or may not equal the
	// first; only reuse with different content is a violation).
	sources.UpdateObject(sourceWith("10.0.0.1"))
	third := installCurrent(secondVersion)
	thirdCLA := third.GetResources(envoyresourcev3.EndpointType)[clusterName]
	g.Expect(third.GetVersion(envoyresourcev3.EndpointType)).NotTo(gomega.Equal(secondVersion))
	g.Expect(proto.Equal(firstCLA, thirdCLA)).To(gomega.BeTrue(), "restoring the endpoint set must restore the CLA content")
	if third.GetVersion(envoyresourcev3.EndpointType) != firstVersion {
		t.Logf("RF-025: identical CLA content published under versions %q and %q; the EDS version is not a pure function of the CLA", firstVersion, third.GetVersion(envoyresourcev3.EndpointType))
	}
}
