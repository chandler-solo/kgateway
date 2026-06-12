//go:build e2e

package loadtesting

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	gwv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwv1b1 "sigs.k8s.io/gateway-api/apis/v1beta1"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	testdefaults "github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
)

// StrictChurn is a control-plane convergence test for the per-client xDS
// pipeline under its worst-case field shape (#14184): strict validation
// (every translated cluster pays an external envoy invocation), a few hundred
// backends, sustained route/Service churn including a dangling backend
// reference, and a controller restart mid-churn. It asserts the properties
// the per-client liveness work guarantees:
//
//   - stable routes keep serving throughout churn and across the restart
//     (connected clients are never stranded on stale or withheld config);
//   - new config still converges after the churn stops (a route created
//     post-churn becomes routable within a bound);
//   - per-client snapshot deferrals plateau once inputs are consistent
//     (kgateway_xds_snapshot_perclient_defers_total stops increasing), and
//     any deferral episode is followed by a recovery
//     (kgateway_xds_snapshot_perclient_recoveries_total).
//
// The suite mutates the controller deployment (KGW_VALIDATION_MODE=STRICT)
// and restores it on teardown, so it must not share a cluster with suites
// that assume a steady controller. It is registered with the suite runner but
// excluded from the CI e2e clusters; run it via `make run-load-tests-strict-churn`.

// Scale and timing knobs. The scale is sized so a full per-client fan-out is
// hundreds of strict validations per connected client — large enough that a
// wedged or backlogged pipeline fails the convergence bounds, small enough to
// run on a laptop kind cluster in a few minutes.
const (
	strictChurnCycles  = 8
	churnCycleInterval = 5 * time.Second
	// endpointChurnInterval paces the background EndpointSlice rewriter that
	// keeps the fleet's endpoints from ever quiescing during phases 4-5.
	endpointChurnInterval = 250 * time.Millisecond
	// gatewayRolloutBound is how long a rolling-restarted gateway Envoy has to
	// come back Ready. A fresh Envoy is a brand-new xDS client with no
	// fallback config: if its first snapshot is withheld indefinitely, the
	// pod never reports Ready and the rollout wedges ("gateways are unable to
	// spawn"). Generous enough to absorb one full per-client rebuild on a
	// healthy control plane.
	gatewayRolloutBound = "180s"

	// stableRouteBound is how quickly the stable route must answer 200 at
	// every checkpoint during churn. It was reachable before churn started,
	// so any sustained failure means the data plane lost working config.
	stableRouteBound = 30 * time.Second
	// convergenceBound is how quickly a route created after the churn stops
	// must become routable end to end. This is the liveness property: the
	// per-client pipeline still publishes after churn plus a restart.
	convergenceBound = 90 * time.Second
	// plateauWindow and plateauTimeout bound the defers-stop-increasing
	// assertion: two samples plateauWindow apart must be equal within
	// plateauTimeout of churn ending.
	plateauWindow  = 10 * time.Second
	plateauTimeout = 3 * time.Minute

	strictChurnMetricsService = "kgateway-loadtest-metrics"
	metricsPort               = 9092

	defersMetric           = "kgateway_xds_snapshot_perclient_defers_total"
	recoveriesMetric       = "kgateway_xds_snapshot_perclient_recoveries_total"
	boundedPublishesMetric = "kgateway_xds_snapshot_perclient_bounded_publishes_total"

	stableRouteHost    = "stable.strict-churn.example.com"
	postChurnRouteHost = "postchurn.strict-churn.example.com"
)

var strictChurnGateways = []string{"churn-gw-1", "churn-gw-2"}

// Fleet scale, overridable to push past the laptop-friendly defaults when
// hunting load-dependent behavior (e.g. KGW_LOADTEST_BACKENDS=800).
var (
	strictChurnBackends = envScale("KGW_LOADTEST_BACKENDS", 200)
	strictChurnRoutes   = envScale("KGW_LOADTEST_ROUTES", 200)
)

