//go:build e2e

package xds_warming

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	testdefaults "github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	"github.com/kgateway-dev/kgateway/v2/test/envoyutils/admincli"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
	"github.com/kgateway-dev/kgateway/v2/test/gomega/transforms"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

var (
	setupManifest               = filepath.Join(fsutils.MustGetThisDir(), "testdata", "setup.yaml")
	routeNewManifest            = filepath.Join(fsutils.MustGetThisDir(), "testdata", "route-new.yaml")
	backendNewManifest          = filepath.Join(fsutils.MustGetThisDir(), "testdata", "backend-new.yaml")
	startupServiceRouteManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "startup-service-route.yaml")
	startupBackendManifest      = filepath.Join(fsutils.MustGetThisDir(), "testdata", "startup-backend.yaml")

	setup = base.TestCase{
		Manifests: []string{
			testdefaults.CurlPodManifest,
			setupManifest,
		},
	}

	testCases = map[string]*base.TestCase{
		"TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic": {},
		"TestInitialRouteWaitsForEDSBeforeBecomingActive":       {},
	}
)

const (
	gatewayNamespace   = "kgateway-base"
	gatewayName        = "gateway"
	routeName          = "xds-warming"
	hostName           = "xds-warming.example.com"
	oldBody            = "xds-warming-old"
	newBody            = "xds-warming-new"
	oldClusterName     = "kube_kgateway-base_warming-old_8080"
	newClusterName     = "kube_kgateway-base_warming-new_8080"
	startupRouteName   = "xds-warming-startup"
	startupHostName    = "xds-warming-startup.example.com"
	startupBody        = "xds-warming-startup"
	startupClusterName = "kube_kgateway-base_warming-startup_8080"
)

var proxyObjectMeta = metav1.ObjectMeta{
	Name:      gatewayName,
	Namespace: gatewayNamespace,
}

type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		BaseTestingSuite: base.NewBaseTestingSuite(ctx, testInst, setup, testCases),
	}
}

func (s *testingSuite) TearDownSuite() {
	if testutils.ShouldSkipCleanup(s.T()) {
		return
	}
	s.BaseTestingSuite.TearDownSuite()
}

func (s *testingSuite) AfterTest(suiteName, testName string) {
	s.BaseTestingSuite.AfterTest(suiteName, testName)

	if testutils.ShouldSkipCleanup(s.T()) {
		return
	}

	for _, manifest := range []string{
		backendNewManifest,
		startupBackendManifest,
		startupServiceRouteManifest,
	} {
		err := s.TestInstallation.Actions.Kubectl().DeleteFileSafe(s.Ctx, manifest)
		s.Require().NoError(err, "can clean up %s", manifest)
	}

	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, setupManifest)
	s.Require().NoError(err, "can restore baseline warming route")
}

func (s *testingSuite) TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic() {
	s.TestInstallation.AssertionsT(s.T()).EventuallyHTTPRouteCondition(
		s.Ctx,
		routeName,
		gatewayNamespace,
		gwv1.RouteConditionAccepted,
		metav1.ConditionTrue,
	)
	s.assertGatewayEventuallyServes(hostName, oldBody)
	s.assertActiveClusterPresence(oldClusterName, true)

	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, routeNewManifest)
	s.Require().NoError(err, "can retarget route to new service before endpoints exist")
	s.eventuallyRouteObserved(routeName, gatewayNamespace)

	s.assertActiveClusterPresence(oldClusterName, true)
	s.assertGatewayServesConsistently(hostName, oldBody, 10*time.Second, 500*time.Millisecond)

	err = s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, backendNewManifest)
	s.Require().NoError(err, "can create delayed new backend deployment")
	s.TestInstallation.AssertionsT(s.T()).EventuallyObjectsExist(
		s.Ctx,
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "warming-new", Namespace: gatewayNamespace}},
	)
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodsRunning(
		s.Ctx,
		gatewayNamespace,
		metav1.ListOptions{LabelSelector: testdefaults.WellKnownAppLabel + "=warming-new"},
		time.Minute,
		500*time.Millisecond,
	)

	s.assertActiveClusterPresence(newClusterName, true)
	s.assertGatewayEventuallyServes(hostName, newBody)
	s.assertGatewayServesConsistently(hostName, newBody, 5*time.Second, time.Second)
}

func (s *testingSuite) TestInitialRouteWaitsForEDSBeforeBecomingActive() {
	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, startupServiceRouteManifest)
	s.Require().NoError(err, "can publish startup route and service before endpoints exist")
	s.eventuallyRouteObserved(startupRouteName, gatewayNamespace)

	s.assertGatewayStatusConsistently(startupHostName, http.StatusNotFound, 10*time.Second, 500*time.Millisecond)

	err = s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, startupBackendManifest)
	s.Require().NoError(err, "can create delayed startup backend deployment")
	s.TestInstallation.AssertionsT(s.T()).EventuallyObjectsExist(
		s.Ctx,
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "warming-startup", Namespace: gatewayNamespace}},
	)
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodsRunning(
		s.Ctx,
		gatewayNamespace,
		metav1.ListOptions{LabelSelector: testdefaults.WellKnownAppLabel + "=warming-startup"},
		time.Minute,
		500*time.Millisecond,
	)

	s.assertActiveClusterPresence(startupClusterName, true)
	s.assertGatewayEventuallyServes(startupHostName, startupBody)
	s.assertGatewayServesConsistently(startupHostName, startupBody, 5*time.Second, time.Second)
}

