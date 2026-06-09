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

type formalAssumptions struct {
	Assumptions []formalAssumption `json:"assumptions"`
}

type formalAssumption struct {
	ID           string                 `json:"id"`
	Summary      string                 `json:"summary"`
	SpecFile     string                 `json:"specFile"`
	SpecAnchors  []string               `json:"specAnchors"`
	DischargedBy []formalAssumptionTest `json:"dischargedBy"`
}

type formalAssumptionTest struct {
	File string `json:"file"`
	Test string `json:"test"`
}

// TestFormalAssumptionsDischarged gates the mapping between the Lean
// spec's named assumptions (devel/formal/lean/ASSUMPTIONS.md) and the Go
// or e2e tests that discharge them. It fails when a spec anchor vanishes
// from the Lean sources or a discharging test function is renamed or
// deleted, so the assumption ledger cannot silently rot.
func TestFormalAssumptionsDischarged(t *testing.T) {
	repoRoot := repoRootFromPackageDir(t)

	data, err := os.ReadFile("formal-assumptions.yaml")
	if err != nil {
		t.Fatalf("failed to read formal assumptions mapping: %v", err)
	}
	var mapping formalAssumptions
	if err := yaml.Unmarshal(data, &mapping); err != nil {
		t.Fatalf("failed to parse formal assumptions mapping: %v", err)
	}
	if len(mapping.Assumptions) == 0 {
		t.Fatal("formal assumptions mapping must contain at least one assumption")
	}

	ledger, err := os.ReadFile(filepath.Join(repoRoot, "devel/formal/lean/ASSUMPTIONS.md"))
	if err != nil {
		t.Fatalf("failed to read assumption ledger: %v", err)
	}

	seen := make(map[string]struct{}, len(mapping.Assumptions))
	for _, assumption := range mapping.Assumptions {
		t.Run(assumption.ID, func(t *testing.T) {
			if assumption.ID == "" {
				t.Fatal("assumption id must not be empty")
			}
			if _, ok := seen[assumption.ID]; ok {
				t.Fatalf("duplicate assumption id %q", assumption.ID)
			}
			seen[assumption.ID] = struct{}{}
			if assumption.Summary == "" {
				t.Fatal("assumption summary must not be empty")
			}
			if !strings.Contains(string(ledger), assumption.ID) {
				t.Fatalf("assumption %q is not described in devel/formal/lean/ASSUMPTIONS.md", assumption.ID)
			}

			spec, err := os.ReadFile(filepath.Join(repoRoot, assumption.SpecFile))
			if err != nil {
				t.Fatalf("spec file %q does not exist: %v", assumption.SpecFile, err)
			}
			if len(assumption.SpecAnchors) == 0 {
				t.Fatal("assumption must name at least one spec anchor")
			}
			for _, anchor := range assumption.SpecAnchors {
				if !strings.Contains(string(spec), anchor) {
					t.Errorf("spec anchor %q not found in %s", anchor, assumption.SpecFile)
				}
			}

			if len(assumption.DischargedBy) == 0 {
				t.Fatal("assumption must name at least one discharging test")
			}
			for _, discharge := range assumption.DischargedBy {
				source, err := os.ReadFile(filepath.Join(repoRoot, discharge.File))
				if err != nil {
					t.Errorf("discharging test file %q does not exist: %v", discharge.File, err)
					continue
				}
				// Match both top-level test funcs and suite methods.
				declaration := regexp.MustCompile(
					fmt.Sprintf(`func\s+(\([^)]*\)\s*)?%s\s*\(`, regexp.QuoteMeta(discharge.Test)))
				if !declaration.Match(source) {
					t.Errorf("discharging test %q not declared in %s", discharge.Test, discharge.File)
				}
			}
		})
	}
}
