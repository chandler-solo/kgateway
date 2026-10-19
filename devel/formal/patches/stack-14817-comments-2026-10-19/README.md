# Stack #14817 comment rewrites

One patch per PR, in stack order starting at #14698, inspected October 19, 2026.
Reviewed 747 added or changed comment groups, including tests, shell/YAML comments
and Make help. Rewrote 669; retained 78 already-concise comments or directives.
Markdown prose and PR discussion comments are outside this source-comment edit.

Each patch targets its PR's unedited head below. Apply it to that revision before
restacking. These are independent per-PR patches, rather than a cumulative series
for the top branch. Later PRs sometimes replace comments from earlier layers, so
restacking earlier rewrites can require reconciling comment context.

| PR | Target head | Patch | Rewritten/reviewed groups |
|---|---|---|---|
| [#14698](https://github.com/kgateway-dev/kgateway/pull/14698) | `4bcddfc313f2b7b9c66935f0b4290c3e8d609c43` | [01-pr-14698-comments.patch](01-pr-14698-comments.patch) | 267/296 |
| [#14816](https://github.com/kgateway-dev/kgateway/pull/14816) | `a027dd32a93c62cf1ecf84dffd5ea96849c7b628` | [02-pr-14816-comments.patch](02-pr-14816-comments.patch) | 66/79 |
| [#14699](https://github.com/kgateway-dev/kgateway/pull/14699) | `3b7d5b2bf8b04c639b0de0bc07ab5be94d19e47c` | [03-pr-14699-comments.patch](03-pr-14699-comments.patch) | 8/10 |
| [#14700](https://github.com/kgateway-dev/kgateway/pull/14700) | `e1738feb37b797b3e3fab3495e0765f56482cb98` | [04-pr-14700-comments.patch](04-pr-14700-comments.patch) | 26/31 |
| [#14701](https://github.com/kgateway-dev/kgateway/pull/14701) | `cebc61396b57244455433aa60ce12cd1d4e355e5` | [05-pr-14701-comments.patch](05-pr-14701-comments.patch) | 31/33 |
| [#14702](https://github.com/kgateway-dev/kgateway/pull/14702) | `cdbbb60fee6b5eeef8b62844854c2ca92312e091` | [06-pr-14702-comments.patch](06-pr-14702-comments.patch) | 19/21 |
| [#14703](https://github.com/kgateway-dev/kgateway/pull/14703) | `6451814be20ce9a304b5574af1d86857cc00f46a` | [07-pr-14703-comments.patch](07-pr-14703-comments.patch) | 21/22 |
| [#14716](https://github.com/kgateway-dev/kgateway/pull/14716) | `653a09152b05c35e31cb65edbbe9aa27045d73a0` | [08-pr-14716-comments.patch](08-pr-14716-comments.patch) | 26/29 |
| [#14717](https://github.com/kgateway-dev/kgateway/pull/14717) | `2a944b52b2b20cc2d492d28a3ed3e0de64a46433` | [09-pr-14717-comments.patch](09-pr-14717-comments.patch) | 29/30 |
| [#14718](https://github.com/kgateway-dev/kgateway/pull/14718) | `b9cd435a5e6e70dc99b1e8412bfb67a8eceb7973` | [10-pr-14718-comments.patch](10-pr-14718-comments.patch) | 52/60 |
| [#14719](https://github.com/kgateway-dev/kgateway/pull/14719) | `6c29edc503746ca4b0a39f44ef0bf7ccdfad48d5` | [11-pr-14719-comments.patch](11-pr-14719-comments.patch) | 56/62 |
| [#14720](https://github.com/kgateway-dev/kgateway/pull/14720) | `3530c295dba0fe71b5d67482c2c3c900837ecc50` | [12-pr-14720-comments.patch](12-pr-14720-comments.patch) | 24/29 |
| [#14721](https://github.com/kgateway-dev/kgateway/pull/14721) | `8e75468f4d0e055deb9b8f34e4254831f011e2a3` | [13-pr-14721-comments.patch](13-pr-14721-comments.patch) | 44/45 |

For a matching source snapshot, apply the corresponding patch with:

```sh
patch --batch --fuzz=0 -p1 -i /absolute/path/to/NN-pr-NUMBER-comments.patch
```

The rewrites retain lock order, generation and current-row checks, timer identity
and cancellation, carry recovery, EDS contribution precedence, shared assignments,
claim validation and lifetime, and per-gateway selector scope. They distinguish
cache installation from delivery and application, NACK from wholesale rollback,
and elapsed transition windows from confirmed propagation. Version comments retain
input-only churn and finite-hash/XOR collision limits.

Editorial validation: every patch applies at its pinned head with no offset or
fuzz, and application reproduces the intended files exactly. Every changed comment
belongs to that PR's diff. All bytes outside comments, including whitespace and
literals, are unchanged. Go files parse and remain gofmt-clean; the shell script
passes bash -n. Build directives, equality annotations and the shebang are unchanged.
All 13 remote base/head identities were rechecked after validation. No functional
suite was rerun for this comment-only change.

Only these patch files and this index were added to the research worktree. The
upstream snapshots were edited and tested in scratch copies. No patches were
applied to either working checkout, and neither Git nor GitHub was mutated.
Generated execution logs and receipts remain outside the repository.
