//go:build e2e

package xds_warming

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
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
	routeWeightedManifest       = filepath.Join(fsutils.MustGetThisDir(), "testdata", "route-weighted.yaml")
	backendNewManifest          = filepath.Join(fsutils.MustGetThisDir(), "testdata", "backend-new.yaml")
	startupServiceRouteManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "startup-service-route.yaml")
	startupBackendManifest      = filepath.Join(fsutils.MustGetThisDir(), "testdata", "startup-backend.yaml")
	emptyRouteManifest          = filepath.Join(fsutils.MustGetThisDir(), "testdata", "empty-route.yaml")
	canaryRouteManifest         = filepath.Join(fsutils.MustGetThisDir(), "testdata", "canary-route.yaml")
	postrestartRouteManifest    = filepath.Join(fsutils.MustGetThisDir(), "testdata", "postrestart-route.yaml")

	setup = base.TestCase{
		Manifests: []string{
			testdefaults.CurlPodManifest,
			setupManifest,
		},
	}

	testCases = map[string]*base.TestCase{
		"TestRouteUpdateToEmptyBackendPublishesTruth":          {},
		"TestWeightedRouteToEmptyBackendServesMixedTruth":      {},
		"TestInitialRouteToEmptyBackendServes503UntilReady":    {},
		"TestSteadyStateEmptyBackendSurvivesControllerRestart": {},
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
		emptyRouteManifest,
		canaryRouteManifest,
		postrestartRouteManifest,
	} {
		err := s.TestInstallation.Actions.Kubectl().DeleteFileSafe(s.Ctx, manifest)
		s.Require().NoError(err, "can clean up %s", manifest)
	}

	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, setupManifest)
	s.Require().NoError(err, "can restore baseline warming route")
}

// TestRouteUpdateToEmptyBackendPublishesTruth pins stock-parity presence
// semantics (#14352): retargeting a route to a Service whose EndpointSlice
// exists but is empty publishes the flip as the backend's truth — the route
// answers 503 until endpoints arrive, and it must NOT pin route/listener/
// secret updates behind the empty backend, because "empty forever, on
// purpose" (scale-to-zero, ExternalName shapes) is a production-proven
// steady state.
func (s *testingSuite) TestRouteUpdateToEmptyBackendPublishesTruth() {
	s.TestInstallation.AssertionsT(s.T()).EventuallyHTTPRouteCondition(
		s.Ctx,
		routeName,
		gatewayNamespace,
		gwv1.RouteConditionAccepted,
		metav1.ConditionTrue,
	)
	s.assertGatewayEventuallyServes(hostName, oldBody)
	s.assertActiveClusterIsPresent(oldClusterName)

	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, routeNewManifest)
	s.Require().NoError(err, "can retarget route to new service before endpoints exist")
	s.eventuallyRouteObserved(routeName)

	// The flip publishes: the new cluster reaches the served CDS and the
	// route answers 503 (no healthy upstream) — the truthful state of an
	// endpoint-less backend, not an indefinite hold.
	s.assertActiveClusterIsPresent(newClusterName)
	s.assertGatewayEventuallyStatus(hostName, http.StatusServiceUnavailable, time.Minute, 500*time.Millisecond)

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

	s.assertGatewayEventuallyServes(hostName, newBody)
	s.assertGatewayServesConsistently(hostName, newBody, 5*time.Second, time.Second)
}

// TestWeightedRouteToEmptyBackendServesMixedTruth pins stock-parity presence
// semantics for a weighted split where one target's EndpointSlice exists but
// is empty: the split publishes immediately, so requests weighted to the
// empty target answer 503 while the rest keep serving the old backend —
// accurate per-target degradation instead of an indefinite hold (#14352).
func (s *testingSuite) TestWeightedRouteToEmptyBackendServesMixedTruth() {
	s.TestInstallation.AssertionsT(s.T()).EventuallyHTTPRouteCondition(
		s.Ctx,
		routeName,
		gatewayNamespace,
		gwv1.RouteConditionAccepted,
		metav1.ConditionTrue,
	)
	s.assertGatewayEventuallyServes(hostName, oldBody)
	s.assertActiveClusterIsPresent(oldClusterName)

	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, routeWeightedManifest)
	s.Require().NoError(err, "can update route to weighted old/new backends before new endpoints exist")
	s.eventuallyRouteObserved(routeName)

	// The split publishes: the empty target's cluster reaches the served CDS
	// and its share of requests answers 503 while the old backend's share
	// keeps serving.
	s.assertActiveClusterIsPresent(newClusterName)
	s.assertGatewayEventuallyMixes(hostName, oldBody, http.StatusServiceUnavailable, time.Minute, time.Second)

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

	s.assertActiveClusterIsPresent(newClusterName)
	s.assertGatewayEventuallyServesAll(hostName, []string{oldBody, newBody}, 30*time.Second, time.Second)
}

