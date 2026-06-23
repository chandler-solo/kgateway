//go:build e2e

package xdsidentityrace

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/onsi/gomega"
	"github.com/stretchr/testify/suite"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kgateway-dev/kgateway/v2/test/controllerutils/admincli"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
)

var _ e2e.NewSuiteFunc = NewTestingSuite

// identityChangeLog is the controller log emitted when a connected Envoy's
// re-derived identity no longer matches the one its stream opened with. This
// line is the proof that the per-request re-derivation fired and closed the
// stream so the client could re-identify; it is absent in the pre-fix behavior
// (identity frozen on the first request).
const identityChangeLog = "xds client identity changed; closing stream"

// uccNameRE matches a UniqlyConnectedClient resource name belonging to the base
// "gateway" proxy in kgateway-base. The format is
// role~hash(AugmentedLabels)~namespace, i.e.
// kgateway-kube-gateway-api~<ns>~<gw>~<hash>~<ns>. Only the hash varies when the
// proxy pod's labels change, so capturing the full name lets us assert that the
// identity transitioned without recomputing the hash ourselves. The same name is
// the node-id key under which the proxy's xDS snapshot is published.
var uccNameRE = regexp.MustCompile(`kgateway-kube-gateway-api~kgateway-base~gateway~\d+~kgateway-base`)

// testingSuite exercises the xDS client-identity re-derivation end-to-end: a
// connected Envoy whose pod labels drift after the stream is established must
// have its stream closed and re-identified against current state, rather than
// serving config bound to the stale identity for the stream's lifetime.
//
// All signals are read from the controller admin API (KRT + xDS snapshots) and
// the controller logs, reached via port-forward. We deliberately avoid curling
// the gateway's LoadBalancer address, which is not routable from the host on
// local kind.
type testingSuite struct {
	*base.BaseTestingSuite
}

func NewTestingSuite(ctx context.Context, testInst *e2e.TestInstallation) suite.TestingSuite {
	return &testingSuite{
		BaseTestingSuite: base.NewBaseTestingSuite(ctx, testInst, setup, testCases),
	}
}

