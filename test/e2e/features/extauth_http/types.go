// go:build e2e

package extauth_http

import (
	"path/filepath"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
)

var (
	// httpbin service
	httpbinNs = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "httpbin",
		},
	}

	httpbinSvc = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "httpbin",
			Namespace: "httpbin",
		},
	}

	httpbinDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "httpbin",
			Namespace: "httpbin",
		},
	}

	// Gateway
	gatewayObjMeta = metav1.ObjectMeta{
		Name:      "http-extauth-gateway",
		Namespace: "kgateway-test",
	}
	gatewayDeployment = &appsv1.Deployment{ObjectMeta: gatewayObjMeta}
	gatewayService    = &corev1.Service{ObjectMeta: gatewayObjMeta}

	// HTTP Auth Server
	httpAuthSvc = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "http-auth-server",
			Namespace: "kgateway-test",
		},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{
				{
					Name: "http",
					Port: 8080,
				},
			},
			Selector: map[string]string{
				defaults.WellKnownAppLabel: "http-auth-server",
			},
		},
	}

	httpAuthDeployment = &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "http-auth-server",
			Namespace: "kgateway-test",
		},
	}

	// HTTP ExtAuth Extension
	httpExtAuthExtension = &v1alpha1.GatewayExtension{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "http-ext-auth",
			Namespace: "kgateway-test",
		},
		Spec: v1alpha1.GatewayExtensionSpec{
			Type: v1alpha1.GatewayExtensionTypeExtAuth,
			ExtAuth: &v1alpha1.ExtAuthProvider{
				HttpService: &v1alpha1.ExtHttpService{
					BackendRef: gwv1.BackendRef{
						BackendObjectReference: gwv1.BackendObjectReference{
							Name: "http-auth-server",
							Port: ptrTo(gwv1.PortNumber(8080)),
						},
					},
					PathPrefix: ptrTo("/verify"),
				},
				FailOpen:      false,
				StatusOnError: 403,
			},
		},
	}

	// HTTPRoute
	httpbinRoute = &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "httpbin-route",
			Namespace: "httpbin",
		},
	}

	// Traffic Policies
	gatewayTrafficPolicy = &v1alpha1.TrafficPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "gateway-http-extauth-policy",
			Namespace: "kgateway-test",
		},
	}

	routeDisablePolicy = &v1alpha1.TrafficPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "route-disable-extauth-policy",
			Namespace: "httpbin",
		},
	}

	// Manifest files
	commonManifest           = getTestFile("common.yaml")
	serviceManifest          = getTestFile("service.yaml")
	httpAuthManifest         = getTestFile("http-auth-server.yaml")
	httpExtAuthManifest      = getTestFile("http-extauth-extension.yaml")
	gatewayPolicyManifest    = getTestFile("gateway-policy.yaml")
	routeDisablePolicyManifest = getTestFile("route-disable-policy.yaml")
)

func getTestFile(filename string) string {
	return filepath.Join(fsutils.MustGetThisDir(), "testdata", filename)
}

func ptrTo[T any](v T) *T {
	return &v
}