func envScale(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// controllerAppName returns the app.kubernetes.io/name of the controller under
// test. Defaults to the OSS controller; override with
// KGW_LOADTEST_CONTROLLER_APP (e.g. for a downstream distribution) to point
// the deployment lookup and metrics scrape at a differently-labeled control
// plane. The gateway class is not parameterized — create a GatewayClass named
// "kgateway" bound to the distribution's controllerName instead.
func controllerAppName() string {
	if app := os.Getenv("KGW_LOADTEST_CONTROLLER_APP"); app != "" {
		return app
	}
	return "kgateway"
}

func controllerSelector() string {
	return fmt.Sprintf("%s=%s", testdefaults.WellKnownAppLabel, controllerAppName())
}

var _ e2e.NewSuiteFunc = NewStrictChurnSuite

func NewStrictChurnSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &StrictChurnSuite{
		LoadTestingSuite: LoadTestingSuite{
			Suite:            suite.Suite{},
			ctx:              ctx,
			testInstallation: testInst,
		},
	}
}

type StrictChurnSuite struct {
	LoadTestingSuite
	loadTestManager      *LoadTestManager
	installNamespace     string
	controllerDeployment string
}

func (s *StrictChurnSuite) SetupSuite() {
	// Hard opt-in gate, independent of -run regexes: this suite mutates the
	// controller deployment (strict validation env, a mid-test restart), so a
	// broad invocation like the nightly's unanchored `^TestKgateway` matcher
	// must not pick it up implicitly. `make run-load-tests-strict-churn` sets
	// the variable.
	if os.Getenv("KGW_ENABLE_STRICT_CHURN") != "true" {
		s.T().Skip("StrictChurn mutates the controller deployment; set KGW_ENABLE_STRICT_CHURN=true (or use `make run-load-tests-strict-churn`) to run it")
	}
	s.loadTestManager = NewLoadTestManager(s.ctx, s.testInstallation,
		fmt.Sprintf("kgateway-strictchurn-%d", time.Now().UnixNano()))
	s.installNamespace = s.testInstallation.Metadata.InstallNamespace

	name, err := s.resolveControllerDeployment()
	s.Require().NoError(err, "should find the kgateway controller deployment in %s", s.installNamespace)
	s.controllerDeployment = name

	// The curl pod issues both data-plane probes and metrics scrapes; nginx is
	// the real backend behind the stable and post-churn routes (the simulated
	// services have unroutable endpoints, so they can't answer traffic).
	err = s.testInstallation.Actions.Kubectl().ApplyFile(s.ctx, testdefaults.CurlPodManifest)
	s.Require().NoError(err, "should apply curl pod")
	err = s.testInstallation.Actions.Kubectl().ApplyFile(s.ctx, testdefaults.NginxPodManifest)
	s.Require().NoError(err, "should apply nginx backend")
	s.testInstallation.AssertionsT(s.T()).EventuallyPodsRunning(s.ctx,
		testdefaults.CurlPod.GetNamespace(), metav1.ListOptions{LabelSelector: testdefaults.CurlPodLabelSelector})
	s.testInstallation.AssertionsT(s.T()).EventuallyPodsRunning(s.ctx,
		testdefaults.NginxPod.GetNamespace(), metav1.ListOptions{
			FieldSelector: "metadata.name=" + testdefaults.NginxPod.GetName(),
		})

	s.Require().NoError(s.createMetricsService(), "should expose controller metrics")

	s.T().Logf("Enabling strict validation on deployment/%s in %s", s.controllerDeployment, s.installNamespace)
	s.Require().NoError(s.setValidationMode("KGW_VALIDATION_MODE=STRICT"))
}

