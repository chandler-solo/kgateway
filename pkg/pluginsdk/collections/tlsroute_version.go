package collections

import (
	"context"
	"log/slog"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	"github.com/kgateway-dev/kgateway/v2/pkg/kgateway/wellknown"
)

var promotedTLSRouteGVR = schema.GroupVersionResource{
	Group:    wellknown.GatewayGroup,
	Version:  gwv1.GroupVersion.Version,
	Resource: "tlsroutes",
}

var tlsRouteV1Alpha3GVR = schema.GroupVersionResource{
	Group:    wellknown.GatewayGroup,
	Version:  wellknown.TLSRouteV1Alpha3Version,
	Resource: "tlsroutes",
}

var legacyTLSRouteV1Alpha2GVR = schema.GroupVersionResource{
	Group:    wellknown.GatewayGroup,
	Version:  gwv1a2.GroupVersion.Version,
	Resource: "tlsroutes",
}

type servedTLSRouteVersions struct {
	Promoted      bool
	Legacy        bool
	LegacyGVR     schema.GroupVersionResource
	Authoritative bool
}

func fallbackTLSRouteVersions() servedTLSRouteVersions {
	return servedTLSRouteVersions{
		Promoted:  true,
		Legacy:    true,
		LegacyGVR: tlsRouteV1Alpha3GVR,
	}
}

// legacyTLSRouteWatchGVRs returns the legacy TLSRoute API versions that should
// be watched for the current discovery result. When discovery is authoritative,
// prefer a single served version to avoid duplicate logical TLSRoutes. When
// discovery is non-authoritative, keep both legacy versions active so clusters
// that only serve v1alpha2 remain discoverable.
func legacyTLSRouteWatchGVRs(versions servedTLSRouteVersions) []schema.GroupVersionResource {
	if !versions.Legacy || (versions.Authoritative && versions.Promoted) {
		return nil
	}
	if versions.Authoritative {
		return []schema.GroupVersionResource{versions.LegacyGVR}
	}
	return []schema.GroupVersionResource{tlsRouteV1Alpha3GVR, legacyTLSRouteV1Alpha2GVR}
}

// getServedTLSRouteVersions resolves which TLSRoute API versions are currently
// served by the cluster. When discovery is unavailable, or the CRD is not yet
// installed, we conservatively allow both promoted and legacy watches so
// startup does not incorrectly disable TLSRoute support before delayed
// informers can recover.
func getServedTLSRouteVersions(extClient apiextensionsclient.Interface) servedTLSRouteVersions {
	if extClient == nil {
		return fallbackTLSRouteVersions()
	}

	ctx, cancel := context.WithTimeout(context.Background(), crdLookupTimeout)
	defer cancel()

	crd, err := extClient.ApiextensionsV1().CustomResourceDefinitions().Get(
		ctx,
		"tlsroutes.gateway.networking.k8s.io",
		metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fallbackTLSRouteVersions()
		}
		return fallbackTLSRouteVersions()
	}

	versions := servedTLSRouteVersions{Authoritative: true}
	servedLegacyVersions := map[string]bool{}
	for _, version := range crd.Spec.Versions {
		if !version.Served {
			continue
		}

		switch version.Name {
		case gwv1.GroupVersion.Version:
			versions.Promoted = true
		case wellknown.TLSRouteV1Alpha3Version, gwv1a2.GroupVersion.Version:
			servedLegacyVersions[version.Name] = true
		}
	}

	for _, legacyVersion := range []string{wellknown.TLSRouteV1Alpha3Version, gwv1a2.GroupVersion.Version} {
		if servedLegacyVersions[legacyVersion] {
			versions.Legacy = true
			versions.LegacyGVR = schema.GroupVersionResource{
				Group:    wellknown.GatewayGroup,
				Version:  legacyVersion,
				Resource: "tlsroutes",
			}
			break
		}
	}

	return versions
}

func convertUnstructuredTLSRouteToV1Alpha2(in *unstructured.Unstructured) *gwv1a2.TLSRoute {
	if in == nil {
		return nil
	}

	out := &gwv1a2.TLSRoute{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(in.Object, out); err != nil {
		slog.Warn("ignoring unstructured TLSRoute with invalid payload",
			"name", in.GetName(),
			"namespace", in.GetNamespace(),
			"error", err,
		)
		return nil
	}
	out.SetGroupVersionKind(wellknown.TLSRouteGVK)
	return out
}

func ConvertUnstructuredTLSRouteToV1Alpha2ForStatus(in *unstructured.Unstructured) *gwv1a2.TLSRoute {
	return convertUnstructuredTLSRouteToV1Alpha2(in)
}
