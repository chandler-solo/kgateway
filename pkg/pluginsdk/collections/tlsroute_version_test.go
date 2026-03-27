package collections

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

func TestConvertUnstructuredTLSRouteToV1Alpha2(t *testing.T) {
	route := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": promotedTLSRouteGVK.GroupVersion().String(),
			"kind":       wellknown.TLSRouteKind,
			"metadata": map[string]any{
				"name":      "example",
				"namespace": "default",
			},
			"spec": map[string]any{
				"hostnames": []any{"example.com"},
				"parentRefs": []any{
					map[string]any{"name": "gw"},
				},
			},
		},
	}

	converted := ConvertUnstructuredTLSRouteToV1Alpha2(route)
	require.NotNil(t, converted)
	require.Equal(t, "example", converted.Name)
	require.Equal(t, "default", converted.Namespace)
	require.Equal(t, wellknown.TLSRouteKind, converted.Kind)
	require.Len(t, converted.Spec.Hostnames, 1)
	require.Equal(t, "example.com", string(converted.Spec.Hostnames[0]))
	require.Len(t, converted.Spec.ParentRefs, 1)
	require.Equal(t, "gw", string(converted.Spec.ParentRefs[0].Name))
}
