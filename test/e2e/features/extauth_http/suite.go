//go:build e2e

package extauth_http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	testdefaults "github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

// testingSuite is a suite of tests for HTTP ExtAuth functionality
type testingSuite struct {
	suite.Suite

	ctx context.Context

	// testInstallation contains all the metadata/utilities necessary to execute a series of tests
	// against an installation of kgateway
	testInstallation *e2e.TestInstallation

	// manifests shared by all tests
	commonManifests []string
	// resources from manifests shared by all tests
	commonResources []client.Object
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		ctx:              ctx,
		testInstallation: testInst,
	}
}

func (s *testingSuite) SetupSuite() {
	s.commonManifests = []string{
		testdefaults.CurlPodManifest,
		serviceManifest,
		commonManifest,
		httpAuthManifest,
		httpExtAuthManifest,
	}
	s.commonResources = []client.Object{
		// resources from curl manifest
		testdefaults.CurlPod,
		// resources from service manifest
		httpbinNs, httpbinSvc, httpbinDeployment,
		// resources from common manifest
		httpbinRoute,
		// deployer-generated resources
		gatewayDeployment, gatewayService,
		// http auth resources
		httpAuthSvc, httpAuthDeployment, httpExtAuthExtension,
	}

	// set up common resources once
	for _, manifest := range s.commonManifests {
		err := s.testInstallation.Actions.Kubectl().ApplyFile(s.ctx, manifest)
		s.Require().NoError(err, "can apply "+manifest)
	}
	s.testInstallation.Assertions.EventuallyObjectsExist(s.ctx, s.commonResources...)

	// make sure pods are running
	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, testdefaults.CurlPod.GetNamespace(), metav1.ListOptions{
		LabelSelector: testdefaults.CurlPodLabelSelector,
	})

	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, gatewayObjMeta.GetNamespace(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", testdefaults.WellKnownAppLabel, gatewayObjMeta.GetName()),
	}, time.Minute*2)

	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, httpAuthSvc.GetNamespace(), metav1.ListOptions{
		LabelSelector: "app=http-auth-server",
	})

	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, httpbinNs.GetName(), metav1.ListOptions{
		LabelSelector: "app=httpbin",
	})
}

func (s *testingSuite) TearDownSuite() {
	if testutils.ShouldSkipCleanup(s.T()) {
		return
	}
	// clean up common resources
	for _, manifest := range s.commonManifests {
		err := s.testInstallation.Actions.Kubectl().DeleteFileSafe(s.ctx, manifest)
		s.Require().NoError(err, "can delete "+manifest)
	}
	s.testInstallation.Assertions.EventuallyObjectsNotExist(s.ctx, s.commonResources...)

	s.testInstallation.Assertions.EventuallyPodsNotExist(s.ctx, gatewayObjMeta.GetNamespace(), metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", testdefaults.WellKnownAppLabel, gatewayObjMeta.GetName()),
	})
}

// TestHttpExtAuthBasic tests basic HTTP ExtAuth functionality with header-based allow/deny
func (s *testingSuite) TestHttpExtAuthBasic() {
	manifests := []string{
		gatewayPolicyManifest,
	}

	resources := []client.Object{
		gatewayTrafficPolicy,
	}
	testutils.Cleanup(s.T(), func() {
		for _, manifest := range manifests {
			err := s.testInstallation.Actions.Kubectl().DeleteFileSafe(s.ctx, manifest)
			s.Require().NoError(err)
		}
		s.testInstallation.Assertions.EventuallyObjectsNotExist(s.ctx, resources...)
	})

	// Apply gateway-level HTTP extauth policy
	for _, manifest := range manifests {
		err := s.testInstallation.Actions.Kubectl().ApplyFile(s.ctx, manifest)
		s.Require().NoError(err, "can apply "+manifest)
	}
	s.testInstallation.Assertions.EventuallyObjectsExist(s.ctx, resources...)

	// Wait for pods to be running
	s.ensurePodsRunning()

	testCases := []struct {
		name           string
		headers        map[string]string
		hostname       string
		expectedStatus int
		bodyContains   string
	}{
		{
			name: "request allowed with x-auth-token: allow header",
			headers: map[string]string{
				"x-auth-token": "allow",
			},
			hostname:       "www.example.com",
			expectedStatus: http.StatusOK,
			bodyContains:   "", // just check status
		},
		{
			name:           "request denied without auth header",
			headers:        map[string]string{},
			hostname:       "www.example.com",
			expectedStatus: http.StatusForbidden,
			bodyContains:   "Access Denied",
		},
		{
			name: "request denied with invalid auth header",
			hostname: "www.example.com",
			headers: map[string]string{
				"x-auth-token": "deny",
			},
			expectedStatus: http.StatusForbidden,
			bodyContains:   "Access Denied",
		},
	}

	for _, tc := range testCases {
		s.Run(tc.name, func() {
			// Build curl options
			opts := []curl.Option{
				curl.WithHost(kubeutils.ServiceFQDN(gatewayObjMeta)),
				curl.WithHostHeader(tc.hostname),
				curl.WithPort(8080),
			}

			// Add test-specific headers
			for k, v := range tc.headers {
				opts = append(opts, curl.WithHeader(k, v))
			}

			// Test the request
			s.testInstallation.Assertions.AssertEventualCurlResponse(
				s.ctx,
				testdefaults.CurlPodExecOpt,
				opts,
				&testmatchers.HttpResponse{
					StatusCode: tc.expectedStatus,
					Body:       gomega.ContainSubstring(tc.bodyContains),
				})
		})
	}
}

