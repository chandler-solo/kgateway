//go:build e2e

package zero_downtime_rollout

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils/kubectl"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/common"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
	"github.com/kgateway-dev/kgateway/v2/test/helpers"
)

var (
	serviceManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "service.yaml")
	setupManifest   = filepath.Join(fsutils.MustGetThisDir(), "testdata", "setup.yaml")

	gatewayName = "gw"
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
				Manifests: []string{serviceManifest, setupManifest},
			},
			map[string]*base.TestCase{
				"TestZeroDowntimeRollout":           {},
				"TestZeroDowntimeControllerRestart": {},
			},
		),
	}
}

// startTrafficAndAssertNoErrors starts a load test with hey, executes restartFunc
// during traffic, then asserts all requests returned 200 with no errors.
func (s *testingSuiteKgateway) startTrafficAndAssertNoErrors(numRequests int, restartFunc func()) {
	kCli := kubectl.NewCli()

	args := []string{
		"exec", "-n", "hey", "heygw", "--",
		"hey", "-disable-keepalive",
		"-c", "4", "-q", "10", "--cpus", "1",
		"-n", fmt.Sprintf("%d", numRequests),
		"-m", "GET", "-t", "1",
		"-host", "example.com",
		"http://gw.default.svc.cluster.local:8080",
	}

	cmd := kCli.Command(s.Ctx, args...)

	if err := cmd.Start(); err != nil {
		s.T().Fatal("error starting command", err)
	}

	restartFunc()

	if err := cmd.Wait(); err != nil {
		s.T().Fatal("error waiting for command to finish", err)
	}

	output := string(cmd.Output())
	s.Contains(output, fmt.Sprintf("[200]\t%d responses", numRequests))
	s.NotContains(output, "Error distribution")
}

func (s *testingSuiteKgateway) TestZeroDowntimeRollout() {
	// Ensure the gateway pod is up and running.
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodsRunning(s.Ctx,
		"default", metav1.ListOptions{
			LabelSelector: defaults.WellKnownAppLabel + "=" + gatewayName,
		})

	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: 200},
		curl.WithHostHeader("example.com"),
		curl.WithPort(8080),
	)

	kCli := kubectl.NewCli()

	// Send traffic while restarting the gateway (envoy) deployment twice.
	// 800 req @ 4 concurrency / 10 qps = 20s (3 * terminationGracePeriodSeconds (5) + buffer).
	s.startTrafficAndAssertNoErrors(800, func() {
		err := kCli.RestartDeploymentAndWait(s.Ctx, "gw")
		s.Require().NoError(err)

		time.Sleep(time.Second)

		err = kCli.RestartDeploymentAndWait(s.Ctx, "gw")
		s.Require().NoError(err)
	})
}

// TestZeroDowntimeControllerRestart verifies that restarting the kgateway
// controller does not cause errors on the data plane. When the controller
// restarts, envoy proxies reconnect to the new xDS server. Until the new
// controller builds and pushes xDS snapshots, envoy must continue serving
// traffic using its existing configuration. A failure here (typically NC
// response flags) indicates a gap in the xDS readiness gating.
func (s *testingSuiteKgateway) TestZeroDowntimeControllerRestart() {
	// Ensure the gateway pod is up and running.
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodsRunning(s.Ctx,
		"default", metav1.ListOptions{
			LabelSelector: defaults.WellKnownAppLabel + "=" + gatewayName,
		})

	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: 200},
		curl.WithHostHeader("example.com"),
		curl.WithPort(8080),
	)

	kCli := kubectl.NewCli()
	installNs := s.TestInstallation.Metadata.InstallNamespace

	// Send traffic while restarting the kgateway controller deployment.
	// The controller restart causes envoy proxies to reconnect to a new xDS
	// server. Envoy must keep serving with its cached config until the new
	// controller pushes a snapshot.
	// 800 req @ 4 concurrency / 10 qps = 20s, enough for a controller restart.
	s.startTrafficAndAssertNoErrors(800, func() {
		err := kCli.RestartDeploymentAndWait(s.Ctx, helpers.DefaultKgatewayDeploymentName, "-n", installNs)
		s.Require().NoError(err)

		time.Sleep(time.Second)

		err = kCli.RestartDeploymentAndWait(s.Ctx, helpers.DefaultKgatewayDeploymentName, "-n", installNs)
		s.Require().NoError(err)
	})
}
