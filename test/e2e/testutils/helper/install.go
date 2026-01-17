//go:build e2e

package helper

import (
	"fmt"
	"path/filepath"
	"runtime"

	"helm.sh/helm/v3/pkg/repo"

	"github.com/kgateway-dev/kgateway/v2/pkg/logging"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/pkg/version"
	"github.com/kgateway-dev/kgateway/v2/test/testutils"
)

const (
	defaultTestAssetDir   = "_test"
	HelmRepoIndexFileName = "index.yaml"
)

var (
	logger = logging.New("helper/install")
)

// Gets the absolute path to a locally-built helm chart. This assumes that the helm index has a reference
// to exactly one version of the helm chart. If assetDir is an empty string, it will default to "_test".
func GetLocalChartPath(chartName string, assetDir string) (string, error) {
	dir := assetDir
	if dir == "" {
		dir = defaultTestAssetDir
	}
	rootDir := testutils.GitRootDirectory()
	testAssetDir := filepath.Join(rootDir, dir)
	if !fsutils.IsDirectory(testAssetDir) {
		return "", fmt.Errorf("%s does not exist or is not a directory", testAssetDir)
	}

	version, err := getChartVersion(testAssetDir, chartName)
	if err != nil {
		return "", fmt.Errorf("getting Helm chart version: %w", err)
	}
	return filepath.Join(testAssetDir, fmt.Sprintf("%s-%s.tgz", chartName, version)), nil
}

// Parses the Helm index file and returns the version of the chart.
func getChartVersion(testAssetDir string, chartName string) (string, error) {
	// Find helm index file in test asset directory
	helmIndexPath := filepath.Join(testAssetDir, HelmRepoIndexFileName)
	helmIndex, err := repo.LoadIndexFile(helmIndexPath)
	if err != nil {
		return "", fmt.Errorf("parsing Helm index file: %w", err)
	}
	logger.Info("found Helm index file", "path", helmIndexPath)

	// Read and return version from helm index file
	if chartVersions, ok := helmIndex.Entries[chartName]; !ok {
		return "", fmt.Errorf("index file does not contain entry with key: %s", chartName)
	} else if len(chartVersions) == 0 || len(chartVersions) > 1 {
		return "", fmt.Errorf("expected a single entry with name [%s], found: %v", chartName, len(chartVersions))
	} else {
		version := chartVersions[0].Version
		logger.Info("version of Helm chart", "chart", chartName, "version", version)
		return version, nil
	}
}

// GetLocalImageTag returns the image tag for locally-built images.
// For snapshot/CI builds (built via goreleaser), images have architecture suffix
// (e.g., "1.0.0-ci1-amd64") because docker manifests don't work with --load.
// This function returns the version with the appropriate architecture suffix.
func GetLocalImageTag() string {
	v := version.Version
	if v == "" || v == version.UndefinedVersion {
		// During local development when version is not set via ldflags,
		// fall back to a dev tag with arch suffix
		return "dev-" + runtime.GOARCH
	}
	return v + "-" + runtime.GOARCH
}

// GetLocalImageHelmArgs returns the helm arguments needed to set the image tag
// for locally-built images. This should be appended to ExtraHelmArgs in install.Context.
func GetLocalImageHelmArgs() []string {
	return []string{"--set", "image.tag=" + GetLocalImageTag()}
}