// TestInitialRouteToEmptyBackendServes503UntilReady pins stock-parity
// presence semantics for a brand-new route whose Service exists with no
// endpoints: the route publishes and answers 503 (the backend's truth) until
// endpoints arrive — it does not stay invisible behind a hold (#14352).
func (s *testingSuite) TestInitialRouteToEmptyBackendServes503UntilReady() {
	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, startupServiceRouteManifest)
	s.Require().NoError(err, "can publish startup route and service before endpoints exist")
	s.eventuallyRouteObserved(startupRouteName)

	s.assertActiveClusterIsPresent(startupClusterName)
	s.assertGatewayEventuallyStatus(startupHostName, http.StatusServiceUnavailable, time.Minute, 500*time.Millisecond)

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

	s.assertActiveClusterIsPresent(startupClusterName)
	s.assertGatewayEventuallyServes(startupHostName, startupBody)
	s.assertGatewayServesConsistently(startupHostName, startupBody, 5*time.Second, time.Second)
}

// TestSteadyStateEmptyBackendSurvivesControllerRestart reproduces #14352:
// a shared gateway that references a backend which legitimately never has
// endpoints (scale-to-zero / ExternalName shape — here, a Service with no
// Deployment). The emptiness is a steady state, not a transient race, so the
// publication path must treat it as truth everywhere:
//
//  1. a new route to the empty backend publishes and answers 503 — it never
//     pins route/listener/secret updates behind the empty backend;
//  2. unrelated route updates keep flowing while the empty reference exists;
//  3. after a CONTROLLER restart, warm proxies resume receiving updates
//     (pre-fix they were withheld indefinitely, freezing all config
//     changes);
//  4. a gateway rollout after the restart brings up a fresh proxy pod that
//     goes Ready (pre-fix it received no snapshot at all and crash-looped at
//     "cm init: initializing cds").
func (s *testingSuite) TestSteadyStateEmptyBackendSurvivesControllerRestart() {
	s.assertGatewayEventuallyServes(hostName, oldBody)

	// A route to the permanently-empty backend publishes as truth: 503 (no
	// healthy upstream), not an indefinite hold.
	err := s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, emptyRouteManifest)
	s.Require().NoError(err, "can create route to permanently-empty backend")
	s.eventuallyRouteObserved("xds-warming-empty")
	s.assertGatewayEventuallyStatus("xds-warming-empty.example.com", http.StatusServiceUnavailable, time.Minute, time.Second)

	// Unrelated route updates must keep flowing despite the steady-state
	// empty reference.
	err = s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, canaryRouteManifest)
	s.Require().NoError(err, "can create canary route while empty-backend route exists")
	s.assertGatewayEventuallyServes("xds-warming-canary.example.com", oldBody)

	// Controller restart: the snapshot cache starts empty and every rebuild
	// carries the steady-state empty gap, so warm clients depend on the
	// budget-bounded truth publish to ever receive config again.
	err = s.TestInstallation.Actions.Kubectl().RestartDeploymentAndWait(s.Ctx, "kgateway",
		"-n", s.TestInstallation.Metadata.InstallNamespace)
	s.Require().NoError(err, "can restart the kgateway controller")

	// Existing traffic is never interrupted (Envoy keeps its last config).
	s.assertGatewayEventuallyServes(hostName, oldBody)

	// The hole-1 regression check: a route change applied AFTER the restart
	// must become effective. Pre-fix, the warm client was withheld forever
	// and this route never appeared.
	err = s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, postrestartRouteManifest)
	s.Require().NoError(err, "can create route after controller restart")
	s.eventuallyRouteObserved("xds-warming-postrestart")
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		statusCode, body, err := s.gatewayResponse("xds-warming-postrestart.example.com")
		g.Expect(err).NotTo(gomega.HaveOccurred(), "gateway request should complete")
		g.Expect(statusCode).To(gomega.Equal(http.StatusOK), "gateway returned body: %s", body)
		g.Expect(body).To(gomega.ContainSubstring(oldBody))
	}).WithContext(s.Ctx).WithTimeout(2*time.Minute).WithPolling(time.Second).Should(gomega.Succeed(),
		"a route created after a controller restart must become effective despite a steady-state empty reference")

	// The #14352 crashloop check: a fresh proxy pod (gateway rollout) must
	// receive a snapshot and go Ready. Pre-fix it starved at cds init and
	// the rollout never completed.
	err = s.TestInstallation.Actions.Kubectl().RestartDeploymentAndWait(s.Ctx, gatewayName,
		"-n", gatewayNamespace)
	s.Require().NoError(err, "a fresh gateway proxy pod must go Ready despite a steady-state empty reference")
	s.assertGatewayEventuallyServes(hostName, oldBody)
}

