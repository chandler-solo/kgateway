//go:build e2e

package xdsidentityrace

import (
	"path/filepath"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
	"github.com/kgateway-dev/kgateway/v2/test/helpers"
)

var (
	// manifests
	serviceManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "service.yaml")
	route1Manifest  = filepath.Join(fsutils.MustGetThisDir(), "testdata", "route1.yaml")
	// route2 is applied mid-test (not at BeforeTest) to force an xDS push onto the
	// already-established stream, so it is intentionally not part of any TestCase.
	route2Manifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "route2.yaml")

	// The shared gateway proxy deployment created by the deployer for the base
	// "gateway" Gateway in kgateway-base.
	gwNamespace = "kgateway-base"
	gwName      = "gateway"

	// The kgateway controller, whose logs and KRT/admin snapshot we inspect.
	controllerNamespace = "kgateway-test"
	controllerSelector  = "app.kubernetes.io/name=" + helpers.DefaultKgatewayDeploymentName

	kgatewayDeploymentObjectMeta = metav1.ObjectMeta{
		Name:      helpers.DefaultKgatewayDeploymentName,
		Namespace: controllerNamespace,
	}

	setup = base.TestCase{
		Manifests: []string{serviceManifest, route1Manifest},
	}

	testCases = map[string]*base.TestCase{
		// route2 is applied by the test body, so no per-test manifests here.
		"TestReidentifyOnPodLabelDrift": {},
	}
)
