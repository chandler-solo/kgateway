package collections

import (
	"context"
	"log/slog"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// servedVersions holds the result of querying a CRD for which API versions are
// currently served.
type servedVersions struct {
	// Served maps each requested version name to whether it is served.
	Served map[string]bool
	// Exists is true when the CRD object itself exists.
	Exists bool
	// Authoritative is true when the CRD was successfully looked up, meaning
	// the Served map reflects the actual cluster state.
	Authoritative bool
}

// getServedVersions queries the apiextensions client for the named CRD and
// returns which of the requested versions are served.
func getServedVersions(extClient apiextensionsclient.Interface, crdName string, versions ...string) servedVersions {
	if extClient == nil {
		slog.Warn("apiextensions client unavailable during CRD version discovery",
			"crd", crdName,
		)
		return servedVersions{Served: make(map[string]bool, len(versions))}
	}

	crd, err := extClient.ApiextensionsV1().CustomResourceDefinitions().Get(
		context.Background(), crdName, metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return servedVersions{
				Served:        make(map[string]bool, len(versions)),
				Authoritative: true,
			}
		}
		slog.Warn("failed to discover served CRD versions",
			"crd", crdName,
			"error", err,
		)
		return servedVersions{Served: make(map[string]bool, len(versions))}
	}

	result := servedVersions{
		Served:        make(map[string]bool, len(versions)),
		Exists:        true,
		Authoritative: true,
	}
	for _, v := range crd.Spec.Versions {
		if !v.Served {
			continue
		}
		for _, req := range versions {
			if v.Name == req {
				result.Served[req] = true
			}
		}
	}

	for _, req := range versions {
		if !result.Served[req] {
			slog.Warn("CRD exists but requested version is not served, skipping informer",
				"crd", crdName,
				"requestedVersion", req,
			)
		}
	}

	return result
}
