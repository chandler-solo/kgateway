package collections

import (
	"testing"

	"github.com/stretchr/testify/require"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestIsCRDVersionServed(t *testing.T) {
	tcpRouteGVR := schema.GroupVersionResource{
		Group:    "gateway.networking.k8s.io",
		Version:  "v1alpha2",
		Resource: "tcproutes",
	}

	t.Run("returns true when version is served", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "tcproutes.gateway.networking.k8s.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha2", Served: true},
				},
			},
		})
		require.True(t, isCRDVersionServed(client, tcpRouteGVR))
	})

	t.Run("returns true when CRD does not exist to let delayed informer handle it", func(t *testing.T) {
		client := apiextensionsfake.NewClientset()
		require.True(t, isCRDVersionServed(client, tcpRouteGVR))
	})

	t.Run("returns false when CRD exists but version is not served", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "tcproutes.gateway.networking.k8s.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1alpha2", Served: false},
				},
			},
		})
		require.False(t, isCRDVersionServed(client, tcpRouteGVR))
	})

	t.Run("returns false when CRD exists but requested version is absent", func(t *testing.T) {
		client := apiextensionsfake.NewClientset(&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{Name: "tcproutes.gateway.networking.k8s.io"},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{Name: "v1", Served: true},
				},
			},
		})
		require.False(t, isCRDVersionServed(client, tcpRouteGVR))
	})

	t.Run("returns true when extClient is nil", func(t *testing.T) {
		require.True(t, isCRDVersionServed(nil, tcpRouteGVR))
	})
}
