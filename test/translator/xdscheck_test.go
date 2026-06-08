package translator

import (
	"testing"

	envoyclusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	envoyendpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/stretchr/testify/require"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/pluginsdk/ir"
)

func TestFilterEndpointResourcesForXDSCheckKeepsOnlyRequiredEDSAssignments(t *testing.T) {
	clusters := []*envoyclusterv3.Cluster{
		{
			Name: "eds-by-cluster-name",
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
				Type: envoyclusterv3.Cluster_EDS,
			},
			EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{},
		},
		{
			Name: "eds-by-service-name",
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
				Type: envoyclusterv3.Cluster_EDS,
			},
			EdsClusterConfig: &envoyclusterv3.Cluster_EdsClusterConfig{
				ServiceName: "service-name",
			},
		},
		{
			Name: "static-cluster",
			ClusterDiscoveryType: &envoyclusterv3.Cluster_Type{
				Type: envoyclusterv3.Cluster_STATIC,
			},
		},
	}
	endpoints := []*envoyendpointv3.ClusterLoadAssignment{
		{ClusterName: "eds-by-cluster-name"},
		{ClusterName: "service-name"},
		{ClusterName: "eds-by-service-name"},
		{ClusterName: "static-cluster"},
		{ClusterName: "stale"},
		nil,
	}

	filtered := filterEndpointResourcesForXDSCheck(clusters, endpoints)

	require.Equal(t, []string{"eds-by-cluster-name", "service-name"}, clusterLoadAssignmentNames(filtered))
}

func TestCloneEndpointsForBackendUsesGatewayScopedResourceIdentity(t *testing.T) {
	baseBackend := ir.NewBackendObjectIR(ir.ObjectSource{
		Kind:      "Service",
		Namespace: "default",
		Name:      "backend",
	}, 443, "", "")
	baseEndpoints := ir.NewEndpointsForBackend(baseBackend)
	locality := ir.PodLocality{Region: "region", Zone: "zone", Subzone: "subzone"}
	baseEndpoints.Add(locality, ir.EndpointWithMd{
		LbEndpoint: &envoyendpointv3.LbEndpoint{},
	})
	gatewayBackend := baseBackend.CloneForGatewayBackendClientCertificate(
		ir.ObjectSource{
			Group:     gwv1.GroupVersion.Group,
			Kind:      "Gateway",
			Namespace: "default",
			Name:      "gateway",
		},
		&ir.GatewayBackendClientCertificateIR{},
	)

	clonedEndpoints := cloneEndpointsForBackend(*baseEndpoints, gatewayBackend)

	require.NotEqual(t, baseEndpoints.ClusterName, clonedEndpoints.ClusterName)
	require.Equal(t, gatewayBackend.ClusterName(), clonedEndpoints.ClusterName)
	require.Equal(t, gatewayBackend.ResourceName(), clonedEndpoints.ResourceName())
	require.Len(t, clonedEndpoints.LbEps[locality], 1)
}

func clusterLoadAssignmentNames(endpoints []*envoyendpointv3.ClusterLoadAssignment) []string {
	names := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		names = append(names, endpoint.GetClusterName())
	}
	return names
}