// TestReidentifyOnPodLabelDrift artificially manipulates the startup race the
// fix is designed to heal. The race: a stream's first xDS request can be
// processed before the pod informer has surfaced the proxy pod's full labels,
// freezing a stale identity. We can't control informer-vs-request timing in a
// live cluster, but the identity is a pure function of the pod's labels
// (resource name = role~hash(AugmentedLabels)~ns), so we inject a label the
// "first request missed" AFTER the stream is established. We then force an xDS
// push so the established stream re-derives, detects the drift, closes, and the
// Envoy reconnects under the corrected identity.
func (s *testingSuite) TestReidentifyOnPodLabelDrift() {
	ctx := s.Ctx
	a := s.TestInstallation.AssertionsT(s.T())

	// The stream is established when the proxy connects to xDS, which surfaces as
	// the proxy's UCC in the controller's KRT snapshot. Capture that identity.
	var preNames sets.Set[string]
	a.AssertKgatewayAdminApi(ctx, kgatewayDeploymentObjectMeta,
		func(ctx context.Context, adminClient *admincli.Client) {
			a.Gomega.Eventually(func(g gomega.Gomega) {
				preNames = proxyKrtNames(g, ctx, adminClient)
				g.Expect(preNames.Len()).To(gomega.BeNumerically(">", 0), "proxy UCC present (stream established)")
			}).WithContext(ctx).WithTimeout(60 * time.Second).WithPolling(2 * time.Second).Should(gomega.Succeed())
		})
	s.Require().NotEmpty(preNames, "expected at least one UCC identity for the proxy before drift")

	// Locate the single proxy pod whose labels feed the identity hash.
	proxyPods, err := s.TestInstallation.Actions.Kubectl().GetPodsInNsWithLabel(
		ctx, gwNamespace, "gateway.networking.k8s.io/gateway-name="+gwName)
	s.Require().NoError(err)
	s.Require().Len(proxyPods, 1, "expected exactly one gateway proxy pod")
	proxyPod := proxyPods[0]

	controllerPod := s.controllerPod(ctx)

	// Baseline the identity-change log count so we detect a NEW occurrence caused
	// by our drift, not any pre-existing churn earlier in the controller's life.
	baseLogs, err := s.TestInstallation.Actions.Kubectl().GetContainerLogs(ctx, controllerNamespace, controllerPod)
	s.Require().NoError(err, "read controller logs")
	baselineCount := strings.Count(baseLogs, identityChangeLog)

	// Inject the label the first request "lost the race" on. augmentPodLabels
	// clones all pod labels into AugmentedLabels, so this changes the identity
	// hash. A unique value guarantees a hash change even when re-running against a
	// reused cluster where a prior run already added the key.
	labelValue := fmt.Sprintf("r%d", time.Now().UnixNano())
	defer func() {
		// Best-effort: leave the shared proxy pod as we found it for later suites.
		_, _, _ = s.TestInstallation.Actions.Kubectl().Execute(ctx,
			"label", "pod", "-n", gwNamespace, proxyPod, "xdsracetest-")
	}()
	_, _, err = s.TestInstallation.Actions.Kubectl().Execute(ctx,
		"label", "pod", "-n", gwNamespace, proxyPod, "xdsracetest="+labelValue, "--overwrite")
	s.Require().NoError(err, "label the proxy pod")

	// A bare pod-label change does not push (the UCC collection reads pods
	// point-in-time, outside KRT deps), so identity is only re-derived when a
	// DiscoveryRequest arrives. Toggling route2 produces config pushes; each ACK
	// drives a re-derivation. We loop for liveness in case an early ACK races
	// ahead of the informer surfacing the new label. route2 is not part of any
	// TestCase, so clean it up ourselves.
	defer func() { _ = s.TestInstallation.Actions.Kubectl().DeleteFile(ctx, route2Manifest) }()
	route2Applied := false
	toggleRoute2 := func() {
		if route2Applied {
			_ = s.TestInstallation.Actions.Kubectl().DeleteFile(ctx, route2Manifest)
		} else {
			_ = s.TestInstallation.Actions.Kubectl().ApplyFile(ctx, route2Manifest)
		}
		route2Applied = !route2Applied
	}

	// PRIMARY assertion: the controller closes the stream so the client
	// re-identifies. Absent without the fix (identity frozen on first request).
	found := false
	for i := 0; i < 40 && !found; i++ {
		toggleRoute2()
		time.Sleep(2 * time.Second)
		logs, err := s.TestInstallation.Actions.Kubectl().GetContainerLogs(ctx, controllerNamespace, controllerPod)
		s.Require().NoError(err, "read controller logs")
		if strings.Count(logs, identityChangeLog) > baselineCount {
			found = true
		}
	}
	s.Require().True(found,
		"controller should have logged %q after the proxy pod's labels drifted; without per-request "+
			"identity re-derivation the stream stays bound to the stale identity", identityChangeLog)

	// Leave route2 applied so it is part of the post-drift snapshot.
	if !route2Applied {
		s.Require().NoError(s.TestInstallation.Actions.Kubectl().ApplyFile(ctx, route2Manifest))
	}

	// SUPPORTING + HEAL assertion: the identity transitioned (a new UCC name
	// appears and the stale one is released), and the reconnect re-identified
	// against current state — proven by a published xDS snapshot keyed by the new
	// identity.
	a.AssertKgatewayAdminApi(ctx, kgatewayDeploymentObjectMeta,
		func(ctx context.Context, adminClient *admincli.Client) {
			a.Gomega.Eventually(func(g gomega.Gomega) {
				postNames := proxyKrtNames(g, ctx, adminClient)
				newNames := postNames.Difference(preNames)
				g.Expect(newNames.Len()).To(gomega.BeNumerically(">", 0),
					"a new UCC identity should exist after the label drift")
				g.Expect(preNames.Difference(postNames).Len()).To(gomega.BeNumerically(">", 0),
					"the stale UCC identity should be released after the stream closes")

				xdsNodes := proxyXdsNodes(g, ctx, adminClient)
				g.Expect(xdsNodes.Intersection(newNames).Len()).To(gomega.BeNumerically(">", 0),
					"an xDS snapshot should be published for the re-identified client")
			}).WithContext(ctx).WithTimeout(90 * time.Second).WithPolling(3 * time.Second).Should(gomega.Succeed())
		})
}

// proxyKrtNames returns the set of UniqlyConnectedClient resource names for the
// base gateway proxy found in the controller KRT snapshot.
func proxyKrtNames(g gomega.Gomega, ctx context.Context, adminClient *admincli.Client) sets.Set[string] {
	snap, err := adminClient.GetKrtSnapshot(ctx)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "can get krt snapshot")
	g.Expect(snap).NotTo(gomega.BeEmpty(), "krt snapshot is not empty")
	out := sets.New[string]()
	for _, m := range uccNameRE.FindAllString(snap, -1) {
		out.Insert(m)
	}
	return out
}

// proxyXdsNodes returns the set of xDS snapshot node-id keys for the base
// gateway proxy. A key present here means a snapshot is published for that
// identity.
func proxyXdsNodes(g gomega.Gomega, ctx context.Context, adminClient *admincli.Client) sets.Set[string] {
	snap, err := adminClient.GetXdsSnapshot(ctx)
	g.Expect(err).NotTo(gomega.HaveOccurred(), "can get xds snapshot")
	out := sets.New[string]()
	for k := range snap {
		if uccNameRE.MatchString(k) {
			out.Insert(k)
		}
	}
	return out
}

func (s *testingSuite) controllerPod(ctx context.Context) string {
	pods, err := s.TestInstallation.Actions.Kubectl().GetPodsInNsWithLabel(ctx, controllerNamespace, controllerSelector)
	s.Require().NoError(err)
	s.Require().NotEmpty(pods, "expected a kgateway controller pod")
	return pods[0]
}
