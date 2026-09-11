// checkreceipts requires actual run/pass events, not just test declarations.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

type (
	key   struct{ Package, Test string }
	event struct{ Action, Package, Test string }
)

func check(r io.Reader, required map[key]bool) error {
	if len(required) == 0 {
		return fmt.Errorf("no required tests")
	}
	ran, passed, packages := map[key]bool{}, map[key]bool{}, map[string]bool{}
	dec := json.NewDecoder(r)
	for {
		var e event
		if err := dec.Decode(&e); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("malformed receipt: %w", err)
		}
		k := key{e.Package, e.Test}
		if e.Test == "" {
			if e.Action == "pass" {
				packages[e.Package] = true
			}
			if e.Action == "fail" {
				return fmt.Errorf("package failed: %s", e.Package)
			}
			continue
		}
		for req := range required {
			if e.Package == req.Package && (e.Test == req.Test || strings.HasPrefix(e.Test, req.Test+"/")) {
				if e.Action == "skip" || e.Action == "fail" {
					return fmt.Errorf("required test %s/%s: %s", e.Package, e.Test, e.Action)
				}
			}
		}
		if e.Action == "run" {
			ran[k] = true
		}
		if e.Action == "pass" {
			passed[k] = true
		}
	}
	for k := range required {
		if !ran[k] || !passed[k] || !packages[k.Package] {
			return fmt.Errorf("missing run/test-pass/package-pass receipt for %s/%s", k.Package, k.Test)
		}
	}
	return nil
}

func requirements(root string) (map[key]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "devel/testing/formal-assumptions.yaml"))
	if err != nil {
		return nil, err
	}
	var mapping struct {
		Assumptions []struct {
			Tests []struct{ File, Test string } `json:"dischargedBy"`
		} `json:"assumptions"`
	}
	if err = yaml.Unmarshal(data, &mapping); err != nil {
		return nil, err
	}
	req := map[key]bool{}
	prefix := "github.com/kgateway-dev/kgateway/v2/"
	for _, a := range mapping.Assumptions {
		for _, test := range a.Tests {
			if strings.HasPrefix(test.File, "test/e2e/") {
				continue
			} // Separate live e2e obligation RF-001/004.
			req[key{prefix + filepath.ToSlash(filepath.Dir(test.File)), test.Test}] = true
		}
	}
	// Require every dependency probe, including characterization outside the
	// assumption map. Missing an entire package cannot yield a green gate.
	files, err := filepath.Glob(filepath.Join(root, "devel/formal/gcpprobe/*_test.go"))
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no dependency probe source files")
	}
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && strings.HasPrefix(fn.Name.Name, "Test") {
				req[key{prefix + "devel/formal/gcpprobe", fn.Name.Name}] = true
			}
		}
	}
	req[key{prefix + "devel/testing", "TestFormalAssumptionsDischarged"}] = true
	req[key{prefix + "devel/testing", "TestProtocolScopeHasExplicitDispositions"}] = true
	req[key{prefix + "pkg/sds/server", "TestSDSFetchDoesNotAuthorizeNodeIdentity"}] = true
	req[key{prefix + "pkg/kgateway/proxy_syncer", "TestWarmEmptyBackendHoldsUnrelatedRouteAndSecret"}] = true
	return req, nil
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: checkreceipts <repository-root> <go-test.jsonl>")
	}
	req, err := requirements(os.Args[1])
	if err != nil {
		return err
	}
	f, err := os.Open(os.Args[2])
	if err != nil {
		return err
	}
	defer f.Close()
	if err = check(f, req); err != nil {
		return err
	}
	fmt.Printf("PASS execution receipts for %d required unit tests; live e2e obligations remain separate\n", len(req))
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