func (s *testingSuite) assertGatewayEventuallyStatus(host string, status int, timeout, poll time.Duration) {
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		statusCode, body, err := s.gatewayResponse(host)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "gateway request should complete")
		g.Expect(statusCode).To(gomega.Equal(status), "gateway returned body: %s", body)
	}).WithContext(s.Ctx).WithTimeout(timeout).WithPolling(poll).Should(gomega.Succeed())
}

// assertGatewayEventuallyMixes asserts that, within a burst of requests, the
// gateway serves bodySubstring on some and answers status on others — the
// signature of a weighted split where one target is legitimately empty.
func (s *testingSuite) assertGatewayEventuallyMixes(host, bodySubstring string, status int, timeout, poll time.Duration) {
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		var sawBody, sawStatus bool
		observed := make([]string, 0, 12)
		for range 12 {
			statusCode, body, err := s.gatewayResponse(host)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "gateway request should complete")
			observed = append(observed, fmt.Sprintf("%d:%s", statusCode, body))
			if statusCode == http.StatusOK && strings.Contains(body, bodySubstring) {
				sawBody = true
			}
			if statusCode == status {
				sawStatus = true
			}
			if sawBody && sawStatus {
				return
			}
		}
		g.Expect(sawBody && sawStatus).To(gomega.BeTrue(), "observed responses: %v", observed)
	}).WithContext(s.Ctx).WithTimeout(timeout).WithPolling(poll).Should(gomega.Succeed())
}

func (s *testingSuite) eventuallyRouteObserved(routeName string) {
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		route := &gwv1.HTTPRoute{}
		err := s.TestInstallation.ClusterContext.Client.Get(
			s.Ctx,
			types.NamespacedName{Name: routeName, Namespace: gatewayNamespace},
			route,
		)
		g.Expect(err).NotTo(gomega.HaveOccurred(), "can get HTTPRoute %s/%s", gatewayNamespace, routeName)

		generation := route.GetGeneration()
		g.Expect(route.Status.Parents).NotTo(gomega.BeEmpty(), "HTTPRoute should have parent status")
		for _, parent := range route.Status.Parents {
			accepted := apimeta.FindStatusCondition(parent.Conditions, string(gwv1.RouteConditionAccepted))
			if accepted != nil && accepted.Status == metav1.ConditionTrue && accepted.ObservedGeneration >= generation {
				return
			}
		}
		g.Expect(false).To(gomega.BeTrue(), "HTTPRoute %s/%s has not observed generation %d; status: %+v",
			gatewayNamespace, routeName, generation, route.Status)
	}).WithContext(s.Ctx).WithTimeout(time.Minute).WithPolling(500 * time.Millisecond).Should(gomega.Succeed())
}

func (s *testingSuite) assertActiveClusterIsPresent(clusterName string) {
	s.TestInstallation.AssertionsT(s.T()).AssertEnvoyAdminApi(s.Ctx, proxyObjectMeta, func(ctx context.Context, adminClient *admincli.Client) {
		s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
			clusters, err := adminClient.GetDynamicClusters(ctx)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "can get dynamic active clusters")
			_, ok := clusters[clusterName]
			g.Expect(ok).To(gomega.BeTrue(), "cluster %s should be active", clusterName)
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

func (s *testingSuite) assertGatewayEventuallyServesAll(host string, bodySubstrings []string, timeout, poll time.Duration) {
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		seen := make(map[string]bool, len(bodySubstrings))
		observedBodies := make([]string, 0, 12)

		for range 12 {
			statusCode, body, err := s.gatewayResponse(host)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "gateway request should complete")
			g.Expect(statusCode).To(gomega.Equal(http.StatusOK), "gateway returned body: %s", body)

			observedBodies = append(observedBodies, body)
			for _, bodySubstring := range bodySubstrings {
				if strings.Contains(body, bodySubstring) {
					seen[bodySubstring] = true
				}
			}
			if len(seen) == len(bodySubstrings) {
				return
			}
		}

		missing := make([]string, 0, len(bodySubstrings))
		for _, bodySubstring := range bodySubstrings {
			if !seen[bodySubstring] {
				missing = append(missing, bodySubstring)
			}
		}
		g.Expect(missing).To(gomega.BeEmpty(), "observed bodies: %v", observedBodies)
	}).WithContext(s.Ctx).WithTimeout(timeout).WithPolling(poll).Should(gomega.Succeed())
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
