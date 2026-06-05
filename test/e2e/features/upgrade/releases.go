//go:build e2e

package upgrade

// The upgrade e2e test installs a previously-released version of kgateway and then
// upgrades it to the locally-built chart. These constants are the released versions
// it upgrades from.
//
// They are checked in deliberately, rather than computed at test time by querying the
// GitHub releases API. Checking them in keeps the test hermetic (no network access, no
// GitHub API rate limits, works in shallow checkouts) and makes the inputs reviewable.
//
// The trade-off is that they must be refreshed whenever a release is cut on this branch.
// The release checklist enforces this; see the "Refresh upgrade-test versions" item in
// .github/ISSUE_TEMPLATE/RELEASE-REQUEST.md and devel/contributing/releasing.md.
const (
	// LatestRelease is the most recent release that is an ancestor of this branch.
	// On main this is the latest patch of the most recently released minor line.
	LatestRelease = "v2.3.2"

	// PreviousMinorRelease is the most recent release of the minor line immediately
	// before LatestRelease (i.e. LatestRelease's minor minus one).
	PreviousMinorRelease = "v2.2.5"
)
