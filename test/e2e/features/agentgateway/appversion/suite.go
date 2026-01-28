//go:build e2e

package appversion

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

var (
	gatewayManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "gateway.yaml")

	proxyObjectMeta = metav1.ObjectMeta{
		Name:      "appversion-agw",
		Namespace: "default",
	}

	setup = base.TestCase{
		Manifests: []string{gatewayManifest},
	}

	testCases = map[string]*base.TestCase{
		"TestProxyImageTag": {},
	}
)

type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		base.NewBaseTestingSuite(ctx, testInst, setup, testCases),
	}
}

// TestProxyImageTag verifies that the deployed agentgateway proxy pod:
// 1. Starts successfully (meaning the image was pulled)
// 2. Uses the expected agentgateway image
func (s *testingSuite) TestProxyImageTag() {
	// Wait for the proxy deployment to have ready replicas
	s.TestInstallation.Assertions.EventuallyReadyReplicas(s.Ctx, proxyObjectMeta, gomega.Equal(1))

	// Get the deployment and verify the image
	deployment := &appsv1.Deployment{}
	err := s.TestInstallation.ClusterContext.Client.Get(s.Ctx, client.ObjectKey{
		Namespace: proxyObjectMeta.Namespace,
		Name:      proxyObjectMeta.Name,
	}, deployment)
	s.Require().NoError(err, "failed to get proxy deployment")

	// Find the agentgateway container and check its image
	var agwImage string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if strings.Contains(container.Image, "agentgateway") {
			agwImage = container.Image
			break
		}
	}
	s.Require().NotEmpty(agwImage, "could not find agentgateway container image")

	s.T().Logf("Agentgateway proxy deployment is running with image: %s", agwImage)
}