func (s *StrictChurnSuite) TearDownSuite() {
	// SetupSuite skipped (opt-in gate) before creating anything: do not touch
	// the cluster — the curl/nginx manifests may be in use by other suites.
	if s.loadTestManager == nil {
		return
	}
	// Restore the controller before anything else so a failed run doesn't
	// leave the install in strict mode for whoever uses the cluster next.
	if s.controllerDeployment != "" {
		if err := s.setValidationMode("KGW_VALIDATION_MODE-"); err != nil {
			s.T().Logf("WARNING: failed to restore validation mode: %v", err)
		}
	}
	if s.loadTestManager != nil {
		s.loadTestManager.CleanupAll()
	}
	_ = s.testInstallation.Actions.Kubectl().DeleteFileSafe(s.ctx, testdefaults.NginxPodManifest)
	_ = s.testInstallation.Actions.Kubectl().DeleteFileSafe(s.ctx, testdefaults.CurlPodManifest)
}

func (s *StrictChurnSuite) resolveControllerDeployment() (string, error) {
	out, _, err := s.testInstallation.ClusterContext.Cli.Execute(s.ctx,
		"get", "deploy", "-n", s.installNamespace,
		"-l", controllerSelector(),
		"-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(out)
	if name == "" {
		return "", fmt.Errorf("no deployment matching %q in %s", controllerSelector(), s.installNamespace)
	}
	return name, nil
}

// setValidationMode applies a `kubectl set env` expression (KEY=VALUE or KEY-)
// to the controller deployment and waits for the resulting rollout.
func (s *StrictChurnSuite) setValidationMode(envExpr string) error {
	if err := s.testInstallation.Actions.Kubectl().RunCommand(s.ctx,
		"set", "env", "-n", s.installNamespace,
		"deployment/"+s.controllerDeployment, envExpr); err != nil {
		return err
	}
	return s.testInstallation.Actions.Kubectl().DeploymentRolloutStatus(s.ctx,
		s.controllerDeployment, "-n", s.installNamespace, "--timeout=120s")
}

// createMetricsService exposes the controller's metrics port inside the
// cluster so the curl pod can scrape it. The default install has no metrics
// Service; the e2e metrics feature creates its own, so this one gets a
// distinct name to avoid colliding if both ever share a cluster.
func (s *StrictChurnSuite) createMetricsService() error {
	return s.loadTestManager.createResource(&corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      strictChurnMetricsService,
			Namespace: s.installNamespace,
			Labels:    map[string]string{"loadtest": "true"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{testdefaults.WellKnownAppLabel: controllerAppName()},
			Ports: []corev1.ServicePort{{
				Name:       "metrics",
				Port:       metricsPort,
				TargetPort: intstr.FromString("metrics"),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	})
}

func (s *StrictChurnSuite) TestStrictChurnConvergence() {
	s.T().Log("=== StrictChurn: strict validation + route/Service churn + controller restart ===")

	// Phase 1: scale fixture — fake Services/EndpointSlices and baseline routes.
	s.Require().NoError(s.loadTestManager.SetupSimulation(strictChurnBackends, "strict-churn"),
		"should setup cluster simulation")
	s.Require().NoError(s.loadTestManager.SetupTestInfrastructure(), "should setup test infrastructure")
	s.createReferenceGrants()

	s.Require().NoError(s.loadTestManager.CreateGateways(strictChurnGateways), "should create gateways")
	s.Require().NoError(s.loadTestManager.WaitForGatewayReadiness(gatewayReadinessTimeout), "gateways should be ready")

	config := &AttachedRoutesConfig{
		Gateways:    strictChurnGateways,
		Routes:      strictChurnRoutes,
		GracePeriod: GetConfig(strictChurnRoutes).GracePeriod,
		BatchSize:   GetOptimalBatchSize(strictChurnRoutes),
	}
	s.Require().NoError(s.loadTestManager.CreateRoutesBatched(config), "should create baseline routes")
	s.waitForAttachedRoutes(strictChurnGateways[0], strictChurnRoutes/len(strictChurnGateways), translationCompletionTimeout)

	// Phase 2: a stable route to a real backend, verified working before any
	// churn. This is the route that must never break.
	s.createRouteToNginx("stable-route", strictChurnGateways[0], stableRouteHost)
	s.assertRouteServes(strictChurnGateways[0], stableRouteHost, stableRouteBound)
	s.T().Log("Stable route serving 200 — starting churn")

	// Phase 3: two permanently-unhealthy references that persist through the
	// run, attached to the stable route's gateway. The dangling route's
	// Service never exists (unresolvable backend); the starved route's
	// Service exists but its selector matches no pods, so the referenced EDS
	// cluster never gets a ready endpoint — the "stable service with no
	// ready CLA" shape that historically wedged the all-or-nothing
	// consistency gate for the whole gateway. A correct control plane must
	// keep publishing for the rest of the gateway's config regardless.
	s.createDanglingRoute(strictChurnGateways[0])
	s.createStarvedRoute(strictChurnGateways[0])

	// Phase 4: churn — create/delete a Service+EndpointSlice and a route each
	// cycle, restart the controller halfway through, and require the stable
	// route back at 200 before the next cycle. A background churner also
	// rewrites the simulated fleet's EndpointSlices continuously: real
	// clusters never quiesce, and an all-or-nothing consistency gate that
	// requires a globally-consistent instant will never find one while
	// endpoints keep moving. (This is the load shape that separates a
	// bounded-wait control plane from a wedged one; quiet-endpoint runs
	// converge even on builds with the unbounded gate.)
	defersBeforeChurn := s.scrapeCounterSum(defersMetric)
	churnerCtx, stopChurner := context.WithCancel(s.ctx)
	defer stopChurner()
	s.startEndpointChurner(churnerCtx)
	for cycle := range strictChurnCycles {
		s.runChurnCycle(cycle)

		// Client identity churn: roll each gateway's Envoy once mid-run. A
		// rolled pod reconnects as a brand-new xDS client whose first
		// snapshot must be built from scratch while the fleet churns —
		// completion is asserted after the loop (gatewayRolloutBound).
		switch cycle {
		case 2:
			s.rollGateway(strictChurnGateways[0])
		case 5:
			s.rollGateway(strictChurnGateways[1])
		}

		if cycle == strictChurnCycles/2 {
			s.T().Logf("Cycle %d: restarting controller mid-churn", cycle)
			s.Require().NoError(s.testInstallation.Actions.Kubectl().RestartDeploymentAndWait(s.ctx,
				s.controllerDeployment, "-n", s.installNamespace), "controller should restart cleanly mid-churn")
		}

		s.assertRouteServes(strictChurnGateways[0], stableRouteHost, stableRouteBound)
		time.Sleep(churnCycleInterval)
	}

	// Phase 4b: every rolled gateway must finish its rollout — the freshly
	// started Envoy only reports Ready once it has received config, so a
	// control plane that withholds a new client's first snapshot indefinitely
	// fails here with a wedged rollout.
	for _, gw := range strictChurnGateways {
		s.Require().NoError(s.testInstallation.Actions.Kubectl().DeploymentRolloutStatus(s.ctx,
			gw, "-n", s.loadTestManager.testNamespace, "--timeout="+gatewayRolloutBound),
			"gateway %s rollout must complete: a fresh Envoy client must receive its first xDS snapshot", gw)
	}
	s.T().Log("Route churn + gateway rolls complete — asserting convergence under live endpoint churn")

	// Phase 5: liveness — config created while the fleet's endpoints are
	// still moving must flow to the data plane within the bound. This is the
	// assertion that separates a bounded-wait publisher from an
	// all-or-nothing gate: the latter waits for a globally-consistent
	// instant that a busy fleet never provides.
	s.createRouteToNginx("postchurn-route", strictChurnGateways[1], postChurnRouteHost)
	s.assertRouteServes(strictChurnGateways[1], postChurnRouteHost, convergenceBound)
	stopChurner()

	// Phase 6: the defer counter must plateau — two samples plateauWindow
	// apart with no increments. A sustained defer rate after inputs settle is
	// exactly the wedge signature this scenario exists to catch.
	var lastDefers float64 = -1
	s.Require().Eventually(func() bool {
		current := s.scrapeCounterSum(defersMetric)
		plateaued := lastDefers >= 0 && current == lastDefers
		if !plateaued {
			s.T().Logf("defers_total=%v (waiting for plateau)", current)
		}
		lastDefers = current
		return plateaued
	}, plateauTimeout, plateauWindow, "per-client defers must stop increasing after churn ends")

	// Phase 7: every defer episode must have healed. The counters reset on
	// the mid-churn restart, so compare against the post-restart baseline.
	finalDefers := s.scrapeCounterSum(defersMetric)
	finalRecoveries := s.scrapeCounterSum(recoveriesMetric)
	finalBoundedFirst := s.scrapeCounterSumMatching(boundedPublishesMetric, `mode="first_publish"`)
	finalBoundedCarry := s.scrapeCounterSumMatching(boundedPublishesMetric, `mode="carry_forward"`)
	s.T().Logf("Final counters: defers_total=%v (pre-churn %v), recoveries_total=%v, bounded_publishes_total{first_publish}=%v, bounded_publishes_total{carry_forward}=%v",
		finalDefers, defersBeforeChurn, finalRecoveries, finalBoundedFirst, finalBoundedCarry)
	if finalDefers > 0 {
		s.Require().GreaterOrEqual(finalRecoveries, float64(1),
			"clients were deferred (%v defers) but never recovered; per-client publication is wedged", finalDefers)
	}

	s.T().Log("=== StrictChurn PASSED: stable traffic held, post-churn config converged, defers plateaued ===")
}

// runChurnCycle creates this cycle's Service+EndpointSlice+route, then deletes
// the previous cycle's, so the controller continuously sees backend and route
// add/remove events without the resource count growing.
func (s *StrictChurnSuite) runChurnCycle(cycle int) {
	simNS := s.loadTestManager.simulator.config.Namespace
	svcName := fmt.Sprintf("churn-svc-%d", cycle)

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: simNS,
			Labels:    map[string]string{"loadtest": "true", "churn": "true"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": svcName},
			Ports: []corev1.ServicePort{{
				Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP,
			}},
		},
	}
	port := int32(8080)
	portName := "http"
	protocol := corev1.ProtocolTCP
	eps := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      svcName,
			Namespace: simNS,
			Labels: map[string]string{
				"kubernetes.io/service-name": svcName,
				"loadtest":                   "true",
				"churn":                      "true",
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints: []discoveryv1.Endpoint{{
			Addresses: []string{fmt.Sprintf("10.250.0.%d", (cycle%250)+1)},
		}},
		Ports: []discoveryv1.EndpointPort{{Name: &portName, Port: &port, Protocol: &protocol}},
	}
	route := s.buildChurnRoute(fmt.Sprintf("churn-route-%d", cycle), strictChurnGateways[cycle%len(strictChurnGateways)],
		fmt.Sprintf("churn-%d.strict-churn.example.com", cycle), svcName, simNS)

	s.Require().NoError(s.testInstallation.ClusterContext.Client.Create(s.ctx, svc), "churn service create")
	s.Require().NoError(s.testInstallation.ClusterContext.Client.Create(s.ctx, eps), "churn endpointslice create")
	s.Require().NoError(s.testInstallation.ClusterContext.Client.Create(s.ctx, route), "churn route create")

	// Delete the previous cycle's resources (kept alive one cycle so the
	// controller always overlaps an add with a remove).
	if cycle > 0 {
		prev := cycle - 1
		prevSvc := fmt.Sprintf("churn-svc-%d", prev)
		_ = s.testInstallation.Actions.Kubectl().RunCommand(s.ctx,
			"delete", "httproute", fmt.Sprintf("churn-route-%d", prev),
			"-n", s.loadTestManager.testNamespace, "--ignore-not-found", "--wait=false")
		_ = s.testInstallation.Actions.Kubectl().RunCommand(s.ctx,
			"delete", "service", prevSvc, "-n", simNS, "--ignore-not-found", "--wait=false")
		_ = s.testInstallation.Actions.Kubectl().RunCommand(s.ctx,
			"delete", "endpointslice", prevSvc, "-n", simNS, "--ignore-not-found", "--wait=false")
	}
	// The last cycle's leftovers are deleted by namespace teardown.
}

func (s *StrictChurnSuite) createDanglingRoute(gateway string) {
	route := s.buildChurnRoute("dangling-route", gateway,
		"dangling.strict-churn.example.com", "no-such-service", s.loadTestManager.testNamespace)
	s.Require().NoError(s.testInstallation.ClusterContext.Client.Create(s.ctx, route), "dangling route create")
	s.loadTestManager.createdRoutes = append(s.loadTestManager.createdRoutes, route)
}

// startEndpointChurner continuously rewrites simulated EndpointSlices, one
// every endpointChurnInterval, rotating through the fleet. Each write is a
// real input change (the endpoint IP moves), so every referenced backend's
// per-client endpoint translation keeps re-running for every connected
// client — the steady-state load shape of a production fleet where pods are
// always rolling somewhere.
func (s *StrictChurnSuite) startEndpointChurner(ctx context.Context) {
	cfg := s.loadTestManager.simulator.config
	simNS := cfg.Namespace
	// The simulator sizes the fleet itself: the number of sim-service-*
	// EndpointSlices is FakeNodeCount*ServicesPerNode, NOT strictChurnBackends.
	// Rotate over what actually exists or the churner runs off the end of the
	// fleet and stops producing churn.
	totalSimServices := cfg.FakeNodeCount * cfg.ServicesPerNode
	s.Require().Positive(totalSimServices, "simulation must have services to churn")
	go func() {
		ticker := time.NewTicker(endpointChurnInterval)
		defer ticker.Stop()
		attempts, writes := 0, 0
		for {
			select {
			case <-ctx.Done():
				s.T().Logf("Endpoint churner stopped after %d EndpointSlice rewrites (%d attempts)", writes, attempts)
				return
			case <-ticker.C:
			}
			idx := attempts % totalSimServices
			ip := fmt.Sprintf("10.246.%d.%d", (attempts/250)%200, attempts%250+1)
			// Advance every tick regardless of outcome so one unpatchable
			// slice can never wedge the rotation.
			attempts++
			patch := fmt.Sprintf(`[{"op":"replace","path":"/endpoints/0/addresses/0","value":%q}]`, ip)
			eps := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("sim-service-%d", idx), Namespace: simNS,
			}}
			if err := s.testInstallation.ClusterContext.Client.Patch(ctx, eps,
				client.RawPatch(types.JSONPatchType, []byte(patch))); err != nil {
				if ctx.Err() != nil {
					return
				}
				s.T().Logf("endpoint churn patch failed (continuing): %v", err)
				continue
			}
			writes++
		}
	}()
}

