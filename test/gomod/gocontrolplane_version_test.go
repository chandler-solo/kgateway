// Package gomod contains tests that assert invariants on the repository's
// go.mod file.
//
// The go-control-plane module is a complex repository whose proto
// bindings must track the Envoy runtime version that Gloo ships
// with. Newer Envoy releases may break wire and API compatibility
// with older proto definitions, and go-control-plane does not publish
// a compatibility matrix that lets us mechanically determine which
// proto version pairs with which Envoy runtime. In practice we have
// to pin both the replace directive for
// `github.com/envoyproxy/go-control-plane` and the envoy submodule by
// hand after verifying against the Envoy binary we actually deploy.
//
// To prevent an unrelated dependency bump (for example, a CVE bump to grpc)
// from transitively dragging in a newer go-control-plane and silently
// breaking xDS, we assert here that the effective pinned version stays
// below v0.33.0 on this release branch.
package gomod_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

const (
	goControlPlaneModule = "github.com/envoyproxy/go-control-plane"
	// maxGoControlPlaneVersion is exclusive: the effective version used at
	// build time must compare strictly less than this value.
	//
	// Do not raise this without careful analysis of go-control-plane.
	maxGoControlPlaneVersion = "v0.33.0"
)

func TestGoControlPlaneVersionBelowCap(t *testing.T) {
	mod := loadGoMod(t)

	version := effectiveVersion(mod, goControlPlaneModule)
	if version == "" {
		t.Fatalf("could not find %s in go.mod (neither require nor replace directives matched)", goControlPlaneModule)
	}

	canonical := canonicalSemver(version)
	if !semver.IsValid(canonical) {
		t.Fatalf("effective version %q for %s is not a valid semver string", version, goControlPlaneModule)
	}

	if semver.Compare(canonical, maxGoControlPlaneVersion) >= 0 {
		t.Fatalf(
			"%s is pinned to %s, which is >= %s.\n\n"+
				"go-control-plane is a complex repository whose proto bindings must track "+
				"the Envoy runtime version Gloo ships with. Backwards compatibility across "+
				"go-control-plane versions is not documented, so bumping past the cap must "+
				"be done deliberately after verifying against the running Envoy binary. "+
				"If this bump is intentional, update maxGoControlPlaneVersion in %s along "+
				"with the pins in go.mod.",
			goControlPlaneModule, version, maxGoControlPlaneVersion, thisFile(),
		)
	}
}

// pinnedSubmoduleMinors captures the major.minor that each go-control-plane
// submodule is allowed to use. A mismatch — including a minor bump
// introduced transitively by an unrelated upgrade — fails the test.
//
// Entries are keyed by module path. The value is the expected MAJOR.MINOR
// prefix (e.g. "v1.32"); patch and pseudo-version suffixes are ignored.
//
// An empty value means "no minor has been approved yet": if the module
// ever appears in go.mod, the test fails so the developer must deliberately
// choose and record the minor here.
var pinnedSubmoduleMinors = map[string]string{
	// envoy: replaced to v1.32.5-pseudo to match the Envoy v1.32 runtime
	// shipped on the v1.18.x branch. The require line declares v1.36.0 but
	// the replace is what the build resolves.
	"github.com/envoyproxy/go-control-plane/envoy": "v1.32",
	// contrib: proto bindings must stay aligned with the envoy submodule
	// above, so they share the v1.32 minor.
	"github.com/envoyproxy/go-control-plane/contrib": "v1.32",
	// ratelimit: independent release cadence from envoy/contrib. Pinned
	// at v0.1 so a breaking API reshuffle under v0 semver cannot slip in
	// via a transitive bump.
	"github.com/envoyproxy/go-control-plane/ratelimit": "v0.1",
	// xdsmatcher: not currently a direct dependency. Listed here so that
	// if anything pulls it into go.mod the test fails until a reviewer
	// records the deliberately chosen minor.
	"github.com/envoyproxy/go-control-plane/xdsmatcher": "",
}

