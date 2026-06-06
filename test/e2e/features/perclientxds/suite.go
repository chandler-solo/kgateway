//go:build e2e

package perclientxds

import (
	"context"
	"net/http"

	"github.com/stretchr/testify/suite"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/requestutils/curl"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/common"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	testmatchers "github.com/kgateway-dev/kgateway/v2/test/gomega/matchers"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

const (
	backendNamespace = "kgateway-base"
	backendDeploy    = "deploy/echo-perclient"
	routeHostname    = "perclient.example.com"
)

// testingSuite is a regression test for the endpoint-follow symptom reported in
// #14184: after a backend's endpoints change (a rollout that re-IPs the pods, or a
// scale), the gateway must route to the current endpoints rather than stay stuck on
// stale ones.
//
// Scope note (calibrated against the production diagnostics for #14184): the
// evidenced defect lives in the per-client CLUSTERS pipeline — missing
// (client, backend) rows in PerClientEnvoyClusters — while the endpoints pipeline
// was healthy throughout. This suite exercises the endpoint-follow path end to end
// and would NOT have failed during that incident; the cluster-hole mechanism and
// its heartbeat heal are covered by unit tests in pkg/kgateway/proxy_syncer
// (perclient_heartbeat_test.go), because the hole formation is timing-dependent
// and not reliably reproducible in e2e.
type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		BaseTestingSuite: base.NewBaseTestingSuite(ctx, testInst, setup, testCases),
	}
}

// sendExpectOK drives traffic through the shared base gateway to the per-client
// backend and requires a 200. common.BaseGateway.Send retries, so it tolerates the
// brief windows during a rollout/scale where endpoints are still converging.
func (s *testingSuite) sendExpectOK() {
	common.BaseGateway.Send(
		s.T(),
		&testmatchers.HttpResponse{StatusCode: http.StatusOK},
		curl.WithHostHeader(routeHostname),
		curl.WithPath("/"),
		curl.WithPort(80),
	)
}

// Endpoints must follow a backend rollout. The rollout replaces the single pod with
// a new one on a new IP; a 200 after the rollout completes means the gateway picked
// up the new endpoint instead of staying stuck on the old (now-gone) one.
func (s *testingSuite) TestEndpointsFollowBackendRollout() {
	s.sendExpectOK()

	err := s.TestInstallation.Actions.Kubectl().RunCommand(s.Ctx,
		"rollout", "restart", backendDeploy, "-n", backendNamespace)
	s.Require().NoError(err)
	// Wait until the old pod is gone and the new one is ready, so the subsequent
	// request can only succeed if the gateway is using the new endpoint.
	err = s.TestInstallation.Actions.Kubectl().RunCommand(s.Ctx,
		"rollout", "status", backendDeploy, "-n", backendNamespace, "--timeout=90s")
	s.Require().NoError(err)

	s.sendExpectOK()
}

// Endpoints must follow backend scale up and back down. Scale waits for the
// deployment to be available, so each request runs against the converged replica set.
func (s *testingSuite) TestEndpointsFollowBackendScale() {
	s.sendExpectOK()

	err := s.TestInstallation.Actions.Kubectl().Scale(s.Ctx, backendNamespace, backendDeploy, 3)
	s.Require().NoError(err)
	s.sendExpectOK()

	err = s.TestInstallation.Actions.Kubectl().Scale(s.Ctx, backendNamespace, backendDeploy, 1)
	s.Require().NoError(err)
	s.sendExpectOK()
}