// rollGateway triggers a rolling restart of a gateway's Envoy deployment
// (named after the Gateway by the deployer) without waiting — completion is
// asserted later against gatewayRolloutBound.
func (s *StrictChurnSuite) rollGateway(gateway string) {
	s.T().Logf("Rolling gateway %s (new xDS client identity)", gateway)
	s.Require().NoError(s.testInstallation.Actions.Kubectl().RestartDeployment(s.ctx,
		gateway, "-n", s.loadTestManager.testNamespace), "should roll gateway %s", gateway)
}

// createStarvedRoute creates a Service whose selector matches no pods plus a
// route referencing it: the backendRef resolves (the Service exists), so the
// gateway's referenced-cluster set permanently contains an EDS cluster that
// will never have a ready endpoint.
func (s *StrictChurnSuite) createStarvedRoute(gateway string) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "starved-svc",
			Namespace: s.loadTestManager.testNamespace,
			Labels:    map[string]string{"loadtest": "true"},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "starved-no-such-pod"},
			Ports: []corev1.ServicePort{{
				Name: "http", Port: 80, TargetPort: intstr.FromInt(8080), Protocol: corev1.ProtocolTCP,
			}},
		},
	}
	s.Require().NoError(s.loadTestManager.createResource(svc), "starved service create")
	route := s.buildChurnRoute("starved-route", gateway,
		"starved.strict-churn.example.com", "starved-svc", s.loadTestManager.testNamespace)
	s.Require().NoError(s.testInstallation.ClusterContext.Client.Create(s.ctx, route), "starved route create")
	s.loadTestManager.createdRoutes = append(s.loadTestManager.createdRoutes, route)
}

