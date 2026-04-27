//go:build e2e

package zero_downtime_rollout

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils/kubectl"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
	"github.com/kgateway-dev/kgateway/v2/test/helpers"
)

var (
	serviceManifest      = filepath.Join(fsutils.MustGetThisDir(), "testdata", "service.yaml")
	gatewayManifest      = filepath.Join(fsutils.MustGetThisDir(), "testdata", "gateway.yaml")
	agentgatewayManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "agentgateway.yaml")
	bloatManifest        = filepath.Join(fsutils.MustGetThisDir(), "testdata", "bloat.yaml")

	proxyObjectMeta = metav1.ObjectMeta{
		Name:      "gw",
		Namespace: "default",
	}

	agentgatewayObjectMeta = metav1.ObjectMeta{
		Name:      "agentgw",
		Namespace: "default",
	}
)

type testingSuiteKgateway struct {
	*base.BaseTestingSuite
}

func NewTestingSuiteKgateway(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuiteKgateway{
		base.NewBaseTestingSuite(
			ctx,
			testInst,
			base.TestCase{
				Manifests: []string{serviceManifest},
			},
			map[string]*base.TestCase{
				"TestZeroDowntimeRollout": {
					Manifests: []string{gatewayManifest, defaults.CurlPodManifest},
				},
				"TestZeroDowntimeControllerRestart": {
					Manifests: []string{gatewayManifest, bloatManifest},
				},
			},
		),
	}
}

func (s *testingSuiteKgateway) TestZeroDowntimeRollout() {
	// Ensure the gateway pod is up and running.
	s.TestInstallation.Assertions.EventuallyPodsRunning(s.Ctx,
		proxyObjectMeta.GetNamespace(), metav1.ListOptions{
			LabelSelector: defaults.WellKnownAppLabel + "=" + proxyObjectMeta.GetName(),
		})

	s.TestInstallation.Assertions.AssertEventualCurlResponse(
		s.Ctx,
		defaults.CurlPodExecOpt,
		[]curl.Option{
			curl.WithHost(kubeutils.ServiceFQDN(proxyObjectMeta)),
			curl.WithHostHeader("example.com"),
		},
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
		})

	kCli := kubectl.NewCli()

	// Send traffic to the gateway pod while we restart the deployment.
	// Run this for 30s which is long enough to restart the deployment since there's no easy way
	// to stop this command once the test is over.
	// This executes 800 req @ 4 req/sec = 20s (3 * terminationGracePeriodSeconds (5) + buffer).
	// kubectl exec -n hey heygw -- hey -disable-keepalive -c 4 -q 10 --cpus 1 -n 800 -m GET -t 1 -host example.com http://gw.default.svc.cluster.local:8080.
	args := []string{"exec", "-n", "hey", "heygw", "--", "hey", "-disable-keepalive", "-c", "4", "-q", "10", "--cpus", "1", "-n", "800", "-m", "GET", "-t", "1", "-host", "example.com", "http://gw.default.svc.cluster.local:8080"}

	cmd := kCli.Command(s.Ctx, args...)

	if err := cmd.Start(); err != nil {
		s.T().Fatal("error starting command", err)
	}

	// Restart the deployment, twice.
	// There should be no downtime, since the gateway pod
	// should have readiness probes configured.
	err := kCli.RestartDeploymentAndWait(s.Ctx, "gw")
	s.Require().NoError(err)

	time.Sleep(time.Second)

	err = kCli.RestartDeploymentAndWait(s.Ctx, "gw")
	s.Require().NoError(err)

	if err := cmd.Wait(); err != nil {
		s.T().Fatal("error waiting for command to finish", err)
	}

	// Verify that there were no errors.
	s.Contains(string(cmd.Output()), "[200]	800 responses")
	s.NotContains(string(cmd.Output()), "Error distribution")
}

