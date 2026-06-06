//go:build e2e

package perclientxds

import (
	"path/filepath"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/tests/base"
)

var (
	setupManifest = filepath.Join(fsutils.MustGetThisDir(), "testdata", "setup.yaml")

	setup = base.TestCase{
		Manifests: []string{setupManifest},
	}

	// The tests mutate the backend at runtime (rollout / scale) rather than
	// applying per-test manifests, so there are no per-test cases.
	testCases = map[string]*base.TestCase{}
)