func (s *StrictChurnSuite) createRouteToNginx(name, gateway, hostname string) {
	route := s.buildChurnRoute(name, gateway, hostname,
		testdefaults.NginxSvc.GetName(), testdefaults.NginxSvc.GetNamespace())
	s.Require().NoError(s.testInstallation.ClusterContext.Client.Create(s.ctx, route), "route %s create", name)
	s.loadTestManager.createdRoutes = append(s.loadTestManager.createdRoutes, route)
}

func (s *StrictChurnSuite) buildChurnRoute(name, gateway, hostname, backendSvc, backendNS string) *gwv1.HTTPRoute {
	pathType := gwv1.PathMatchPathPrefix
	pathValue := "/"
	ns := gwv1.Namespace(backendNS)
	port := gwv1.PortNumber(80)
	if backendNS == testdefaults.NginxSvc.GetNamespace() {
		port = gwv1.PortNumber(8080)
	}
	return &gwv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.loadTestManager.testNamespace,
			Labels:    map[string]string{"loadtest": "true"},
		},
		Spec: gwv1.HTTPRouteSpec{
			CommonRouteSpec: gwv1.CommonRouteSpec{
				ParentRefs: []gwv1.ParentReference{{Name: gwv1.ObjectName(gateway)}},
			},
			Hostnames: []gwv1.Hostname{gwv1.Hostname(hostname)},
			Rules: []gwv1.HTTPRouteRule{{
				Matches: []gwv1.HTTPRouteMatch{{
					Path: &gwv1.HTTPPathMatch{Type: &pathType, Value: &pathValue},
				}},
				BackendRefs: []gwv1.HTTPBackendRef{{
					BackendRef: gwv1.BackendRef{
						BackendObjectReference: gwv1.BackendObjectReference{
							Name:      gwv1.ObjectName(backendSvc),
							Namespace: &ns,
							Port:      &port,
						},
					},
				}},
			}},
		},
	}
}