func TestGoControlPlaneSubmoduleMinorVersionsPinned(t *testing.T) {
	mod := loadGoMod(t)

	// Sort keys for deterministic subtest order and output.
	paths := make([]string, 0, len(pinnedSubmoduleMinors))
	for p := range pinnedSubmoduleMinors {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, path := range paths {
		expectedMinor := pinnedSubmoduleMinors[path]
		t.Run(path, func(t *testing.T) {
			version := effectiveVersion(mod, path)
			if version == "" {
				if expectedMinor == "" {
					// Module not in go.mod and no baseline recorded — nothing to enforce yet.
					t.Skipf("%s is not a dependency and has no pinned minor; nothing to check", path)
				}
				// Having a pinned minor but no entry in go.mod means the module
				// was removed. That is fine, but the reviewer should clear the
				// pin so the intent stays accurate.
				t.Skipf("%s is no longer in go.mod; consider removing the pin from pinnedSubmoduleMinors", path)
			}

			if expectedMinor == "" {
				t.Fatalf(
					"%s is now present in go.mod at %s, but pinnedSubmoduleMinors has no approved minor for it.\n\n"+
						"Bumping, adding, or removing go-control-plane submodules must be a deliberate decision "+
						"because their proto bindings couple tightly to the Envoy runtime and their "+
						"cross-minor compatibility is not documented. Update %s to record the intended minor.",
					path, version, thisFile(),
				)
			}

			canonical := canonicalSemver(version)
			if !semver.IsValid(canonical) {
				t.Fatalf("effective version %q for %s is not a valid semver string", version, path)
			}

			actualMinor := semver.MajorMinor(canonical)
			if actualMinor != expectedMinor {
				t.Fatalf(
					"%s is pinned to %s (major.minor %s), but pinnedSubmoduleMinors requires %s.\n\n"+
						"A minor-version change on a go-control-plane submodule is never safe to accept "+
						"implicitly: the proto bindings must line up with the Envoy runtime in use, and "+
						"go-control-plane does not publish a compatibility matrix we can check against. "+
						"If this bump is intentional, update %s and verify xDS against the running Envoy "+
						"binary before merging; if it was pulled in transitively (commonly via a grpc or "+
						"cncf/xds CVE bump), add a replace directive in go.mod to hold the minor.",
					path, version, actualMinor, expectedMinor, thisFile(),
				)
			}
		})
	}
}

// loadGoMod parses the repository's go.mod file.
func loadGoMod(t *testing.T) *modfile.File {
	t.Helper()

	path := repoGoModPath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	mod, err := modfile.Parse(path, data, nil)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return mod
}

// effectiveVersion returns the version that will actually be used at build
// time for the given module path. A matching replace directive wins over
// the require directive, which mirrors how the go toolchain resolves the
// module graph.
func effectiveVersion(mod *modfile.File, path string) string {
	for _, r := range mod.Replace {
		if r.Old.Path == path && (r.Old.Version == "" || r.Old.Version == requiredVersion(mod, path)) {
			return r.New.Version
		}
	}
	return requiredVersion(mod, path)
}

func requiredVersion(mod *modfile.File, path string) string {
	for _, r := range mod.Require {
		if r.Mod.Path == path {
			return r.Mod.Version
		}
	}
	return ""
}

// canonicalSemver normalises Go module pseudo-versions like
// "v0.13.5-0.20250123154839-2a6715911fec" into something semver.Compare
// will accept. The leading "vMAJOR.MINOR.PATCH" prefix is already valid
// semver, so we return it unchanged; the pre-release suffix sorts before
// the corresponding release, which is exactly what we want here.
func canonicalSemver(v string) string {
	return semver.Canonical(v)
}

// repoGoModPath walks up from this test file to the repository root and
// returns the path to go.mod. Using runtime.Caller keeps the test
// independent of the working directory it is invoked from.
func repoGoModPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed; cannot locate go.mod")
	}
	// test/gomod/<this file> -> repo root is two directories up.
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "go.mod")
}

func thisFile() string {
	_, f, _, ok := runtime.Caller(0)
	if !ok {
		return "test/gomod/gocontrolplane_version_test.go"
	}
	return f
}
