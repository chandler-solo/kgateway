//go:build e2e

package upgrade

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/stretchr/testify/suite"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils/kubectl"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/pkg/version"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/common"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
	"github.com/kgateway-dev/kgateway/v2/test/helpers"
)

var (
	_             e2e.NewSuiteFunc = NewTestingSuite
	setupManifest                  = filepath.Join(fsutils.MustGetThisDir(), "testdata", "setup.yaml")
)

// testingSuite validates that kgateway can be upgraded from a released version to the locally-built chart.
// The parent test function (TestUpgrade) is responsible for:
//   - Installing kgateway from the remote release before this suite runs.
//   - Uninstalling kgateway after this suite completes.
type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		base.NewBaseTestingSuite(ctx, testInst, base.TestCase{}, nil),
	}
}

func (s *testingSuite) SetupSuite() {
	s.BaseTestingSuite.SetupSuite()
	// kgateway was installed from a released version by the parent test function.
	// Verify it is healthy before attempting the upgrade.
	s.TestInstallation.AssertionsT(s.T()).EventuallyGatewayInstallSucceeded(s.Ctx)
}

func (s *testingSuite) applyManifests() func() {
	s.ApplyManifests(&base.TestCase{
		Manifests: []string{setupManifest, defaults.HttpbinManifest},
	})

	return func() {
		s.DeleteManifests(&base.TestCase{
			Manifests: []string{setupManifest, defaults.HttpbinManifest},
		})
	}
}

// TestUpgrade upgrades both the CRD chart and the controller chart from the previously installed
// remote release to the locally-built chart, then verifies the installation is healthy.
func (s *testingSuite) TestUpgrade() {
	// Create a gateway and ensure it works as expected
	cleanup := s.applyManifests()
	s.T().Cleanup(cleanup)
	common.SetupBaseGateway(s.Ctx, s.T(), s.TestInstallation, types.NamespacedName{
		Name:      "gateway",
		Namespace: "default",
	})
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithPath("/"),
		curl.WithHostHeader("example.com"),
		curl.WithPort(8080),
	)

	s.TestInstallation.InstallKgatewayCRDsFromLocalChart(s.Ctx, s.T())
	s.TestInstallation.InstallKgatewayCoreFromLocalChart(s.Ctx, s.T())
	s.TestInstallation.AssertionsT(s.T()).EventuallyKgatewayUpgradeSucceeded(s.Ctx, version.Version)
	s.assertNoKgatewayPodErrors()

	// Ensure the proxy pod is also updated
	// This should be updated to app.kubernetes.io/component=proxy. v2.2.x did not have this label
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodHasImageVersion(s.Ctx, "default", "app.kubernetes.io/managed-by=kgateway", version.Version)

	// Ensure the same gateway works after the upgrade
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithPath("/"),
		curl.WithHostHeader("example.com"),
		curl.WithPort(8080),
	)

	// Recreate the same gateway and ensure it works after the upgrade
	cleanup()
	s.applyManifests()
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithPath("/"),
		curl.WithHostHeader("example.com"),
		curl.WithPort(8080),
	)
}

// assertNoKgatewayPodErrors fetches logs from all kgateway pods and fails if any error-level log lines are found.
func (s *testingSuite) assertNoKgatewayPodErrors() {
	ns := s.TestInstallation.Metadata.InstallNamespace
	pods, err := s.TestInstallation.Actions.Kubectl().GetPodsInNsWithLabel(s.Ctx, ns, defaults.KGatewayPodLabel)
	s.Require().NoError(err, "failed to list kgateway pods in namespace %s", ns)
	s.Require().NotEmpty(pods, "no kgateway pods found in namespace %s", ns)

	for _, pod := range pods {
		logs, err := s.TestInstallation.Actions.Kubectl().GetContainerLogs(s.Ctx, ns, pod,
			kubectl.WithContainer(helpers.KgatewayContainerName))
		s.Require().NoError(err, "failed to get logs for pod %s", pod)

		for i, line := range strings.Split(logs, "\n") {
			lower := strings.ToLower(line)
			s.Assert().False(
				strings.Contains(lower, `"level":"error"`) || strings.Contains(lower, `"level": "error"`),
				"error log found in pod %s line %d: %s", pod, i+1, line,
			)
		}
	}
}