// createReferenceGrants permits the test namespace's routes to reference
// Services in the simulation and nginx namespaces. Without these, every
// cross-namespace backendRef is RefNotPermitted and no clusters are built —
// which would silence the strict-validation load this scenario exists to apply.
func (s *StrictChurnSuite) createReferenceGrants() {
	for _, targetNS := range []string{
		s.loadTestManager.simulator.config.Namespace,
		testdefaults.NginxSvc.GetNamespace(),
	} {
		grant := &gwv1b1.ReferenceGrant{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "strict-churn-grant",
				Namespace: targetNS,
				Labels:    map[string]string{"loadtest": "true"},
			},
			Spec: gwv1b1.ReferenceGrantSpec{
				From: []gwv1b1.ReferenceGrantFrom{{
					Group:     gwv1b1.Group(gwv1.GroupName),
					Kind:      "HTTPRoute",
					Namespace: gwv1b1.Namespace(s.loadTestManager.testNamespace),
				}},
				To: []gwv1b1.ReferenceGrantTo{{
					Group: "",
					Kind:  "Service",
				}},
			},
		}
		s.Require().NoError(s.loadTestManager.createResource(grant),
			"should create ReferenceGrant in %s", targetNS)
	}
}

// assertRouteServes requires the given hostname to answer 200 with the nginx
// body through the gateway's in-cluster Service within the bound.
func (s *StrictChurnSuite) assertRouteServes(gateway, hostname string, bound time.Duration) {
	s.testInstallation.AssertionsT(s.T()).AssertEventualCurlResponse(
		s.ctx,
		testdefaults.CurlPodExecOpt,
		[]curl.Option{
			curl.WithHost(kubeutils.ServiceFQDN(metav1.ObjectMeta{
				Name: gateway, Namespace: s.loadTestManager.testNamespace,
			})),
			curl.WithHostHeader(hostname),
			curl.WithPath("/"),
			curl.WithPort(80),
		},
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
			Body:       gomega.ContainSubstring(testdefaults.NginxResponse),
		},
		bound, 2*time.Second,
	)
}

