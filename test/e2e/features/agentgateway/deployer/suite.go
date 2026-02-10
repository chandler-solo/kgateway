//go:build e2e

package deployer

import (
	"context"
	"time"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/agentgateway"
	"github.com/kgateway-dev/kgateway/v2/api/v1alpha1/shared"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

var (
	testCases = map[string]*base.TestCase{
		"TestAgentgatewayParametersUpdateTriggersReconciliation": {
			Manifests: []string{agwWithParameters},
		},
	}
)

type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		base.NewBaseTestingSuite(ctx, testInst, base.TestCase{}, testCases),
	}
}

func (s *testingSuite) TestAgentgatewayParametersUpdateTriggersReconciliation() {
	// Wait for the gateway deployment to be ready
	s.TestInstallation.AssertionsT(s.T()).EventuallyReadyReplicas(s.Ctx, agwProxyObjectMeta, gomega.Equal(1))

	// Patch AgentgatewayParameters to change the deployment label
	s.patchAgentgatewayParameters(agwParamsObjectMeta, func(params *agentgateway.AgentgatewayParameters) {
		if params.Spec.Deployment == nil {
			params.Spec.Deployment = &shared.KubernetesResourceOverlay{}
		}
		if params.Spec.Deployment.Metadata == nil {
			params.Spec.Deployment.Metadata = &shared.ObjectMetadata{}
		}
		params.Spec.Deployment.Metadata.Labels = map[string]string{
			"test-label": "v2",
		}
	})

	// Verify the deployment's metadata labels are updated,
	// which proves that the AgentgatewayParameters change triggered reconciliation
	s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
		deployment := &appsv1.Deployment{}
		err := s.TestInstallation.ClusterContext.Client.Get(s.Ctx, client.ObjectKey{
			Namespace: agwProxyObjectMeta.Namespace,
			Name:      agwProxyObjectMeta.Name,
		}, deployment)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(deployment.Labels).To(
			gomega.HaveKeyWithValue("test-label", "v2"),
			"deployment should have the updated label from AgentgatewayParameters patch",
		)
	}).WithTimeout(60 * time.Second).WithPolling(1 * time.Second).Should(gomega.Succeed())
}

// patchAgentgatewayParameters accepts a reference to an object, and a patch function.
// It then queries the object, performs the patch in memory, and writes the object back to the cluster.
func (s *testingSuite) patchAgentgatewayParameters(objectMeta metav1.ObjectMeta, patchFn func(*agentgateway.AgentgatewayParameters)) {
	params := &agentgateway.AgentgatewayParameters{}
	err := s.TestInstallation.ClusterContext.Client.Get(s.Ctx, client.ObjectKey{
		Name:      objectMeta.GetName(),
		Namespace: objectMeta.GetNamespace(),
	}, params)
	s.Assert().NoError(err, "can query the AgentgatewayParameters object")
	modified := params.DeepCopy()

	patchFn(modified)

	err = s.TestInstallation.ClusterContext.Client.Patch(s.Ctx, modified, client.MergeFrom(params))
	s.Assert().NoError(err, "can update the AgentgatewayParameters object")
}