func (s *testingSuite) eventuallyRouteObserved(routeName, routeNamespace string) {
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		route := &gwv1.HTTPRoute{}
		err := s.TestInstallation.ClusterContext.Client.Get(
			s.Ctx,
			types.NamespacedName{Name: routeName, Namespace: routeNamespace},
			route,
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "can get HTTPRoute %s/%s", routeNamespace, routeName)

		generation := route.GetGeneration()
		g.Expect(route.Status.Parents).NotTo(gomega.BeEmpty(), "HTTPRoute should have parent status")
		for _, parent := range route.Status.Parents {
			accepted := apimeta.FindStatusCondition(parent.Conditions, string(gwv1.RouteConditionAccepted))
			if accepted != nil && accepted.Status == metav1.ConditionTrue && accepted.ObservedGeneration >= generation {
				return
			}
		}
		g.Expect(false).To(gomega.BeTrue(), "HTTPRoute %s/%s has not observed generation %d; status: %+v",
			routeNamespace, routeName, generation, route.Status)
	}).WithContext(s.Ctx).WithTimeout(time.Minute).WithPolling(500 * time.Millisecond).Should(gomega.Succeed())
}

func (s *testingSuite) assertActiveClusterPresence(clusterName string, shouldExist bool) {
	s.TestInstallation.AssertionsT(s.T()).AssertEnvoyAdminApi(s.Ctx, proxyObjectMeta, func(ctx context.Context, adminClient *admincli.Client) {
		s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
			clusters, err := adminClient.GetDynamicClusters(ctx)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "can get dynamic active clusters")
			_, ok := clusters[clusterName]
			g.Expect(ok).To(gomega.Equal(shouldExist), "cluster %s active presence should be %t", clusterName, shouldExist)
		}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(time.Second).Should(gomega.Succeed())
	})
}

func (s *testingSuite) assertGatewayEventuallyServes(host, bodySubstring string) {
	s.TestInstallation.AssertionsT(s.T()).AssertEventualCurlResponse(
		s.Ctx,
		testdefaults.CurlPodExecOpt,
		s.gatewayCurlOptions(host),
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
			Body:       gomega.ContainSubstring(bodySubstring),
		},
	)
}

func (s *testingSuite) assertGatewayServesConsistently(host, bodySubstring string, window, poll time.Duration) {
	s.assertGatewayServesOnce(host, bodySubstring)

	s.TestInstallation.AssertionsT(s.T()).Gomega.Consistently(func(g gomega.Gomega) {
		statusCode, body, err := s.gatewayResponse(host)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "gateway request should complete")
		g.Expect(statusCode).To(gomega.Equal(http.StatusOK), "gateway returned body: %s", body)
		g.Expect(body).To(gomega.ContainSubstring(bodySubstring))
	}).WithContext(s.Ctx).WithTimeout(window).WithPolling(poll).Should(gomega.Succeed())
}

func (s *testingSuite) assertGatewayServesOnce(host, bodySubstring string) {
	statusCode, body, err := s.gatewayResponse(host)
	s.Require().NoError(err)
	s.Require().Equal(http.StatusOK, statusCode, "gateway returned body: %s", body)
	s.Require().Contains(body, bodySubstring)
}

func (s *testingSuite) assertGatewayStatusConsistently(host string, status int, window, poll time.Duration) {
	s.assertGatewayStatusOnce(host, status)

	s.TestInstallation.AssertionsT(s.T()).Gomega.Consistently(func(g gomega.Gomega) {
		statusCode, body, err := s.gatewayResponse(host)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "gateway request should complete")
		g.Expect(statusCode).To(gomega.Equal(status), "gateway returned body: %s", body)
	}).WithContext(s.Ctx).WithTimeout(window).WithPolling(poll).Should(gomega.Succeed())
}

func (s *testingSuite) assertGatewayStatusOnce(host string, status int) {
	statusCode, body, err := s.gatewayResponse(host)
	s.Require().NoError(err)
	s.Require().Equal(status, statusCode, "gateway returned body: %s", body)
}

func (s *testingSuite) gatewayResponse(host string) (int, string, error) {
	curlResponse, err := s.TestInstallation.ClusterContext.Cli.CurlFromPod(
		s.Ctx,
		testdefaults.CurlPodExecOpt,
		s.gatewayCurlOptions(host)...,
	)
	if err != nil {
		return 0, "", err
	}

	response := transforms.WithCurlResponse(curlResponse)
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, "", fmt.Errorf("read gateway response body: %w", err)
	}
	return response.StatusCode, string(body), nil
}

func (s *testingSuite) gatewayCurlOptions(host string) []curl.Option {
	return []curl.Option{
		curl.WithHost(kubeutils.ServiceFQDN(proxyObjectMeta)),
		curl.WithHostHeader(host),
		curl.WithPort(80),
		curl.WithPath("/"),
		curl.WithConnectionTimeout(2),
	}
}
