package collections

import (
	"context"
	"log/slog"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// servedVersions holds the result of querying a CRD for which API versions are
// currently served.
type servedVersions struct {
	// Served maps each requested version name to whether it is served.
	Served map[string]bool
	// Authoritative is true when the CRD was successfully looked up, meaning
	// the Served map reflects the actual cluster state. When false (nil client
	// or lookup error), the Served map optimistically marks all requested
	// versions as true so the delayed informer's CRD watcher can handle the
	// missing-CRD case at runtime.
	Authoritative bool
}

// getServedVersions queries the apiextensions client for the named CRD and
// returns which of the requested versions are served.
func getServedVersions(extClient apiextensionsclient.Interface, crdName string, versions ...string) servedVersions {
	if extClient == nil {
		return optimistic(versions)
	}

	crd, err := extClient.ApiextensionsV1().CustomResourceDefinitions().Get(
		context.Background(), crdName, metav1.GetOptions{},
	)
	if err != nil {
		return optimistic(versions)
	}

	result := servedVersions{
		Served:        make(map[string]bool, len(versions)),
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

// optimistic returns a servedVersions where every requested version is assumed
// to be served. Used when discovery is unavailable.
func optimistic(versions []string) servedVersions {
	m := make(map[string]bool, len(versions))
	for _, v := range versions {
		m[v] = true
	}
	return servedVersions{Served: m, Authoritative: false}
}
