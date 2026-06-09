//go:build e2e

package basicrouting

import (
	"context"
	"net/http"
	"path/filepath"
	"time"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/kubeutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/common"
	testdefaults "github.com/kgateway-dev/kgateway/v2/test/e2e/defaults"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	"github.com/kgateway-dev/kgateway/v2/test/envoyutils/admincli"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

var (
	// manifests
	serviceManifest               = filepath.Join(fsutils.MustGetThisDir(), "testdata", "service.yaml")
	headlessServiceManifest       = filepath.Join(fsutils.MustGetThisDir(), "testdata", "headless-service.yaml")
	gatewayWithRouteManifest      = filepath.Join(fsutils.MustGetThisDir(), "testdata", "gateway-with-route.yaml")
	longHTTPRouteManifest         = filepath.Join(fsutils.MustGetThisDir(), "testdata", "long-httproute.yaml")
	samePrefixLongGatewayManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "gateway-with-same-prefix-80char-names.yaml")

	// test cases
	setup = base.TestCase{
		Manifests: []string{
			gatewayWithRouteManifest,
		},
	}
	testCases = map[string]*base.TestCase{
		"TestGatewayWithRoute": {
			Manifests: []string{serviceManifest},
		},
		"TestBackendDeletionAndReapplyUpdatesEnvoyClusters": {
			Manifests: []string{serviceManifest},
		},
		"TestHeadlessService": {
			Manifests: []string{headlessServiceManifest},
		},
		"TestLongHTTPRouteName": {
			Manifests: []string{longHTTPRouteManifest},
		},
		"TestSamePrefixLongGatewayNameRouting": {
			Manifests: []string{serviceManifest, samePrefixLongGatewayManifest},
		},
	}

	listenerHighPort = 8080
	listenerLowPort  = 80

	exampleService = &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "example-svc",
			Namespace: "default",
		},
	}
	nginxPod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "nginx",
			Namespace: "default",
		},
	}
	proxyObjectMeta = metav1.ObjectMeta{
		Name:      "gateway",
		Namespace: "default",
	}
)

// testingSuite is a suite of basic routing / "happy path" tests
type testingSuite struct {
	*base.BaseTestingSuite
	localGateway common.Gateway
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		base.NewBaseTestingSuite(ctx, testInst, setup, testCases),
		common.Gateway{}, // initialized in SetupSuite
	}
}

func (s *testingSuite) SetupSuite() {
	s.BaseTestingSuite.SetupSuite()

	// Initialize local gateway for this test
	address := s.TestInstallation.AssertionsT(s.T()).EventuallyGatewayAddress(
		s.Ctx,
		"gateway",
		"default",
	)
	s.localGateway = common.Gateway{
		NamespacedName: types.NamespacedName{
			Name:      "gateway",
			Namespace: "default",
		},
		Address: address,
	}
}

func (s *testingSuite) TestGatewayWithRoute() {
	s.assertSuccessfulResponse()
}

func (s *testingSuite) TestBackendDeletionAndReapplyUpdatesEnvoyClusters() {
	const clusterName = "kube_default_example-svc_8080"

	s.assertEnvoyClusterPresence(clusterName, true)

	err := s.TestInstallation.Actions.Kubectl().DeleteFileSafe(s.Ctx, serviceManifest)
	s.Require().NoError(err, "can delete backend service fixture")
	s.TestInstallation.AssertionsT(s.T()).EventuallyObjectsNotExist(s.Ctx, exampleService, nginxPod)
	s.assertEnvoyClusterPresence(clusterName, false)

	err = s.TestInstallation.Actions.Kubectl().ApplyFile(s.Ctx, serviceManifest)
	s.Require().NoError(err, "can reapply backend service fixture")
	s.TestInstallation.AssertionsT(s.T()).EventuallyObjectsExist(s.Ctx, exampleService, nginxPod)
	s.TestInstallation.AssertionsT(s.T()).EventuallyPodsRunning(s.Ctx, nginxPod.GetNamespace(), metav1.ListOptions{
		LabelSelector: testdefaults.WellKnownAppLabel + "=" + nginxPod.GetName(),
	}, time.Minute, 500*time.Millisecond)
	s.assertEnvoyClusterPresence(clusterName, true)
}

func (s *testingSuite) TestHeadlessService() {
	s.assertSuccessfulResponse()
}

func (s *testingSuite) TestLongHTTPRouteName() {
	s.localGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
		},
		curl.WithHostHeader("long.example.com"),
		curl.WithPort(80),
	)
}

func (s *testingSuite) TestSamePrefixLongGatewayNameRouting() {
	const (
		gwNameOne = "very-long-gateway-name-for-testing-80-char-limit-exactly-this-many-chars-aaa-one"
		gwNameTwo = "very-long-gateway-name-for-testing-80-char-limit-exactly-this-many-chars-bbb-two"
	)

	// Verify the two long names with the same prefix produce different safe names
	s.Require().NotEqual(kubeutils.SafeGatewayLabelValue(gwNameOne), kubeutils.SafeGatewayLabelValue(gwNameTwo))

	// Get addresses for both Gateways
	firstGateway := common.Gateway{
		NamespacedName: types.NamespacedName{Name: gwNameOne, Namespace: "default"},
		Address:        s.TestInstallation.AssertionsT(s.T()).EventuallyGatewayAddress(s.Ctx, gwNameOne, "default"),
	}
	secondGateway := common.Gateway{
		NamespacedName: types.NamespacedName{Name: gwNameTwo, Namespace: "default"},
		Address:        s.TestInstallation.AssertionsT(s.T()).EventuallyGatewayAddress(s.Ctx, gwNameTwo, "default"),
	}

	// Verify routing works for both Gateways independently
	firstGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
			Body:       gomega.ContainSubstring(testdefaults.NginxResponse),
		},
		curl.WithHostHeader("long-80-a.example.com"),
		curl.WithPort(8080),
	)
	secondGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{
			StatusCode: http.StatusOK,
			Body:       gomega.ContainSubstring(testdefaults.NginxResponse),
		},
		curl.WithHostHeader("long-80-b.example.com"),
		curl.WithPort(8080),
	)
}

func (s *testingSuite) assertSuccessfulResponse() {
	s.assertSuccessfulResponseOnPorts(listenerHighPort, listenerLowPort)
}

func (s *testingSuite) assertSuccessfulResponseOnPorts(ports ...int) {
	for _, port := range ports {
		s.localGateway.Send(
			s.T(),
			&testmatchers.HttpResponse{
				StatusCode: http.StatusOK,
				Body:       gomega.ContainSubstring(testdefaults.NginxResponse),
			},
			curl.WithHostHeader("example.com"),
			curl.WithPort(port),
		)
	}
}

func (s *testingSuite) assertEnvoyClusterPresence(clusterName string, shouldExist bool) {
	s.TestInstallation.AssertionsT(s.T()).AssertEnvoyAdminApi(s.Ctx, proxyObjectMeta, func(ctx context.Context, adminClient *admincli.Client) {
		s.TestInstallation.AssertionsT(s.T()).Gomega.Eventually(func(g gomega.Gomega) {
			clusters, err := adminClient.GetDynamicClusters(ctx)
			g.Expect(err).NotTo(gomega.HaveOccurred(), "can get dynamic clusters")
			_, ok := clusters[clusterName]
			g.Expect(ok).To(gomega.Equal(shouldExist), "cluster %s presence should be %t", clusterName, shouldExist)
		}).
			WithContext(ctx).
			WithTimeout(120 * time.Second).
			WithPolling(2 * time.Second).
			Should(gomega.Succeed())
	})
}
