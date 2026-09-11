package develtesting

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type protocolScope struct {
	Profile string               `json:"profile"`
	Entries []protocolScopeEntry `json:"entries"`
}

type protocolScopeEntry struct {
	ID                 string `json:"id"`
	Surface            string `json:"surface"`
	Reachability       string `json:"reachability"`
	Status             string `json:"status"`
	ConstructionFile   string `json:"constructionFile"`
	ConstructionAnchor string `json:"constructionAnchor"`
	Evidence           string `json:"evidence"`
	Action             string `json:"action"`
}

func TestProtocolScopeHasExplicitDispositions(t *testing.T) {
	repoRoot := repoRootFromPackageDir(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "devel/formal/protocol-scope.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var scope protocolScope
	if err := yaml.UnmarshalStrict(data, &scope); err != nil {
		t.Fatalf("parse protocol scope: %v", err)
	}
	if scope.Profile == "" || len(scope.Entries) == 0 {
		t.Fatal("protocol scope must name a profile and entries")
	}
	ledger, err := os.ReadFile(filepath.Join(repoRoot, "devel/formal/research-findings.md"))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]struct{}{}
	for _, entry := range scope.Entries {
		t.Run(entry.ID, func(t *testing.T) {
			if entry.ID == "" || entry.Surface == "" || entry.Reachability == "" || entry.ConstructionFile == "" || entry.ConstructionAnchor == "" || entry.Evidence == "" || entry.Action == "" {
				t.Fatalf("scope entry has empty required field: %+v", entry)
			}
			if _, ok := seen[entry.ID]; ok {
				t.Fatalf("duplicate scope ID %q", entry.ID)
			}
			seen[entry.ID] = struct{}{}
			switch entry.Reachability {
			case "deployed-default", "configuration-conditional", "registered-network-surface", "registered-loopback-surface", "excluded-by-construction":
			default:
				t.Fatalf("unknown reachability %q", entry.Reachability)
			}
			switch entry.Status {
			case "open", "excluded", "partially-modeled", "partially-characterized", "characterized-only":
			default:
				t.Fatalf("unknown status %q", entry.Status)
			}
			source, err := os.ReadFile(filepath.Join(repoRoot, entry.ConstructionFile))
			if err != nil {
				t.Fatalf("construction file: %v", err)
			}
			if !strings.Contains(string(source), entry.ConstructionAnchor) {
				t.Fatalf("anchor %q absent from %s", entry.ConstructionAnchor, entry.ConstructionFile)
			}
			if strings.Contains(entry.Action, "RF-") {
				for word := range strings.FieldsSeq(entry.Action) {
					id := strings.Trim(word, ",.;:()")
					if strings.HasPrefix(id, "RF-") && !strings.Contains(string(ledger), "## "+id+" ") {
						t.Errorf("action references missing finding %s", id)
					}
				}
			}
		})
	}
}
