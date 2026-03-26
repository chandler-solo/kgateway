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

		result := getServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.True(t, result.Served["v1"])
		require.True(t, result.Served["v1alpha2"])
	})

	t.Run("returns only promoted when legacy is not served", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1", Served: true},
					{Name: "v1alpha2", Served: false},
				},
			},
		})

		result := getServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.True(t, result.Served["v1"])
		require.False(t, result.Served["v1alpha2"])
	})

	t.Run("returns only legacy when promoted is absent", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha2", Served: true},
				},
			},
		})

		result := getServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.False(t, result.Served["v1"])
		require.True(t, result.Served["v1alpha2"])
	})

	t.Run("optimistic when CRD does not exist", func(t *testing.T) {
		client := apiextensionsfake.NewClientset()

		result := getServedVersions(client, crdName, "v1", "v1alpha2")
		require.False(t, result.Authoritative)
		require.True(t, result.Served["v1"])
		require.True(t, result.Served["v1alpha2"])
	})

	t.Run("optimistic when client is nil", func(t *testing.T) {
		result := getServedVersions(nil, crdName, "v1", "v1alpha2")
		require.False(t, result.Authoritative)
		require.True(t, result.Served["v1"])
		require.True(t, result.Served["v1alpha2"])
	})

	t.Run("single version check", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "tcproutes.gateway.networking.k8s.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha2", Served: true},
				},
			},
		})

		result := getServedVersions(client, "tcproutes.gateway.networking.k8s.io", "v1alpha2")
		require.True(t, result.Authoritative)
		require.True(t, result.Served["v1alpha2"])
	})

	t.Run("CRD exists but no requested versions served", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: crdName},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1beta1", Served: true},
				},
			},
		})

		result := getServedVersions(client, crdName, "v1", "v1alpha2")
		require.True(t, result.Authoritative)
		require.False(t, result.Served["v1"])
		require.False(t, result.Served["v1alpha2"])
	})
}
