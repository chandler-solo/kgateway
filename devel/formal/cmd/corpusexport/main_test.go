package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAcronymsDoNotMatchOrdinaryWords(t *testing.T) {
	terms := compileTerms([]string{"EDS", "CDS", "watch", "endpoint"})
	for _, tc := range []struct {
		text string
		want int
	}{
		{"needs feedback; records updated", 0},
		{"EDS/CDS response rejected", 2},
		{"endpoint watch", 2},
		{"watchdog fires", 0}, // separately listed term; no implied exhaustive keyword coverage
	} {
		if got := keywordHits(tc.text, terms); len(got) != tc.want {
			t.Errorf("%q: want %d hits, got %v", tc.text, tc.want, got)
		}
	}
}
func TestCachedReceiptRejectsCorruptionAndWrongEndpoint(t *testing.T) {
	data := json.RawMessage(`[{"number":1}]`)
	sum := sha256.Sum256(data)
	r := receipt{Endpoint: "repos/example/repo/issues", Data: data, SHA256: hex.EncodeToString(sum[:]), Next: "https://api.github.com/repositories/1/issues?after=cursor"}
	path := filepath.Join(t.TempDir(), "page.json")
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := cached(path, r.Endpoint)
	if err != nil || got.Next != r.Next {
		t.Fatalf("valid cached cursor receipt failed: %v", err)
	}
	if _, err = cached(path, "different-endpoint"); err == nil {
		t.Fatal("wrong endpoint accepted")
	}
	r.Data = json.RawMessage(`[{"number":2}]`)
	b, err = json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = cached(path, r.Endpoint); err == nil {
		t.Fatal("corrupted payload accepted")
	}
}
