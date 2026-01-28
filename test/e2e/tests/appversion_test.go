//go:build e2e

package tests_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/envutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/features/appversion"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/testutils/install"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

// TestKgatewayAppVersion runs parameterized tests to verify that kgateway
// can be installed and deploy proxies with both 'vX.Y.Z' and 'X.Y.Z'
// version formats. Uses TEST_IMAGE_TAG env var (default: 1.0.0-ci1).
func TestKgatewayAppVersion(t *testing.T) {
	baseVersion, _ := envutils.LookupOrDefault("TEST_IMAGE_TAG", "1.0.0-ci1")
	// Strip leading 'v' if present to get base version
	if len(baseVersion) > 0 && baseVersion[0] == 'v' {
		baseVersion = baseVersion[1:]
	}

	versions := []struct {
		name     string
		imageTag string
	}{
		{"with_v_prefix", "v" + baseVersion},
		{"without_v_prefix", baseVersion},
	}

	for _, v := range versions {
		t.Run(v.name, func(t *testing.T) {
			runKgatewayAppVersionTest(t, v.imageTag)
		})
	}
}

func runKgatewayAppVersionTest(t *testing.T, imageTag string) {
	ctx := context.Background()
	installNs, nsEnvPredefined := envutils.LookupOrDefault(testutils.InstallNamespace, "kgateway-appversion-test")

	testInstallation := e2e.CreateTestInstallation(
		t,
		&install.Context{
			InstallNamespace:          installNs,
			ProfileValuesManifestFile: e2e.CommonRecommendationManifest,
			ValuesManifestFile:        e2e.EmptyValuesManifestPath,
			ExtraHelmArgs: []string{
				"--set", "image.tag=" + imageTag,
			},
		},
	)

	if !nsEnvPredefined {
		os.Setenv(testutils.InstallNamespace, installNs)
	}

	testutils.Cleanup(t, func() {
		if !nsEnvPredefined {
			os.Unsetenv(testutils.InstallNamespace)
		}
		if t.Failed() {
			testInstallation.PreFailHandler(ctx)
		}
		testInstallation.UninstallKgateway(ctx)
	})

	testInstallation.InstallKgatewayFromLocalChart(ctx)

	suite.Run(t, appversion.NewTestingSuite(ctx, testInstallation))
}
