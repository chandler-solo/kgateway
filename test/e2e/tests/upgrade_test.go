//go:build e2e

package tests_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-github/v67/github"
	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"

	"github.com/kgateway-dev/kgateway/v2/pkg/utils/cmdutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/envutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/threadsafe"
	"github.com/kgateway-dev/kgateway/v2/test/e2e"
	. "github.com/kgateway-dev/kgateway/v2/test/e2e/tests"
	"github.com/kgateway-dev/kgateway/v2/test/e2e/testutils/install"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

func newGraphQLClient() *githubv4.Client {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return githubv4.NewClient(http.DefaultClient)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	return githubv4.NewClient(oauth2.NewClient(context.Background(), ts))
}

// fetchGithubRelease pages through kgateway releases ordered by creation date descending
// (newest first, skipping drafts) using the GitHub GraphQL API, which
// guarantees the ordering via orderBy. Returns the tag name of the first release for
// which match returns true.
func fetchGithubRelease(ctx context.Context, match func(tagName string) (bool, error)) (string, error) {
	client := newGraphQLClient()

	var query struct {
		Repository struct {
			Releases struct {
				Nodes []struct {
					TagName      githubv4.String
					IsDraft      githubv4.Boolean
					IsPrerelease githubv4.Boolean
				}
				PageInfo struct {
					EndCursor   githubv4.String
					HasNextPage githubv4.Boolean
				}
			} `graphql:"releases(first: 100, after: $cursor, orderBy: {field: CREATED_AT, direction: DESC})"`
		} `graphql:"repository(owner: \"kgateway-dev\", name: \"kgateway\")"`
	}

	variables := map[string]interface{}{
		"cursor": (*githubv4.String)(nil),
	}

	for {
		if err := client.Query(ctx, &query, variables); err != nil {
			return "", fmt.Errorf("graphql query releases: %w", err)
		}
		for _, r := range query.Repository.Releases.Nodes {
			if bool(r.IsDraft) || string(r.TagName) == "" {
				continue
			}
			ok, err := match(string(r.TagName))
			if err != nil {
				return "", err
			}
			if ok {
				return string(r.TagName), nil
			}
		}
		if !bool(query.Repository.Releases.PageInfo.HasNextPage) {
			break
		}
		variables["cursor"] = githubv4.NewString(query.Repository.Releases.PageInfo.EndCursor)
	}
	return "", fmt.Errorf("no matching release found")
}

// FetchLatestRelease returns the most recent release tag that is an ancestor of HEAD.
// This mirrors `git describe --tags --abbrev=0` but works in shallow checkouts where
// tags are not fetched, by resolving HEAD via git then checking ancestry via the GitHub API.
func FetchLatestRelease(ctx context.Context) (string, error) {
	var shaOut threadsafe.Buffer
	if err := cmdutils.Command(ctx, "git", "rev-parse", "HEAD").
		WithStdout(&shaOut).
		WithStderr(&shaOut).
		Run(); err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	headSHA := strings.TrimSpace(shaOut.String())

	// Use REST client for CompareCommits — no GraphQL equivalent.
	restClient := github.NewClient(nil)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		restClient = github.NewClient(nil).WithAuthToken(token)
	}

	return fetchGithubRelease(ctx, func(tagName string) (bool, error) {
		// Compare tag...HEAD: status=="ahead" means HEAD is ahead of the tag (tag is an ancestor).
		comparison, _, err := restClient.Repositories.CompareCommits(ctx, "kgateway-dev", "kgateway", tagName, headSHA, nil)
		if err != nil {
			return false, fmt.Errorf("compare %s...%s: %w", tagName, headSHA, err)
		}
		return comparison.GetStatus() == "ahead" || comparison.GetStatus() == "identical", nil
	})
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
	return fetchGithubRelease(ctx, func(tagName string) (bool, error) {
		return strings.HasPrefix(tagName, previousMinorPrefix), nil
	})
}

func TestUpgrade(t *testing.T) {
	latestTag, err := FetchLatestRelease(t.Context())
	if err != nil {
		t.Fatalf("failed to get latest patch version: %v", err)
	}
	t.Run(fmt.Sprintf("TestUpgradeFromLatestRelease [%s]", latestTag), func(t *testing.T) {
		testUpgrade(t, latestTag)
	})

	previousMinor, err1 := FetchPreviousMinorRelease(t.Context(), latestTag)
	if err1 != nil {
		t.Fatalf("failed to get previous minor release: %v", err1)
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