// TestZeroDowntimeControllerRestart exercises the snapshotPerClient xDS
// race during controller restart. See pkg/kgateway/proxy_syncer/perclient.go.
// On controller restart, Envoy reconnects and creates a new Uniquely Connected
// Client (UCC). The per-client cluster transformation races with the
// snapshotPerClient transformation: if snapshotPerClient observes a partial
// per-client cluster set while listener/route resources already reference all
// clusters, it publishes a CDS missing clusters referenced by RDS/LDS, causing
// Envoy to 503/NC on the affected routes.
func (s *testingSuiteKgateway) TestZeroDowntimeControllerRestart() {
	// Ensure the gateway pod is up and running before exercising controller restarts.
	s.TestInstallation.Assertions.EventuallyPodsRunning(s.Ctx,
		proxyObjectMeta.GetNamespace(), metav1.ListOptions{
			LabelSelector: defaults.WellKnownAppLabel + "=" + proxyObjectMeta.GetName(),
		})

	kCli := kubectl.NewCli()
	installNamespace := s.TestInstallation.Metadata.InstallNamespace

	// Widen the xDS race window by making the controller's Service publish
	// not-ready endpoints. Without this, Envoy can't reconnect until the new
	// controller Pod's readiness probe passes — by which time KRT state is
	// typically warm. With publishNotReadyAddresses=true the Envoy reconnect
	// can land while per-client translation is still populating.
	s.Require().NoError(kCli.RunCommand(s.Ctx,
		"patch", "service", helpers.DefaultKgatewayDeploymentName,
		"-n", installNamespace,
		"--type", "merge",
		"-p", `{"spec":{"publishNotReadyAddresses":true}}`,
	))

	// Sanity-check in-cluster connectivity before exercising the race.
	s.requireInClusterCurlOK("example.com")

	// Restart the controller while traffic is in flight. We use hard pod
	// deletion (not rolling restart) so the new pod must cold-start its KRT
	// state. The bloat manifest added extra routes+services so per-client
	// translation has more work on a fresh controller. Looping restarts gives
	// the race more chances to fire; 500rps densely samples the race window.
	const restartIterations = 5
	s.startTrafficAndAssertNoErrorsWithArgs(120*time.Second, "10", "50", func() {
		for i := 0; i < restartIterations; i++ {
			s.Require().NoError(
				s.deleteControllerPods(kCli, installNamespace),
				"controller pod delete iteration %d failed", i+1,
			)
			s.waitForControllerReady(kCli, installNamespace)
		}
	})

	// Apply a fresh route after the restarts to prove the controller is
	// pushing new xDS, not just letting Envoy coast on cached config.
	postRestartRoute := `apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  name: post-restart-route
spec:
  parentRefs:
    - name: gw
  hostnames:
    - "post-restart.example.com"
  rules:
    - backendRefs:
        - name: example-svc
          port: 8080
`
	err := s.TestInstallation.ClusterContext.IstioClient.ApplyYAMLContents("default", postRestartRoute)
	s.Require().NoError(err)
	defer func() {
		_ = kCli.RunCommand(s.Ctx, "delete", "httproute", "post-restart-route", "-n", "default", "--ignore-not-found")
	}()

	s.requireInClusterCurlOK("post-restart.example.com")
}

// startTrafficAndAssertNoErrorsWithArgs runs sustained hey load while
// restartFunc executes. It fails on any non-200 status or transport error.
func (s *testingSuiteKgateway) startTrafficAndAssertNoErrorsWithArgs(duration time.Duration, concurrency, rate string, restartFunc func()) {
	kCli := kubectl.NewCli()
	args := []string{
		"exec", "-n", "hey", "heygw", "--",
		"hey", "-disable-keepalive",
		"-c", concurrency, "-q", rate, "--cpus", "1",
		"-z", duration.String(),
		"-m", "GET", "-t", "1",
		"-host", "example.com",
		"http://gw.default.svc.cluster.local:8080",
	}
	cmdCtx, cancel := context.WithCancel(s.Ctx)
	cmd := kCli.Command(cmdCtx, args...)

	if err := cmd.Start(); err != nil {
		cancel()
		s.T().Fatal("error starting command", err)
	}

	waited := false
	defer func() {
		cancel()
		if !waited {
			_ = cmd.Wait()
		}
	}()

	restartFunc()

	waitErr := cmd.Wait()
	waited = true
	if waitErr != nil {
		s.T().Fatalf("error waiting for command to finish: %v\ncommand output:\n%s", waitErr, string(cmd.Output()))
	}

	output := string(cmd.Output())
	s.NotContains(output, "Error distribution")

	seenStatusLine := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		seenStatusLine = true
		s.True(strings.HasPrefix(trimmed, "[200]\t"), "unexpected hey status distribution line: %s\nfull output:\n%s", trimmed, output)
	}
	s.True(seenStatusLine, "hey output did not include a status distribution:\n%s", output)
}

// requireInClusterCurlOK uses the heygw pod to issue a single-shot request
// to the gateway service inside the cluster and requires a 200. Retries for
// up to ~30s. Uses `hey -n 1` because the hey image doesn't ship curl.
func (s *testingSuiteKgateway) requireInClusterCurlOK(host string) {
	kCli := kubectl.NewCli()

	const (
		maxAttempts = 30
		waitBetween = time.Second
	)

	var lastOut, lastErr string
	for i := 0; i < maxAttempts; i++ {
		stdout, stderr, err := kCli.Execute(s.Ctx,
			"exec", "-n", "hey", "heygw", "--",
			"hey", "-disable-keepalive",
			"-n", "1", "-c", "1", "-t", "3",
			"-m", "GET",
			"-host", host,
			"http://gw.default.svc.cluster.local:8080",
		)
		lastOut, lastErr = stdout, stderr
		if err == nil && containsStatusLine(stdout, "[200]") && !strings.Contains(stdout, "Error distribution") {
			return
		}
		time.Sleep(waitBetween)
	}
	s.T().Fatalf("in-cluster probe to host %q never returned 200 after %d attempts\nstdout: %s\nstderr: %s",
		host, maxAttempts, lastOut, lastErr)
}

