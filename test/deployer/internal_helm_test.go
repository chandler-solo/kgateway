package deployer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kgateway-dev/kgateway/v2/internal/kgateway/wellknown"
	"github.com/kgateway-dev/kgateway/v2/internal/version"
	pkgdeployer "github.com/kgateway-dev/kgateway/v2/pkg/deployer"
	"github.com/kgateway-dev/kgateway/v2/pkg/schemes"
	"github.com/kgateway-dev/kgateway/v2/pkg/utils/fsutils"
	"github.com/kgateway-dev/kgateway/v2/internal/version"
)

func mockVersion(t *testing.T) {
	// Save the original version and restore it after the test
	// This ensures the test uses a fixed version (1.0.0-ci1) regardless of
	// what VERSION was set when compiling the test binary
	originalVersion := version.Version
	version.Version = "1.0.0-ci1"
	t.Cleanup(func() {
		version.Version = originalVersion
	})
}

// helmChartTests returns the test cases for non-TLS Helm chart tests
func helmChartTests() []HelmTestCase {
	return []HelmTestCase{
		{
			Name:      "basic gateway with default gatewayclass and no gwparams",
			InputFile: "base-gateway",
		},
		{
			Name:      "gateway with replicas GWP via GWC",
			InputFile: "gwc-with-replicas",
		},
		{
			Name:      "gwparams with omitDefaultSecurityContext via GWC",
			InputFile: "omit-default-security-context",
		},
		{
			Name:      "gwparams with omitDefaultSecurityContext via GW",
			InputFile: "omit-default-security-context-via-gw",
		},
		{
			Name:      "gwparams with PDB via GW",
			InputFile: "pdb-via-gw",
		},
		{
			Name:      "agentgateway",
			InputFile: "agentgateway",
		},
		{
			Name:      "agentgateway OmitDefaultSecurityContext true GWP via GWC",
			InputFile: "agentgateway-omitdefaultsecuritycontext",
		},
		{
			Name:      "agentgateway OmitDefaultSecurityContext true GWP via GW",
			InputFile: "agentgateway-omitdefaultsecuritycontext-ref-gwp-on-gw",
		},
	}
}

// helmChartTestsWithTLS returns the test cases for TLS Helm chart tests
func helmChartTestsWithTLS() []HelmTestCase {
	return []HelmTestCase{
		{
			Name:      "basic gateway with TLS enabled",
			InputFile: "base-gateway-tls",
		},
		{
			Name:      "agentgateway with TLS enabled",
			InputFile: "agentgateway-tls",
		},
	}
}

// verifyAllYAMLFilesReferenced ensures every YAML file in testDataDir has a corresponding test case
func verifyAllYAMLFilesReferenced(t *testing.T, testDataDir string, testCases []HelmTestCase) {
	t.Helper()

	yamlFiles, err := filepath.Glob(filepath.Join(testDataDir, "*.yaml"))
	require.NoError(t, err, "failed to list YAML files in %s", testDataDir)

	referencedFiles := make(map[string]bool)
	for _, tc := range testCases {
		referencedFiles[tc.InputFile] = true
	}

	var unreferenced []string
	for _, yamlFile := range yamlFiles {
		baseName := filepath.Base(yamlFile)
		// Skip golden files
		if strings.HasSuffix(baseName, "-out.yaml") {
			continue
		}
		inputName := strings.TrimSuffix(baseName, ".yaml")
		if !referencedFiles[inputName] {
			unreferenced = append(unreferenced, baseName)
		}
	}

	require.Empty(t, unreferenced, "Found YAML files in %s without corresponding test cases: %v", testDataDir, unreferenced)
}

// runHelmChartTests is a helper function that runs a set of Helm chart tests.
// It handles the common setup and iteration logic for both TLS and non-TLS tests.
func runHelmChartTests(t *testing.T, tests []HelmTestCase, extraParams func(client.Client, *pkgdeployer.Inputs) pkgdeployer.HelmValuesGenerator) {
	mockVersion(t)

	tester := DeployerTester{
		ControllerName:    wellknown.DefaultGatewayControllerName,
		AgwControllerName: wellknown.DefaultAgwControllerName,
		ClassName:         wellknown.DefaultGatewayClassName,
		WaypointClassName: wellknown.DefaultWaypointClassName,
		AgwClassName:      wellknown.DefaultAgwClassName,
	}

	dir := fsutils.MustGetThisDir()
	scheme := schemes.GatewayScheme()

	for _, tt := range tests {
		t.Run(tt.Name, func(t *testing.T) {
			tester.RunHelmChartTest(t, tt, scheme, dir, extraParams)
		})
	}
}

// TestAllYAMLFilesReferenced verifies that all YAML files in testdata have corresponding test cases.
// This test runs first (alphabetically) to catch unreferenced files before running actual tests.
func TestAllYAMLFilesReferenced(t *testing.T) {
	// Collect all test cases from both test functions
	allTests := append(helmChartTests(), helmChartTestsWithTLS()...)

	dir := fsutils.MustGetThisDir()
	verifyAllYAMLFilesReferenced(t, filepath.Join(dir, "testdata"), allTests)
}

func TestRenderHelmChart(t *testing.T) {
	runHelmChartTests(t, helmChartTests(), nil)
}

func TestRenderHelmChartWithTLS(t *testing.T) {
	mockVersion(t)

	// Create temporary CA certificate file for testing
	caCertContent := `-----BEGIN CERTIFICATE-----
MIICljCCAX4CCQCKSGhvPtMNGzANBgkqhkiG9w0BAQsFADANMQswCQYDVQQGEwJV
UzAeFw0yNDA3MDEwMDAwMDBaFw0yNTA3MDEwMDAwMDBaMA0xCzAJBgNVBAYTAlVT
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEA1234567890ABCDEFGHIj
klmnopqrstuvwxyz1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890ab
cdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ123456
7890abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJKLMNOPQRSTUVWXYZ
1234567890abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJKLMNOPQRSTU
VWXYZ1234567890abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHIJKLMNO
PQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz1234567890ABCDEFGHI
JKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz1234567890ABC
DEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefghijklmnopqrstuvwxyz123456
wIDAQABMA0GCSqGSIb3DQEBCwUAA4IBAQBtestcertdata
-----END CERTIFICATE-----`

	tmpDir := t.TempDir()
	caCertPath := tmpDir + "/ca.crt"
	err := os.WriteFile(caCertPath, []byte(caCertContent), 0o600)
	require.NoError(t, err)

	// ExtraGatewayParameters function that enables TLS. This is needed as TLS
	// is injected by the control plane and not via the GWP API.
	tlsExtraParams := func(cli client.Client, inputs *pkgdeployer.Inputs) pkgdeployer.HelmValuesGenerator {
		inputs.ControlPlane.XdsTLS = true
		inputs.ControlPlane.XdsTlsCaPath = caCertPath
		return nil
	}

	runHelmChartTests(t, helmChartTestsWithTLS(), tlsExtraParams)
}
