package main

import (
	"strings"
	"testing"
)

func TestReceiptMutations(t *testing.T) {
	run := `{"Action":"run","Package":"p","Test":"TestRequired"}` + "\n"
	pass := `{"Action":"pass","Package":"p","Test":"TestRequired"}` + "\n"
	pkg := `{"Action":"pass","Package":"p"}` + "\n"
	for _, tc := range []struct {
		name, trace string
		wantOK      bool
	}{
		{"complete", run + pass + pkg, true},
		{"empty", "", false},
		{"declaration-without-run", pass + pkg, false},
		{"missing-test-outcome", run + pkg, false},
		{"missing-package-outcome", run + pass, false},
		{"skip", run + `{"Action":"skip","Package":"p","Test":"TestRequired"}` + "\n" + pkg, false},
		{"skipped-child-with-passing-parent", run + `{"Action":"skip","Package":"p","Test":"TestRequired/child"}` + "\n" + pass + pkg, false},
		{"wrong-test", strings.ReplaceAll(run+pass+pkg, "TestRequired", "TestOther"), false},
		{"malformed", run + "{", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(strings.NewReader(tc.trace), map[key]bool{{"p", "TestRequired"}: true})
			if (err == nil) != tc.wantOK {
				t.Fatalf("want pass=%v, got %v", tc.wantOK, err)
			}
		})
	}
}
