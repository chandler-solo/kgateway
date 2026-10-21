package develtesting

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var findingHeading = regexp.MustCompile(`(?m)^## (RF-[0-9]+)\b`)

// duplicateFindingIDs returns each finding ID that more than one ledger
// heading uses, in order of its second use.
func duplicateFindingIDs(ledger string) []string {
	uses := map[string]int{}
	var duplicates []string
	for _, match := range findingHeading.FindAllStringSubmatch(ledger, -1) {
		uses[match[1]]++
		if uses[match[1]] == 2 {
			duplicates = append(duplicates, match[1])
		}
	}
	return duplicates
}

func TestResearchFindingIDsAreUnique(t *testing.T) {
	if got := duplicateFindingIDs("## RF-001 One\n## RF-002 Two\n## RF-001 Three\n"); !slices.Equal(got, []string{"RF-001"}) {
		t.Fatalf("duplicate detection returned %v, want [RF-001]", got)
	}
	ledger, err := os.ReadFile(filepath.Join(repoRootFromPackageDir(t), "devel/formal/research-findings.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !findingHeading.Match(ledger) {
		t.Fatal("research-findings.md has no finding headings")
	}
	if duplicates := duplicateFindingIDs(string(ledger)); len(duplicates) > 0 {
		t.Fatalf("research-findings.md gives more than one finding the ID %v; renumber the later finding and its links", duplicates)
	}
}

type corpusDispositionLine struct {
	Number         int    `json:"number"`
	Classification string `json:"classification"`
	Rule           string `json:"rule"`
	Obligation     string `json:"obligation"`
	Ledger         bool   `json:"ledger"`
}

type corpusBugEntry struct {
	Repository string `json:"repository"`
	Number     int    `json:"number"`
}

func validateCorpusDispositions(dispositions, bugCorpus, findings []byte) error {
	scanner := bufio.NewScanner(strings.NewReader(string(dispositions)))
	var header struct {
		CandidateCount    int            `json:"candidate_count"`
		DispositionCounts map[string]int `json:"disposition_counts"`
		BulkRules         []struct {
			ID string `json:"id"`
		} `json:"bulk_rules"`
	}
	if !scanner.Scan() {
		return errors.New("dispositions are empty")
	}
	if err := json.Unmarshal(scanner.Bytes(), &header); err != nil {
		return fmt.Errorf("parse disposition header: %w", err)
	}
	rules := map[string]bool{}
	for _, rule := range header.BulkRules {
		if rule.ID == "" || rules[rule.ID] {
			return fmt.Errorf("empty or duplicate bulk rule %q", rule.ID)
		}
		rules[rule.ID] = true
	}
	var lines []corpusDispositionLine
	for scanner.Scan() {
		var line corpusDispositionLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return fmt.Errorf("parse disposition line: %w", err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read dispositions: %w", err)
	}
	seen := map[int]bool{}
	counts := map[string]int{}
	ledgerDispositions := map[int]bool{}
	previous := 0
	for i, line := range lines {
		if line.Number <= 0 || seen[line.Number] {
			return fmt.Errorf("invalid or duplicate disposition number %d", line.Number)
		}
		if i > 0 && line.Number <= previous {
			return fmt.Errorf("disposition numbers are not strictly sorted at %d", line.Number)
		}
		seen[line.Number] = true
		previous = line.Number
		counts[line.Classification]++
		if line.Rule != "" && !rules[line.Rule] {
			return fmt.Errorf("disposition %d names unknown rule %q", line.Number, line.Rule)
		}
		if line.Ledger {
			ledgerDispositions[line.Number] = true
		}
	}
	if header.CandidateCount != len(lines) {
		return fmt.Errorf("header candidate count %d differs from %d lines", header.CandidateCount, len(lines))
	}
	if len(counts) != len(header.DispositionCounts) {
		return fmt.Errorf("header classification counts have %d classes, lines have %d", len(header.DispositionCounts), len(counts))
	}
	for class, count := range counts {
		if header.DispositionCounts[class] != count {
			return fmt.Errorf("classification %q count is %d, header says %d", class, count, header.DispositionCounts[class])
		}
	}
	var corpus struct {
		Entries []corpusBugEntry `json:"entries"`
	}
	if err := json.Unmarshal(bugCorpus, &corpus); err != nil {
		return fmt.Errorf("parse bug corpus: %w", err)
	}
	ledgerEntries := map[int]bool{}
	for _, entry := range corpus.Entries {
		if entry.Repository == "envoyproxy/go-control-plane" {
			ledgerEntries[entry.Number] = true
		}
	}
	if len(ledgerEntries) != len(ledgerDispositions) {
		return fmt.Errorf("ledger has %d go-control-plane entries, dispositions mark %d", len(ledgerEntries), len(ledgerDispositions))
	}
	for number := range ledgerEntries {
		if !ledgerDispositions[number] {
			return fmt.Errorf("ledger entry %d is not marked ledger=true", number)
		}
	}
	for number := range ledgerDispositions {
		if !ledgerEntries[number] {
			return fmt.Errorf("disposition %d is marked ledger=true without an entry", number)
		}
	}
	findingIDs := map[string]bool{}
	for _, match := range findingHeading.FindAllSubmatch(findings, -1) {
		findingIDs[string(match[1])] = true
	}
	for _, line := range lines {
		if line.Obligation != "" && !findingIDs[line.Obligation] {
			return fmt.Errorf("disposition %d names missing obligation %q", line.Number, line.Obligation)
		}
	}
	return nil
}

func TestCorpusDispositionsAreConsistent(t *testing.T) {
	baseHeader := map[string]any{
		"candidate_count":    2,
		"disposition_counts": map[string]int{"mapped": 1, "mechanical": 1},
		"bulk_rules":         []map[string]string{{"id": "R1"}},
	}
	baseLines := []map[string]any{
		{"number": 1, "classification": "mapped", "rule": "", "obligation": "RF-001", "ledger": true},
		{"number": 2, "classification": "mechanical", "rule": "R1", "obligation": "", "ledger": false},
	}
	baseCorpus := map[string]any{"entries": []map[string]any{{"repository": "envoyproxy/go-control-plane", "number": 1}}}
	encode := func(header any, lines []map[string]any) []byte {
		var b strings.Builder
		data, _ := json.Marshal(header)
		b.Write(data)
		b.WriteByte('\n')
		for _, line := range lines {
			data, _ := json.Marshal(line)
			b.Write(data)
			b.WriteByte('\n')
		}
		return []byte(b.String())
	}
	cloneLines := func() []map[string]any {
		out := make([]map[string]any, len(baseLines))
		for i, line := range baseLines {
			out[i] = map[string]any{}
			maps.Copy(out[i], line)
		}
		return out
	}
	corpusBytes, _ := json.Marshal(baseCorpus)
	findings := []byte("## RF-001 Synthetic finding\n")
	valid := encode(baseHeader, cloneLines())
	if err := validateCorpusDispositions(valid, corpusBytes, findings); err != nil {
		t.Fatalf("synthetic valid case rejected: %v", err)
	}
	tests := []struct {
		name         string
		dispositions []byte
		corpus       []byte
		findings     []byte
	}{
		{name: "malformed line", dispositions: append(append([]byte(nil), valid...), []byte("{bad}\n")...), corpus: corpusBytes, findings: findings},
		{name: "duplicate numbers", dispositions: encode(baseHeader, []map[string]any{cloneLines()[0], cloneLines()[0]}), corpus: corpusBytes, findings: findings},
		{name: "unsorted numbers", dispositions: encode(baseHeader, []map[string]any{cloneLines()[1], cloneLines()[0]}), corpus: corpusBytes, findings: findings},
		{name: "candidate count", dispositions: encode(map[string]any{"candidate_count": 3, "disposition_counts": baseHeader["disposition_counts"], "bulk_rules": baseHeader["bulk_rules"]}, cloneLines()), corpus: corpusBytes, findings: findings},
		{name: "classification counts", dispositions: encode(map[string]any{"candidate_count": 2, "disposition_counts": map[string]int{"mapped": 2}, "bulk_rules": baseHeader["bulk_rules"]}, cloneLines()), corpus: corpusBytes, findings: findings},
		{name: "unknown rule", dispositions: encode(baseHeader, []map[string]any{{"number": 1, "classification": "mapped", "rule": "R2", "obligation": "RF-001", "ledger": true}, cloneLines()[1]}), corpus: corpusBytes, findings: findings},
		{name: "ledger mismatch", dispositions: encode(baseHeader, []map[string]any{{"number": 1, "classification": "mapped", "rule": "", "obligation": "RF-001", "ledger": false}, cloneLines()[1]}), corpus: corpusBytes, findings: findings},
		{name: "missing obligation", dispositions: encode(baseHeader, []map[string]any{{"number": 1, "classification": "mapped", "rule": "", "obligation": "RF-999", "ledger": true}, cloneLines()[1]}), corpus: corpusBytes, findings: findings},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateCorpusDispositions(tt.dispositions, tt.corpus, tt.findings); err == nil {
				t.Fatal("synthetic invalid case was accepted")
			}
		})
	}

	repoRoot := repoRootFromPackageDir(t)
	dispositions, err := os.ReadFile(filepath.Join(repoRoot, "devel/formal/corpus-dispositions/envoyproxy__go-control-plane.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	bugCorpus, err := os.ReadFile(filepath.Join(repoRoot, "devel/formal/bug-corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	findings, err = os.ReadFile(filepath.Join(repoRoot, "devel/formal/research-findings.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateCorpusDispositions(dispositions, bugCorpus, findings); err != nil {
		t.Fatal(err)
	}
}

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
