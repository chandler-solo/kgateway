package collections

import (
	"context"
	"log/slog"

	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ServedVersions captures the startup-time served versions for a CRD.
type ServedVersions struct {
	Served        map[string]bool
	Exists        bool
	Authoritative bool
}

func GetServedVersions(extClient apiextensionsclient.Interface, crdName string, versions ...string) ServedVersions {
	result := ServedVersions{
		Served: make(map[string]bool, len(versions)),
	}
	if extClient == nil {
		slog.Warn("apiextensions client unavailable during CRD version discovery",
			"crd", crdName,
		)
		return result
	}

	crd, err := extClient.ApiextensionsV1().CustomResourceDefinitions().Get(
		context.Background(),
		crdName,
		metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			result.Authoritative = true
			return result
		}

		slog.Warn("failed to discover served CRD versions",
			"crd", crdName,
			"error", err,
		)
		return result
	}

	result.Exists = true
	result.Authoritative = true
	for _, version := range crd.Spec.Versions {
		if !version.Served {
			continue
		}
		for _, requested := range versions {
			if version.Name == requested {
				result.Served[requested] = true
			}
		}
	}

	for _, requested := range versions {
		if !result.Served[requested] {
			slog.Warn("CRD exists but requested version is not served, skipping informer",
				"crd", crdName,
				"requestedVersion", requested,
			)
		}
	}

	return result
}
