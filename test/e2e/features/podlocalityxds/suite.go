//go:build e2e

package podlocalityxds

import (
	"context"
	"net/http"
	"os"

	"github.com/stretchr/testify/suite"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/common"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
	"github.com/kgateway-dev/kgateway/v2/test/helpers"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

// disablePodLocalityEnv is the raw (non KGW_-prefixed) env var that selects the
// locality-agnostic xDS code path in the controller.
const disablePodLocalityEnv = "DISABLE_POD_LOCALITY_XDS"

// testingSuite verifies the locality-agnostic xDS code path selected by
// DISABLE_POD_LOCALITY_XDS=true. On that path kgateway translates each backend
// once (client-agnostic) and assembles one snapshot per gateway role
// (snapshotPerRole) instead of fanning out per connected client. These tests
// assert the resulting snapshot is valid and servable end-to-end, and that
// config changes converge while the flag is set.
type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		BaseTestingSuite: base.NewBaseTestingSuite(ctx, testInst, setup, testCases),
	}
}

// SetupSuite switches the controller to the locality-agnostic xDS path before the
// base suite applies the routing manifests, so they are translated under that
// path. The env var (and the controller rollout) is reverted on teardown.
func (s *testingSuite) SetupSuite() {
	s.setControllerEnvVar(disablePodLocalityEnv, "true")
	s.BaseTestingSuite.SetupSuite()
}

// TestRoutingWithLocalityDisabled asserts that, with locality disabled, the base
// gateway serves a route to the shared backend — i.e. snapshotPerRole produced a
// coherent, Envoy-accepted snapshot.
func (s *testingSuite) TestRoutingWithLocalityDisabled() {
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithPath("/"),
		curl.WithHostHeader("locality.example.com"),
		curl.WithPort(80),
	)
}

// TestRouteUpdateConvergesWithLocalityDisabled asserts that a route added at
// runtime converges on the locality-agnostic path while the original route keeps
// serving — i.e. the per-role snapshot recomputes on listener/route changes.
func (s *testingSuite) TestRouteUpdateConvergesWithLocalityDisabled() {
	// The setup route still serves.
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithPath("/"),
		curl.WithHostHeader("locality.example.com"),
		curl.WithPort(80),
	)
	// The route added by this test case's manifest converges.
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithPath("/extra"),
		curl.WithHostHeader("locality-extra.example.com"),
		curl.WithPort(80),
	)
}

// setControllerEnvVar appends an env var to the kgateway controller Deployment,
// waits for the rollout, and registers a cleanup that reverts it. Mirrors the
// pattern used by the waypoint suite.
func (s *testingSuite) setControllerEnvVar(name, value string) {
	controllerNamespace, ok := os.LookupEnv(testutils.InstallNamespace)
	if !ok {
		s.FailNow(testutils.InstallNamespace + " environment variable not set")
	}

	original := &appsv1.Deployment{}
	err := s.TestInstallation.ClusterContext.Client.Get(s.Ctx, client.ObjectKey{
		Namespace: controllerNamespace,
		Name:      helpers.DefaultKgatewayDeploymentName,
	}, original)
	s.Require().NoError(err, "get controller deployment")

	envVar := corev1.EnvVar{Name: name, Value: value}
	modified := original.DeepCopy()
	modified.Spec.Template.Spec.Containers[0].Env = append(
		modified.Spec.Template.Spec.Containers[0].Env,
		envVar,
	)
	modified.ResourceVersion = ""
	err = s.TestInstallation.ClusterContext.Client.Patch(s.Ctx, modified, client.MergeFrom(original))
	s.Require().NoError(err, "patch controller deployment with %s=%s", name, value)

	labelSelector := metav1.ListOptions{LabelSelector: defaults.WellKnownAppLabel + "=kgateway"}
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodContainerContainsEnvVar(
		s.Ctx,
		controllerNamespace,
		labelSelector,
		helpers.KgatewayContainerName,
		envVar,
	)

	testutils.Cleanup(s.T(), func() {
		original.ResourceVersion = ""
		err := s.TestInstallation.ClusterContext.Client.Patch(s.Ctx, original, client.MergeFrom(modified))
		s.Require().NoError(err, "revert controller deployment")
		s.TestInstallation.AssertionsT(s.T()).EventuallyPodContainerDoesNotContainEnvVar(
			s.Ctx,
			controllerNamespace,
			labelSelector,
			helpers.KgatewayContainerName,
			envVar.Name,
		)
	})

	// Wait for the controller pods to be running again after the patch.
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodsRunning(s.Ctx, controllerNamespace, labelSelector)
}
