package develtesting

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type formalModelMap struct {
	Obligations []formalModelObligation `json:"obligations"`
}

type formalModelObligation struct {
	ID             string                 `json:"id"`
	Summary        string                 `json:"summary"`
	SpecFile       string                 `json:"specFile"`
	SpecAnchors    []string               `json:"specAnchors"`
	Status         string                 `json:"status"`
	Note           string                 `json:"note,omitempty"`
	Tests          []formalAssumptionTest `json:"tests,omitempty"`
	DivergencePins []formalAssumptionTest `json:"divergencePins,omitempty"`
}

// TestFormalModelMap gates the mapping between the per-cluster readiness
// spec's obligations and the Go code (devel/testing/formal-model-map.yaml).
// Covered obligations must list a discharging test; divergent obligations —
// places where snapshotPerClient currently behaves like one of the spec's
// bug systems — must list a characterization test that pins the current
// behavior, so the divergence breaks CI the moment the code moves (in
// either direction) instead of rotting as prose.
func TestFormalModelMap(t *testing.T) {
	repoRoot := repoRootFromPackageDir(t)

	data, err := os.ReadFile("formal-model-map.yaml")
	if err != nil {
		t.Fatalf("failed to read formal model map: %v", err)
	}
	var modelMap formalModelMap
	if err := yaml.Unmarshal(data, &modelMap); err != nil {
		t.Fatalf("failed to parse formal model map: %v", err)
	}
	if len(modelMap.Obligations) == 0 {
		t.Fatal("formal model map must contain at least one obligation")
	}

	seen := make(map[string]struct{}, len(modelMap.Obligations))
	for _, obligation := range modelMap.Obligations {
		t.Run(obligation.ID, func(t *testing.T) {
			if obligation.ID == "" {
				t.Fatal("obligation id must not be empty")
			}
			if _, ok := seen[obligation.ID]; ok {
				t.Fatalf("duplicate obligation id %q", obligation.ID)
			}
			seen[obligation.ID] = struct{}{}
			if obligation.Summary == "" {
				t.Fatal("obligation summary must not be empty")
			}

			spec, err := os.ReadFile(filepath.Join(repoRoot, obligation.SpecFile))
			if err != nil {
				t.Fatalf("spec file %q does not exist: %v", obligation.SpecFile, err)
			}
			if len(obligation.SpecAnchors) == 0 {
				t.Fatal("obligation must name at least one spec anchor")
			}
			for _, anchor := range obligation.SpecAnchors {
				if !strings.Contains(string(spec), anchor) {
					t.Errorf("spec anchor %q not found in %s", anchor, obligation.SpecFile)
				}
			}

			switch obligation.Status {
			case "covered":
				if len(obligation.Tests) == 0 {
					t.Fatal("covered obligation must list at least one discharging test")
				}
			case "divergent":
				if len(obligation.DivergencePins) == 0 {
					t.Fatal("divergent obligation must list at least one divergence pin")
				}
				if obligation.Note == "" {
					t.Fatal("divergent obligation must explain the divergence in a note")
				}
			case "open":
				if obligation.Note == "" {
					t.Fatal("open obligation must say where the work is tracked")
				}
			default:
				t.Fatalf("status %q must be one of [covered divergent open]", obligation.Status)
			}

			for _, ref := range append(obligation.Tests, obligation.DivergencePins...) {
				requireTestDeclared(t, repoRoot, ref.File, ref.Test)
			}
		})
	}
}

// requireTestDeclared fails unless the named test function (top-level or
// suite method) is declared in the referenced file.
func requireTestDeclared(t *testing.T, repoRoot, file, test string) {
	t.Helper()
	source, err := os.ReadFile(filepath.Join(repoRoot, file))
	if err != nil {
		t.Errorf("referenced test file %q does not exist: %v", file, err)
		return
	}
	declaration := regexp.MustCompile(
		fmt.Sprintf(`func\s+(\([^)]*\)\s*)?%s\s*\(`, regexp.QuoteMeta(test)))
	if !declaration.Match(source) {
		t.Errorf("test %q not declared in %s", test, file)
	}
}