// TestHttpExtAuthWithUpstreamHeaders tests that headers from auth response are forwarded to upstream
func (s *testingSuite) TestHttpExtAuthWithUpstreamHeaders() {
	manifests := []string{
		gatewayPolicyManifest,
	}

	resources := []client.Object{
		gatewayTrafficPolicy,
	}
	testutils.Cleanup(s.T(), func() {
		for _, manifest := range manifests {
			err := s.testInstallation.Actions.Kubectl().DeleteFileSafe(s.ctx, manifest)
			s.Require().NoError(err)
		}
		s.testInstallation.Assertions.EventuallyObjectsNotExist(s.ctx, resources...)
	})

	// Apply gateway-level HTTP extauth policy
	for _, manifest := range manifests {
		err := s.testInstallation.Actions.Kubectl().ApplyFile(s.ctx, manifest)
		s.Require().NoError(err, "can apply "+manifest)
	}
	s.testInstallation.Assertions.EventuallyObjectsExist(s.ctx, resources...)

	// Wait for pods to be running
	s.ensurePodsRunning()

	s.Run("authorized request forwards auth headers to upstream", func() {
		opts := []curl.Option{
			curl.WithHost(kubeutils.ServiceFQDN(gatewayObjMeta)),
			curl.WithHostHeader("www.example.com"),
			curl.WithPort(8080),
			curl.WithPath("/headers"),
			curl.WithHeader("x-auth-token", "allow"),
		}

		// Test that the request succeeds and includes forwarded headers
		s.testInstallation.Assertions.AssertEventualCurlResponse(
			s.ctx,
			testdefaults.CurlPodExecOpt,
			opts,
			&testmatchers.HttpResponse{
				StatusCode: http.StatusOK,
				// httpbin /headers endpoint returns all headers it received
				// The auth server adds x-auth-user and x-auth-roles which should be forwarded
				Body: gomega.And(
					gomega.ContainSubstring("X-Auth-User"),
					gomega.ContainSubstring("test-user"),
					gomega.ContainSubstring("X-Auth-Roles"),
					gomega.ContainSubstring("admin,user"),
				),
			})
	})
}

// TestHttpExtAuthRouteDisable tests that route-level disable works
func (s *testingSuite) TestHttpExtAuthRouteDisable() {
	manifests := []string{
		gatewayPolicyManifest,
		routeDisablePolicyManifest,
	}

	resources := []client.Object{
		gatewayTrafficPolicy,
		routeDisablePolicy,
	}
	testutils.Cleanup(s.T(), func() {
		for _, manifest := range manifests {
			err := s.testInstallation.Actions.Kubectl().DeleteFileSafe(s.ctx, manifest)
			s.Require().NoError(err)
		}
		s.testInstallation.Assertions.EventuallyObjectsNotExist(s.ctx, resources...)
	})

	// Apply both gateway-level and route-level policies
	for _, manifest := range manifests {
		err := s.testInstallation.Actions.Kubectl().ApplyFile(s.ctx, manifest)
		s.Require().NoError(err, "can apply "+manifest)
	}
	s.testInstallation.Assertions.EventuallyObjectsExist(s.ctx, resources...)

	// Wait for pods to be running
	s.ensurePodsRunning()

	s.Run("request allowed without auth header when route disables extauth", func() {
		opts := []curl.Option{
			curl.WithHost(kubeutils.ServiceFQDN(gatewayObjMeta)),
			curl.WithHostHeader("www.example.com"),
			curl.WithPort(8080),
		}

		// Request should succeed without auth header due to route-level disable
		s.testInstallation.Assertions.AssertEventualCurlResponse(
			s.ctx,
			testdefaults.CurlPodExecOpt,
			opts,
			&testmatchers.HttpResponse{
				StatusCode: http.StatusOK,
			})
	})
}

func (s *testingSuite) ensurePodsRunning() {
	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, testdefaults.CurlPod.GetNamespace(), metav1.ListOptions{
		LabelSelector: testdefaults.WellKnownAppLabel + "=curl",
	})
	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, gatewayObjMeta.GetNamespace(), metav1.ListOptions{
		LabelSelector: testdefaults.WellKnownAppLabel + "=" + gatewayObjMeta.GetName(),
	}, time.Minute)
	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, httpAuthSvc.GetNamespace(), metav1.ListOptions{
		LabelSelector: "app=http-auth-server",
	})
	s.testInstallation.Assertions.EventuallyPodsRunning(s.ctx, httpbinNs.GetName(), metav1.ListOptions{
		LabelSelector: "app=httpbin",
	})
}
