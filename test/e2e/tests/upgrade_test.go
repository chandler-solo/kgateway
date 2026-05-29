//go:build e2e

package tests_test

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/v67/github"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/cmdutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/envutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/threadsafe"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	. "github.com/kgateway-dev/kgateway/v2/test/e2e/tests"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/testutils/install"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

func FetchLatestTag(ctx context.Context) (string, *cmdutils.RunError) {
	var output threadsafe.Buffer
	err := cmdutils.Command(ctx, "git", "describe", "--tags", "--abbrev=0").
		WithStdout(&output).
		WithStderr(&output).
		Run()
	return strings.TrimSpace(output.String()), err
}

func FetchPreviousMinorRelease(ctx context.Context, latestRelease string) (string, error) {
	parts := strings.Split(latestRelease, ".")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected tag format: %s", latestRelease)
	}
	minorInt, convErr := strconv.Atoi(parts[1])
	if convErr != nil {
		return "", fmt.Errorf("failed to parse minor version from tag %q: %v", latestRelease, convErr)
	}
	previousMinorPrefix := fmt.Sprintf("%s.%d", parts[0], minorInt-1)
	return fetchLatestGithubRelease(ctx, previousMinorPrefix)
}

func fetchLatestGithubRelease(ctx context.Context, prefix string) (string, error) {
	client := github.NewClient(nil)
	opts := &github.ListOptions{PerPage: 100}
	for {
		releases, resp, err := client.Repositories.ListReleases(ctx, "kgateway-dev", "kgateway", opts)
		if err != nil {
			return "", fmt.Errorf("fetch releases: %w", err)
		}
		for _, r := range releases {
			if r.GetDraft() || r.GetPrerelease() {
				continue
			}

			if r.TagName != nil && strings.HasPrefix(*r.TagName, prefix) {
				return *r.TagName, nil
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return "", fmt.Errorf("no release found with prefix %q", prefix)
}

func TestUpgrade(t *testing.T) {
	latestTag, err := FetchLatestTag(t.Context())
	if err != nil {
		t.Fatalf("failed to get latestPatchVersion.\n\tError: %v.\n\tCommand: %s\n\tOutput: %s", err.Cause(), err.PrettyCommand(), err.OutputString())
	}
	t.Run(fmt.Sprintf("TestUpgradeFromLatestRelease [%s]", latestTag), func(t *testing.T) {
		testUpgrade(t, latestTag)
	})

	previousMinor, err1 := FetchPreviousMinorRelease(t.Context(), latestTag)
	if err1 != nil {
		t.Fatalf("failed to get previous minor release.\n\tError: %v", err1)
	}
	t.Run(fmt.Sprintf("TestUpgradeFromPreviousMinor[%s]", previousMinor), func(t *testing.T) {
		testUpgrade(t, previousMinor)
	})
}

func testUpgrade(t *testing.T, fromVersion string) {
	ctx := context.Background()
	installNs, nsEnvPredefined := envutils.LookupOrDefault(testutils.InstallNamespace, "kgateway-upgrade")
	testInstallation := e2e.CreateTestInstallation(
		t,
		&install.Context{
			InstallNamespace:          installNs,
			ProfileValuesManifestFile: e2e.CommonRecommendationManifest,
			ValuesManifestFile:        e2e.EmptyValuesManifestPath,
			ExtraHelmArgs:             []string{"--wait", "--wait-for-jobs"},
		},
	)

	if !nsEnvPredefined {
		os.Setenv(testutils.InstallNamespace, installNs)
	}

	// Register cleanup before installation so partial installs are also cleaned up.
	testutils.Cleanup(t, func() {
		if !nsEnvPredefined {
			os.Unsetenv(testutils.InstallNamespace)
		}
		if t.Failed() {
			testInstallation.PreFailHandler(ctx, t)
		}
		testInstallation.UninstallKgateway(ctx, t)
	})

	// Install the released version from the remote OCI registry.
	testInstallation.InstallKgatewayFromRelease(ctx, t, fromVersion)

	UpgradeSuiteRunner().Run(ctx, t, testInstallation)
}