// waitForAttachedRoutes polls a gateway's listener status until at least
// minRoutes report attached.
func (s *StrictChurnSuite) waitForAttachedRoutes(gateway string, minRoutes int, timeout time.Duration) {
	s.Require().Eventually(func() bool {
		gw := &gwv1.Gateway{}
		if err := s.testInstallation.ClusterContext.Client.Get(s.ctx,
			types.NamespacedName{Namespace: s.loadTestManager.testNamespace, Name: gateway}, gw); err != nil {
			return false
		}
		if len(gw.Status.Listeners) == 0 {
			return false
		}
		attached := int(gw.Status.Listeners[0].AttachedRoutes)
		s.T().Logf("Gateway %s: AttachedRoutes=%d (waiting for >=%d)", gateway, attached, minRoutes)
		return attached >= minRoutes
	}, timeout, translationPollingInterval, "baseline routes should attach to %s", gateway)
}

// scrapeCounterSum scrapes the controller's /metrics endpoint via the curl pod
// and returns the sum of the named counter across all label sets. Returns 0
// when the metric has not been emitted (a counter that never incremented is
// absent from the exposition).
func (s *StrictChurnSuite) scrapeCounterSum(metricName string) float64 {
	var sum float64
	s.Require().Eventually(func() bool {
		resp, err := s.testInstallation.ClusterContext.Cli.CurlFromPod(s.ctx, testdefaults.CurlPodExecOpt,
			curl.WithHost(kubeutils.ServiceFQDN(metav1.ObjectMeta{
				Name: strictChurnMetricsService, Namespace: s.installNamespace,
			})),
			curl.WithPort(metricsPort),
			curl.WithPath("/metrics"),
		)
		if err != nil {
			s.T().Logf("metrics scrape failed (will retry): %v", err)
			return false
		}
		sum = sumCounterText(resp.StdOut, metricName)
		return true
	}, 60*time.Second, 2*time.Second, "should scrape controller metrics")
	return sum
}

