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
		Name:      "appversion-gw",
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

// TestProxyImageTag verifies that the deployed proxy pod:
// 1. Starts successfully (meaning the image was pulled)
// 2. Uses an image tag with the 'v' prefix
func (s *testingSuite) TestProxyImageTag() {
	// Wait for the proxy deployment to have ready replicas
	s.TestInstallation.Assertions.EventuallyReadyReplicas(s.Ctx, proxyObjectMeta, gomega.Equal(1))

	// Get the deployment and verify the image tag
	deployment := &appsv1.Deployment{}
	err := s.TestInstallation.ClusterContext.Client.Get(s.Ctx, client.ObjectKey{
		Namespace: proxyObjectMeta.Namespace,
		Name:      proxyObjectMeta.Name,
	}, deployment)
	s.Require().NoError(err, "failed to get proxy deployment")

	// Find the envoy container and check its image tag
	var envoyImage string
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if strings.Contains(container.Image, "envoy") || strings.Contains(container.Image, "kgateway") {
			envoyImage = container.Image
			break
		}
	}
	s.Require().NotEmpty(envoyImage, "could not find envoy/kgateway container image")

	// Extract the tag from the image (format: registry/repo:tag)
	parts := strings.Split(envoyImage, ":")
	s.Require().Len(parts, 2, "image should have format registry/repo:tag, got: %s", envoyImage)
	tag := parts[1]

	// Verify the tag has the expected format (starts with 'v' for semver, or is 'latest'/'dev')
	isValidTag := strings.HasPrefix(tag, "v") || tag == "latest" || tag == "dev"
	s.Assert().True(isValidTag, "image tag should start with 'v' for semver versions, got: %s", tag)

	s.T().Logf("Proxy deployment is running with image: %s", envoyImage)
}
