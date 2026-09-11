# Historical corpus method and current coverage

The [scope manifest](corpus-scope.json) covers all six requested repositories
plus solo-kit, with creation cutoff **before 2026-09-10 00:00:00 UTC**. All seven
issue/PR inventories were enumerated to cutoff or the final server-provided
cursor. [Inventory counts](corpus-inventory.json) report retrieval, not review.
[Discovery records](bug-corpus.json) contain seventy-two dispositions: the first eleven discovery mechanisms plus classified batches from go-control-plane, kgateway, Envoy Gateway, Envoy, Gloo, and solo-kit.
RF-016 tracks the unfinished historical program.

## Reproduce and resume

Run from the repository root, authenticated for read-only GitHub access:

```sh
CGO_ENABLED=0 go run -tags e2e ./devel/formal/cmd/corpusexport -out "$HOME/t/xds-corpus-20260910"
CGO_ENABLED=0 go run -tags e2e ./devel/formal/cmd/corpusexport -out "$HOME/t/xds-corpus-20260910" -details devel/formal/bug-corpus.json
```

Use `-repo owner/name` to resume one inventory. Raw exports must stay outside
the repository. Directories/files use private permissions; public reports
contain independently reproducible mechanisms, not private issue contents.
The initial captures are in `~/t/xds-corpus-20260910`.

Each cached API response records endpoint, retrieval timestamp, payload SHA-256,
raw JSON and next cursor. Resume checks endpoint and checksum, and never
silently treats API errors or duplicate issue IDs as completion. GitHub rejects
page-number enumeration at page 100 for large data sets: following the Link
cursor passed that boundary and captured all 47,128 Envoy issue/PR records in
the selected inventory. This avoids the search cap but is not an atomic GitHub
snapshot. Bodies and comments are mutable; retrieval times must accompany any
claim about their content at cutoff. The cutoff selects issue creation, not a
historical reconstruction of every edit as of midnight.

Detail export captures issue comments/events and, for PRs, metadata, changed
files, commits, review comments and reviews, with pagination. Those records
are research inputs; capture alone does not mean they were read or that all
linked issues, source paths and release branches have been followed.

## Repository identity and history

`kgateway-dev/kgateway` is repository ID `118510171`, created in 2018. The
current `solo-io/gloo` is a distinct fork, ID `885037479`, created in November
2024 with kgateway as its parent/source. Searching only today's gloo repository
would miss the older issue history held by kgateway. Preserve canonical
repository IDs and issue IDs, and resolve legacy URLs against this lineage;
do not deduplicate by title or assume the current URL name describes historical
ownership. Fork-specific issues remain distinct records.

## Classification and holdouts

Every inventory row starts **unreviewed**, including rows with no keyword hit.
Case-insensitive word-boundary matches prioritize candidates; this prevents
`EDS` matching ordinary words such as `needs`. Matching is not semantic
classification, and code identifiers, comments and source-only fixes can evade
it. No-hit rows are not classified as irrelevant.

The first ten discovery records distinguish reproduced current mechanisms,
source-reviewed fix leads, plausible reports, and unconfirmed diagnoses. For
example, current Envoy re-requests equal-version EDS during rewarming, whereas
Envoy #13009's older report describes no re-request. The family is reproduced;
the historical and current traces must not be conflated. GCP #46 being closed
does not mean the pinned repeated-NACK behavior is fixed. KGW #14352 includes
missing CDS as well as empty endpoints; the cold-CLA correction covers only
part of that report.

A stable hash nominates one eighth of inventory rows as potential holdouts.
This is only a candidate partition. All already-read seed cases are discovery
cases and cannot become held-out evidence retroactively. Freeze a mechanism
holdout set before reading its details, then report what new model state it
requires. No holdout validation has yet been claimed.

## Remaining accounting

- Review and classify the full candidate set, then inspect non-keyword source
  changes and relevant no-hit records. Counts are not "all past xDS bugs."
- Follow linked issues, fixes, reverts and release ancestry; PR merge status is
  not shipped-version evidence. Resolve legacy gloo links explicitly.
- Expand private-source mechanisms only through sanitized, independent
  reproducers; preserve detailed private classifications outside this tree.
- Add source-path enumeration, protocol/feature reachability dispositions,
  and minimized schedules for each relevant mechanism.
- Keep an obligation open whenever the model cannot express a discovered
  schedule. New findings RF-017/018 demonstrate why fixed named-set/version
  models alone were too narrow.
