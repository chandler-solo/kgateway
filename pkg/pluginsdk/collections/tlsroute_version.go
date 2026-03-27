package collections

import (
	"log/slog"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

var promotedTLSRouteGVR = schema.GroupVersionResource{
	Group:    wellknown.GatewayGroup,
	Version:  "v1",
	Resource: "tlsroutes",
}

var promotedTLSRouteGVK = schema.GroupVersionKind{
	Group:   wellknown.GatewayGroup,
	Version: "v1",
	Kind:    wellknown.TLSRouteKind,
}

var PromotedTLSRouteGVR = promotedTLSRouteGVR

var PromotedTLSRouteGVK = promotedTLSRouteGVK

func ConvertUnstructuredTLSRouteToV1Alpha2(in *unstructured.Unstructured) *gwv1a2.TLSRoute {
	if in == nil {
		return nil
	}

	out := &gwv1a2.TLSRoute{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(in.Object, out); err != nil {
		slog.Warn("ignoring TLSRoute with invalid payload",
			"name", in.GetName(),
			"namespace", in.GetNamespace(),
			"apiVersion", in.GetAPIVersion(),
			"error", err,
		)
		return nil
	}
	if out.GroupVersionKind().Empty() {
		out.SetGroupVersionKind(wellknown.TLSRouteGVK)
	}
	return out
}

func BuildUnstructuredTLSRouteStatus(status gwv1a2.TLSRouteStatus) (any, error) {
	obj := &gwv1a2.TLSRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: wellknown.TLSRouteGVK.GroupVersion().String(),
			Kind:       wellknown.TLSRouteKind,
		},
		Status: status,
	}
	unstructuredObj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return unstructuredObj["status"], nil
}
