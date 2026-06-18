package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"istio.io/api/networking/v1alpha3"
	networkingclient "istio.io/client-go/pkg/apis/networking/v1"

	apisettings "github.com/kgateway-dev/kgateway/v2/api/settings"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/extensions2/plugins/destrule"
	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

func TestPodLocalityDecisionModes(t *testing.T) {
	assert.False(t, newPodLocalityDecision(apisettings.PodLocalityXDSOff).use(),
		"Off must never use pod locality")
	assert.True(t, newPodLocalityDecision(apisettings.PodLocalityXDSOn).use(),
		"On must always use pod locality")
	assert.True(t, newPodLocalityDecision(apisettings.PodLocalityXDSAuto).use(),
		"Auto with no gate installed must conservatively use pod locality")
}

// epsFor builds an EndpointsForBackend with the given traffic distribution and
// endpoint localities (values are irrelevant; only the locality keys matter).
func epsFor(td wellknown.TrafficDistribution, locs ...ir.PodLocality) ir.EndpointsForBackend {
	m := ir.LocalityLbMap{}
	for _, l := range locs {
		m[l] = nil
	}
	return ir.EndpointsForBackend{TrafficDistribution: td, LbEps: m}
}

func drWithLocalityLb(enabled bool) destrule.DestinationRuleWrapper {
	var lb *v1alpha3.LoadBalancerSettings
	if enabled {
		lb = &v1alpha3.LoadBalancerSettings{LocalityLbSetting: &v1alpha3.LocalityLoadBalancerSetting{}}
	}
	return destrule.DestinationRuleWrapper{
		DestinationRule: &networkingclient.DestinationRule{
			Spec: v1alpha3.DestinationRule{TrafficPolicy: &v1alpha3.TrafficPolicy{LoadBalancer: lb}},
		},
	}
}

func TestLocalityInUseFromInputs(t *testing.T) {
	zoneA1 := ir.PodLocality{Region: "ice03", Zone: "f03", Subzone: "ice03-f03-111"}
	zoneA2 := ir.PodLocality{Region: "ice03", Zone: "f03", Subzone: "ice03-f03-115"}
	zoneB := ir.PodLocality{Region: "ice03", Zone: "f01", Subzone: "ice03-f01-001"}

	cases := []struct {
		name string
		eps  []ir.EndpointsForBackend
		drs  []destrule.DestinationRuleWrapper
		want bool
	}{
		{
			name: "no config: not in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionAny, zoneA1, zoneB)},
			want: false,
		},
		{
			name: "PreferSameZone but single zone (multi-subzone): not in use (the single-zone customer)",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionPreferSameZone, zoneA1, zoneA2)},
			want: false,
		},
		{
			name: "PreferSameZone spanning zones: in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionPreferSameZone, zoneA1, zoneB)},
			want: true,
		},
		{
			name: "PreferSameNode: conservatively in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionPreferSameNode, zoneA1)},
			want: true,
		},
		{
			name: "PreferNetwork: conservatively in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionPreferNetwork, zoneA1)},
			want: true,
		},
		{
			name: "mixed backends, one PreferSameZone spans: in use",
			eps: []ir.EndpointsForBackend{
				epsFor(wellknown.TrafficDistributionAny, zoneA1),
				epsFor(wellknown.TrafficDistributionPreferSameZone, zoneA1, zoneB),
			},
			want: true,
		},
		{
			name: "DR localityLb + endpoints span subzone: in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionAny, zoneA1, zoneA2)},
			drs:  []destrule.DestinationRuleWrapper{drWithLocalityLb(true)},
			want: true,
		},
		{
			name: "DR localityLb + endpoints single locality: not in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionAny, zoneA1)},
			drs:  []destrule.DestinationRuleWrapper{drWithLocalityLb(true)},
			want: false,
		},
		{
			name: "DR present but localityLb disabled: not in use",
			eps:  []ir.EndpointsForBackend{epsFor(wellknown.TrafficDistributionAny, zoneA1, zoneB)},
			drs:  []destrule.DestinationRuleWrapper{drWithLocalityLb(false)},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, localityInUseFromInputs(tc.eps, tc.drs))
		})
	}
}
