//go:build e2e

package tests_test

import (
	"context"
	"os"
	"testing"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/envutils"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/features/loadtesting"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/testutils/install"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

func TestStartupBenchmark(t *testing.T) {
	ctx := context.Background()
	installNs, nsEnvPredefined := envutils.LookupOrDefault(testutils.InstallNamespace, "kgateway-startup-benchmark")
	testInstallation := e2e.CreateTestInstallation(
		t,
		&install.Context{
			InstallNamespace:          installNs,
			ProfileValuesManifestFile: e2e.CommonRecommendationManifest,
			ValuesManifestFile:        e2e.EmptyValuesManifestPath,
			ExtraHelmArgs: []string{
				"--set", "controller.extraEnv.KGW_GLOBAL_POLICY_NAMESPACE=" + installNs,
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

		testInstallation.UninstallKgateway(ctx, t)
	})

	testInstallation.InstallKgatewayFromLocalChart(ctx, t)

	startupBenchmarkSuiteRunner := e2e.NewSuiteRunner(false)
	startupBenchmarkSuiteRunner.Register("StartupBenchmark", loadtesting.NewStartupBenchmarkSuite)
	startupBenchmarkSuiteRunner.Run(ctx, t, testInstallation)
}
