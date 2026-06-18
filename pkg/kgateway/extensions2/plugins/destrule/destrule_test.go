package destrule

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"istio.io/api/networking/v1alpha3"
	networkingclient "istio.io/client-go/pkg/apis/networking/v1"
)

func wrap(tp *v1alpha3.TrafficPolicy) DestinationRuleWrapper {
	return DestinationRuleWrapper{
		DestinationRule: &networkingclient.DestinationRule{
			Spec: v1alpha3.DestinationRule{TrafficPolicy: tp},
		},
	}
}

func localityLb(enabled *wrapperspb.BoolValue) *v1alpha3.LoadBalancerSettings {
	return &v1alpha3.LoadBalancerSettings{
		LocalityLbSetting: &v1alpha3.LocalityLoadBalancerSetting{Enabled: enabled},
	}
}

func TestHasEnabledLocalityLbSetting(t *testing.T) {
	cases := []struct {
		name string
		tp   *v1alpha3.TrafficPolicy
		want bool
	}{
		{name: "nil traffic policy", tp: nil, want: false},
		{name: "no load balancer", tp: &v1alpha3.TrafficPolicy{}, want: false},
		{
			name: "top-level localityLbSetting, enabled unset (defaults on)",
			tp:   &v1alpha3.TrafficPolicy{LoadBalancer: localityLb(nil)},
			want: true,
		},
		{
			name: "top-level localityLbSetting explicitly enabled",
			tp:   &v1alpha3.TrafficPolicy{LoadBalancer: localityLb(wrapperspb.Bool(true))},
			want: true,
		},
		{
			name: "top-level localityLbSetting explicitly disabled",
			tp:   &v1alpha3.TrafficPolicy{LoadBalancer: localityLb(wrapperspb.Bool(false))},
			want: false,
		},
		{
			name: "port-level localityLbSetting enabled",
			tp: &v1alpha3.TrafficPolicy{
				PortLevelSettings: []*v1alpha3.TrafficPolicy_PortTrafficPolicy{{
					Port:         &v1alpha3.PortSelector{Number: 8080},
					LoadBalancer: localityLb(wrapperspb.Bool(true)),
				}},
			},
			want: true,
		},
		{
			name: "port-level localityLbSetting disabled",
			tp: &v1alpha3.TrafficPolicy{
				PortLevelSettings: []*v1alpha3.TrafficPolicy_PortTrafficPolicy{{
					Port:         &v1alpha3.PortSelector{Number: 8080},
					LoadBalancer: localityLb(wrapperspb.Bool(false)),
				}},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, HasEnabledLocalityLbSetting(wrap(tc.tp)))
		})
	}
}