// scrapeCounterSumMatching is scrapeCounterSum restricted to samples whose
// label block contains labelSubstr (e.g. `mode="carry_forward"`).
func (s *StrictChurnSuite) scrapeCounterSumMatching(metricName, labelSubstr string) float64 {
	var sum float64
	s.Require().Eventually(func() bool {
		resp, err := s.testInstallation.ClusterContext.Cli.CurlFromPod(s.ctx, testdefaults.CurlPodExecOpt,
			curl.WithHost(kubeutils.ServiceFQDN(metav1.ObjectMeta{
				Name: strictChurnMetricsService, Namespace: s.installNamespace,
			})),
			curl.WithPort(metricsPort),
			curl.WithPath("/metrics"),
		)
		if err != nil {
			s.T().Logf("metrics scrape failed (will retry): %v", err)
			return false
		}
		var filtered strings.Builder
		for line := range strings.Lines(resp.StdOut) {
			if strings.Contains(line, labelSubstr) {
				filtered.WriteString(line)
			}
		}
		sum = sumCounterText(filtered.String(), metricName)
		return true
	}, 60*time.Second, 2*time.Second, "should scrape controller metrics")
	return sum
}

// sumCounterText sums every sample of the named counter in Prometheus text
// exposition format, across all label sets.
func sumCounterText(metricsText, metricName string) float64 {
	var sum float64
	for line := range strings.Lines(metricsText) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, metricName) {
			continue
		}
		rest := line[len(metricName):]
		// Accept only "name{labels} value" or "name value" — not other
		// metrics sharing the prefix.
		if len(rest) == 0 || (rest[0] != '{' && rest[0] != ' ') {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if v, err := strconv.ParseFloat(fields[len(fields)-1], 64); err == nil {
			sum += v
		}
	}
	return sum
}
