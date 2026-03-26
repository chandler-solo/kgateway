package collections

import (
	"context"
	"log/slog"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// isCRDVersionServed checks whether the given GVR has an installed CRD with the
// requested version being served. When discovery is unavailable (nil client or
// lookup error), we optimistically return true and let the delayed informer's CRD
// watcher handle the missing-CRD case at runtime. We only return false when we can
// authoritatively confirm the CRD exists but the requested version is not served.
func isCRDVersionServed(extClient apiextensionsclient.Interface, gvr schema.GroupVersionResource) bool {
	if extClient == nil {
		return true
	}

	crdName := gvr.Resource + "." + gvr.Group
	crd, err := extClient.ApiextensionsV1().CustomResourceDefinitions().Get(context.Background(), crdName, metav1.GetOptions{})
	if err != nil {
		// CRD lookup failed (not found or other error). Return true to let the
		// delayed informer handle it — the CRD watcher will detect the missing
		// CRD and return empty results.
		return true
	}

	// CRD exists — check if the requested version is actually served.
	for _, version := range crd.Spec.Versions {
		if version.Name == gvr.Version && version.Served {
			return true
		}
	}

	slog.Warn("CRD exists but requested version is not served, skipping informer",
		"crd", crdName,
		"requestedVersion", gvr.Version,
	)
	return false
}
