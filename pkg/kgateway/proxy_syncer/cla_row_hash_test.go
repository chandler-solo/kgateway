package proxy_syncer_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/proxy_syncer"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

// RF-029 action 1 (devel/formal/research-findings.md): a per-client endpoint
// row is compared by Client, endpointsName, and EndpointsHash only, never by
// the CLA. On main EndpointsHash is LbEpsEqualityHash ^ additionalHash, and the
// prioritizer reads the backend's TrafficDistribution when no plugin set
// PriorityInfo. A Service trafficDistribution change must therefore move the
// row hash or KRT keeps the previous priorities until an endpoint changes.
func TestUccWithEndpointsRowChangesWhenOnlyTrafficDistributionChanges(t *testing.T) {
	row := func(distribution wellknown.TrafficDistribution) proxy_syncer.UccWithEndpoints {
		backend := ir.NewBackendObjectIR(ir.ObjectSource{Kind: "Service", Namespace: "ns", Name: "svc"}, 8080, "", "")
		backend.Obj = &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: "ns"}}
		backend.TrafficDistribution = distribution
		eps := ir.NewEndpointsForBackend(backend)
		// No endpoint plugin contribution: the row hash is the equality hash alone.
		return proxy_syncer.UccWithEndpoints{
			Client:        ir.UniquelyConnectedClient{Role: "gateway", Namespace: "ns", Locality: ir.PodLocality{Region: "r", Zone: "z"}},
			EndpointsHash: eps.LbEpsEqualityHash,
		}
	}
	before := row(wellknown.TrafficDistributionAny)
	after := row(wellknown.TrafficDistributionPreferSameZone)
	if before.Equals(after) {
		t.Fatal("the per-client endpoint row compared equal after only trafficDistribution changed; the rebuilt CLA would never be republished")
	}
	if !before.Equals(row(wellknown.TrafficDistributionAny)) {
		t.Fatal("identical rows compared unequal; the hash is not deterministic")
	}
}