func containsStatusLine(out, prefix string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			return true
		}
	}
	return false
}

// deleteControllerPods hard-deletes the controller pod (force + grace 0) so a
// fresh replacement pod cold-starts, maximising the window during which Envoy
// reconnects to a controller whose KRT state is still warming.
func (s *testingSuiteKgateway) deleteControllerPods(kCli *kubectl.Cli, namespace string) error {
	return kCli.RunCommand(s.Ctx,
		"delete", "pod",
		"-n", namespace,
		"-l", "kgateway=kgateway",
		"--grace-period=0", "--force", "--wait=false",
	)
}

// waitForControllerReady polls until at least one controller pod is Ready.
// It fails the test if no pod is Ready within 60 seconds.
func (s *testingSuiteKgateway) waitForControllerReady(kCli *kubectl.Cli, namespace string) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		stdout, _, err := kCli.Execute(s.Ctx,
			"get", "pod", "-n", namespace,
			"-l", "kgateway=kgateway",
			"-o", `jsonpath={range .items[*]}{@.metadata.name}{":"}{@.status.conditions[?(@.type=='Ready')].status}{" "}{end}`,
		)
		if err == nil {
			for _, entry := range strings.Fields(strings.TrimSpace(stdout)) {
				if strings.HasSuffix(entry, ":True") {
					return
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	s.T().Fatalf("controller did not become Ready in namespace %q within 60s", namespace)
}

type testingSuiteAgentgateway struct {
	*base.BaseTestingSuite
}

func NewTestingSuiteAgentgateway(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuiteAgentgateway{
		base.NewBaseTestingSuite(
			ctx,
			testInst,
			base.TestCase{
				Manifests: []string{serviceManifest},
			},
			map[string]*base.TestCase{
				"TestZeroDowntimeRolloutAgentgateway": {
					Manifests: []string{agentgatewayManifest, defaults.CurlPodManifest},
				},
			},
		),
	}
}

func (s *testingSuiteAgentgateway) TestZeroDowntimeRolloutAgentgateway() {
	// Ensure the agentgateway pod is up and running.
	s.TestInstallation.Assertions.EventuallyPodsRunning(s.Ctx,
		agentgatewayObjectMeta.GetNamespace(), metav1.ListOptions{
			LabelSelector: defaults.WellKnownAppLabel + "=" + agentgatewayObjectMeta.GetName(),
		})

	s.TestInstallation.Assertions.AssertEventualCurlResponse(
		s.Ctx,
		defaults.CurlPodExecOpt,
		[]curl.Option{
			curl.WithHost(kubeutils.ServiceFQDN(agentgatewayObjectMeta)),
			curl.WithHostHeader("example.com"),
		},
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
		})

	kCli := kubectl.NewCli()

	// Send traffic to the gateway pod while we restart the deployment.
	// Run this for 30s which is long enough to restart the deployment since there's no easy way
	// to stop this command once the test is over.
	// This executes 800 req @ 4 req/sec = 20s (3 * terminationGracePeriodSeconds (5) + buffer).
	// kubectl exec -n hey heyagw -- hey -disable-keepalive -c 4 -q 10 --cpus 1 -n 800 -m GET -t 1 -host example.com http://agentgw.default.svc.cluster.local:8080.
	args := []string{"exec", "-n", "hey", "heyagw", "--", "hey", "-disable-keepalive", "-c", "4", "-q", "10", "--cpus", "1", "-n", "800", "-m", "GET", "-t", "1", "-host", "example.com", "http://agentgw.default.svc.cluster.local:8080"}

	cmd := kCli.Command(s.Ctx, args...)

	if err := cmd.Start(); err != nil {
		s.T().Fatal("error starting command", err)
	}

	// Restart the deployment, twice.
	// There should be no downtime, since the gateway pod
	// should have readiness probes configured.
	err := kCli.RestartDeploymentAndWait(s.Ctx, "agentgw")
	s.Require().NoError(err)

	time.Sleep(time.Second)

	err = kCli.RestartDeploymentAndWait(s.Ctx, "agentgw")
	s.Require().NoError(err)

	if err := cmd.Wait(); err != nil {
		s.T().Fatal("error waiting for command to finish", err)
	}

	// Verify that there were no errors.
	s.Contains(string(cmd.Output()), "[200]	800 responses")
	s.NotContains(string(cmd.Output()), "Error distribution")
}
