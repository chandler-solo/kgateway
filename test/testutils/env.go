package testutils

import (
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/envutils"
)

const (
	// SkipInstall can be used when you plan to re-run a test suite and want to skip the installation
	// and teardown of kgateway.
	SkipInstall = "SKIP_INSTALL"

	// PersistInstall is a convenience flag that combines SKIP_INSTALL_IF_PRESENT and SKIP_TEARDOWN.
	// It will install if nothing is present, but skip installation if charts are already installed,
	// and then skip teardown. Useful for local development - "just handle it" mode.
	PersistInstall = "PERSIST_INSTALL"

	// SkipInstallIfPresent can be used to skip installation if the helm charts appear to already be installed.
	// This is useful for local development to speed up test runs. The default behavior is to always install.
	SkipInstallIfPresent = "SKIP_INSTALL_IF_PRESENT"

	// SkipTeardown can be used to prevent test cases from uninstalling resources they install at setup time.
	// This is useful for local debugging where you want to inspect the state after tests run.
	SkipTeardown = "SKIP_TEARDOWN"

	// InstallNamespace is the namespace in which kgateway is installed
	InstallNamespace = "INSTALL_NAMESPACE"

	// SkipIstioInstall is a flag that indicates whether to skip the install of Istio.
	// This is used to test against an existing installation of Istio so that the
	// test framework does not need to install/uninstall Istio.
	SkipIstioInstall = "SKIP_ISTIO_INSTALL"

	// GithubAction is used by Github Actions and is the name of the currently running action or ID of a step
	// https://docs.github.com/en/actions/learn-github-actions/variables#default-environment-variables
	GithubAction = "GITHUB_ACTION"

	// ReleasedVersion can be used when running KubeE2E tests to have the test suite use a previously released version of kgateway
	// If set to 'LATEST', the most recently released version will be used
	// If set to another value, the test suite will use that version (ie '1.15.0-beta1')
	// This is an optional value, so if it is not set, the test suite will use the locally built version of kgateway
	ReleasedVersion = "RELEASED_VERSION"

	// ClusterName is the name of the cluster used for e2e tests
	ClusterName = "CLUSTER_NAME"

	// This can be used to override the default KubeCtx created.
	// The default KubeCtx used is "kind-<ClusterName>"
	KubeCtx = "KUBE_CTX"
)

// ShouldSkipInstall returns true if kgateway installation and teardown should be skipped.
func ShouldSkipInstall() bool {
	return envutils.IsEnvTruthy(SkipInstall)
}

// ShouldPersistInstall returns true if the install should be persisted across test runs.
// This combines the behavior of SKIP_INSTALL_IF_PRESENT and SKIP_TEARDOWN.
func ShouldPersistInstall() bool {
	return envutils.IsEnvTruthy(PersistInstall)
}

// ShouldSkipInstallIfPresent returns true if installation should be skipped when charts are already installed.
func ShouldSkipInstallIfPresent() bool {
	return envutils.IsEnvTruthy(SkipInstallIfPresent) || ShouldPersistInstall()
}

// ShouldSkipTeardown returns true if teardown should be skipped.
func ShouldSkipTeardown() bool {
	return envutils.IsEnvTruthy(SkipTeardown) || ShouldPersistInstall()
}

// ShouldSkipIstioInstall returns true if istio installation and teardown should be skipped.
func ShouldSkipIstioInstall() bool {
	return envutils.IsEnvTruthy(SkipIstioInstall)
}
