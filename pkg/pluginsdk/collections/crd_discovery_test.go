package collections

import (
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetServedVersions(t *testing.T) {
	const crdName = "tlsroutes.gateway.networking.k8s.io"

	t.Run("returns both versions when both are served", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha2", Served: true},
					{Name: "v1", Served: true},
				},
			},
		})

		result := GetServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.True(t, result.Exists)
		require.True(t, result.Served["v1"])
		require.True(t, result.Served["v1alpha2"])
	})

	t.Run("tracks missing served version on existing CRD", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha3", Served: true},
				},
			},
		})

		result := GetServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.True(t, result.Exists)
		require.False(t, result.Served["v1"])
		require.False(t, result.Served["v1alpha2"])
	})

	t.Run("records CRD absence authoritatively", func(t *testing.T) {
		client := apiextensionsfake.NewClientset()

		result := GetServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.False(t, result.Exists)
		require.False(t, result.Served["v1"])
		require.False(t, result.Served["v1alpha2"])
	})

	t.Run("returns non-authoritative when client is nil", func(t *testing.T) {
		result := GetServedVersions(nil, crdName, "v1", "v1alpha2")
		require.False(t, result.Authoritative)
		require.False(t, result.Exists)
		require.False(t, result.Served["v1"])
		require.False(t, result.Served["v1alpha2"])
	})
}
