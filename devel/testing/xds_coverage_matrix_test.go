package develtesting

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

type xdsCoverageMatrix struct {
	TranslatorFixtureRoots []string             `json:"translatorFixtureRoots"`
	Features               []xdsCoverageFeature `json:"features"`
}

type xdsCoverageFeature struct {
	Name               string   `json:"name"`
	Owner              string   `json:"owner"`
	Status             string   `json:"status"`
	Resources          []string `json:"resources"`
	TranslatorFixtures []string `json:"translatorFixtures"`
	TestFiles          []string `json:"testFiles"`
	E2ESuites          []string `json:"e2eSuites"`
	DynamicCases       []string `json:"dynamicCases"`
	Notes              string   `json:"notes"`
}

func TestXDSCoverageMatrix(t *testing.T) {
	repoRoot := repoRootFromPackageDir(t)
	matrix := readXDSCoverageMatrix(t)

	requireTranslatorFixtureRoots(t, repoRoot, matrix.TranslatorFixtureRoots)

	seenFeatureNames := make(map[string]struct{}, len(matrix.Features))
	for _, feature := range matrix.Features {
		t.Run(feature.Name, func(t *testing.T) {
			if feature.Name == "" {
				t.Fatal("feature name must not be empty")
			}
			if _, ok := seenFeatureNames[feature.Name]; ok {
				t.Fatalf("duplicate feature name %q", feature.Name)
			}
			seenFeatureNames[feature.Name] = struct{}{}
			if feature.Owner == "" {
				t.Fatal("feature owner must not be empty")
			}
			requireAllowed(t, "status", feature.Status, []string{"covered", "partial", "warning-only", "missing"})
			if len(feature.Resources) == 0 {
				t.Fatal("feature resources must not be empty")
			}
			for _, resource := range feature.Resources {
				requireAllowed(t, "resource", resource, []string{"ADS", "LDS", "RDS", "CDS", "EDS", "SDS"})
			}
			for _, path := range feature.TranslatorFixtures {
				requireExistingFile(t, repoRoot, path)
			}
			for _, path := range feature.TestFiles {
				requireExistingFile(t, repoRoot, path)
			}
			for _, path := range feature.E2ESuites {
				requireExistingDir(t, repoRoot, path)
			}
			if feature.Status == "covered" && len(feature.TestFiles) == 0 && len(feature.E2ESuites) == 0 {
				t.Fatal("covered feature must list at least one test file or e2e suite")
			}
			if feature.Status == "warning-only" && len(feature.DynamicCases) == 0 {
				t.Fatal("warning-only feature must describe the dynamic case")
			}
			if feature.Status == "missing" && feature.Notes == "" {
				t.Fatal("missing feature must include notes")
			}
		})
	}
	if len(matrix.Features) == 0 {
		t.Fatal("matrix must contain at least one feature")
	}
}

func readXDSCoverageMatrix(t *testing.T) xdsCoverageMatrix {
	t.Helper()

	data, err := os.ReadFile("xds-coverage-matrix.yaml")
	if err != nil {
		t.Fatalf("failed to read xDS coverage matrix: %v", err)
	}
	var matrix xdsCoverageMatrix
	if err := yaml.Unmarshal(data, &matrix); err != nil {
		t.Fatalf("failed to parse xDS coverage matrix: %v", err)
	}
	return matrix
}

func requireTranslatorFixtureRoots(t *testing.T, repoRoot string, listedRoots []string) {
	t.Helper()

	inputRoot := filepath.Join(repoRoot, "pkg/kgateway/translator/gateway/testutils/inputs")
	entries, err := os.ReadDir(inputRoot)
	if err != nil {
		t.Fatalf("failed to read translator input root: %v", err)
	}

	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			actual = append(actual, entry.Name())
		}
	}
	slices.Sort(actual)

	listed := slices.Clone(listedRoots)
	slices.Sort(listed)

	if !slices.Equal(actual, listed) {
		t.Fatalf("translator fixture roots differ\nactual: %v\nlisted: %v", actual, listed)
	}
}

func requireAllowed(t *testing.T, field, got string, allowed []string) {
	t.Helper()
	if slices.Contains(allowed, got) {
		return
	}
	t.Fatalf("%s %q must be one of %v", field, got, allowed)
}

func requireExistingFile(t *testing.T, repoRoot, path string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatalf("referenced file %q does not exist: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("referenced file %q is a directory", path)
	}
}

func requireExistingDir(t *testing.T, repoRoot, path string) {
	t.Helper()
	info, err := os.Stat(filepath.Join(repoRoot, path))
	if err != nil {
		t.Fatalf("referenced directory %q does not exist: %v", path, err)
	}
	if !info.IsDir() {
		t.Fatalf("referenced directory %q is not a directory", path)
	}
}

func repoRootFromPackageDir(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}
