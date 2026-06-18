//go:build e2e

package podlocalityxds

import (
	"path/filepath"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
)

var (
	// manifests
	routeManifest      = filepath.Join(fsutils.MustGetThisDir(), "testdata", "route.yaml")
	extraRouteManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "route-extra.yaml")

	// setup routes the shared kgateway-base/backend echo service through the base
	// gateway. It is applied after the controller is switched to the
	// locality-agnostic xDS path, so the snapshot is built by snapshotPerRole.
	setup = base.TestCase{
		Manifests: []string{routeManifest},
	}

	testCases = map[string]*base.TestCase{
		"TestRoutingWithLocalityDisabled": {},
		"TestRouteUpdateConvergesWithLocalityDisabled": {
			Manifests: []string{extraRouteManifest},
		},
	}
)
