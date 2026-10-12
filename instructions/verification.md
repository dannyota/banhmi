# Verification

## Hosted check matrix

[CI](../.github/workflows/ci.yml) runs automatically on pushes to `master` and pull requests targeting
any branch. Feature-branch pushes and tag pushes do not trigger CI. Manual dispatch remains available.
Each PR update starts one automatic run. Only a superseded pull request run cancels. Push and manual
runs use distinct groups per run ID, so they never cancel or displace another commit's validation.
[Reusable checks](../.github/workflows/reusable-checks.yml) has no concurrency group.
[Corpus generation](../.github/workflows/corpus-source.yml) uses a separate `corpus-source-` prefix
and the same PR-only cancellation rule, so it cannot cancel its caller.

Both aggregates use `always()` and [the result gate](../tools/ci/check_results.py). The reusable
aggregate depends on every child job. The final `CI` aggregate depends on every other caller job.
Each gate requires every declared dependency result to equal `success`. Failure, skipped, cancelled,
missing, or malformed results fail the gate. The summary lists actual child results, including missing
results, before recording the verdict. Preserve read-only permissions, full commit SHA action pins,
checkouts without persisted credentials, and both aggregate gates. Local reusable workflow references
use the candidate's checked-out repository and do not use remote action pins.

| Job | Checks and inputs | Limits |
|---|---|---|
| Repository hygiene | Python CI helper tests, workflow contracts, portable maintained Markdown checks, actionlint 1.7.11 and ShellCheck 0.11.0 | Static repository rules, not runtime behavior |
| Gitleaks secrets | Pinned, checksum-verified Gitleaks scans all fetched Git history; findings and execution errors fail | Private redacted output; only the four fixed public-checksum fingerprints in the security spec; no baseline or configurable allowlist |
| Semgrep SAST | Pinned Semgrep Code scan with configured Go rules and reported Go paths | Authenticated OSS engine; public reports do not prove every rule ran |
| Semgrep SCA | Pinned Semgrep Supply Chain scan with positive root `go.mod` resolution | Static dependency analysis; public reports cannot prove absence of partial dependency parser errors |
| Auth source generation and drift | Auth sqlc entry, Go formatting and dependencies, Atlas checksum; canonical and migrated PostgreSQL 17 schemas, capability roles and memberships; allowlisted artifact and drift rejection | Disposable databases and synthetic credentials; no source write permission |
| Default Go build, vet, and short tests | `go build ./...`, `go vet ./...`, `go test -short ./...`; Go version from `go.mod`, CGO enabled | No live sources, services, database, corpus, ONNX, or evaluation inputs; short-mode guards skip tests |
| Website browser integration | Runner Chrome, pinned chromedp, `TestBrowser*` in `pkg/web`, fake auth and evidence over `httptest` | Synthetic browser interactions, not a deployed website or real Google |
| Auth PostgreSQL integration | Auth migrations, missing-role bootstrap guards, complete synthetic role bootstrap; identity, migration, cleanup, and OAuth package tests when present | Disposable PostgreSQL 17; distinct migration, runtime, and test URLs; no production connections |
| Required checks aggregate and final CI | Requires every child result to equal `success`; reports actual results | Failed, skipped, cancelled, and missing dependencies fail the aggregate |

Hygiene installs actionlint and ShellCheck from official release archives. It verifies both against
[repository-recorded SHA-256 values](../tools/ci/lint-tools.sha256) before extracting or executing
either binary. The checksum manifest must contain exactly one valid record per expected archive
and no other records. actionlint checks workflow expressions and inline shell with the verified ShellCheck.
ShellCheck also checks every `tools/ci/*.sh` script. Python contract tests use the hosted runner's
preinstalled `yq` to parse workflow YAML. They reject remote action references without full commit
SHAs, jobs without explicit read-only permissions, checkouts with persisted credentials, and aggregate
dependency or required-job list drift. The [required-job inventory](../tools/ci/required-jobs.txt)
records required jobs for each aggregate independently of workflow definitions. Contract tests reject
missing inventory jobs or aggregate dependencies, conditional gate steps except `always()`, and
`continue-on-error` values other than absent or literal `false` on every job and step in every workflow.
There are no exceptions.

The three security jobs follow the [security CI contract](../docs/specs/security-ci.md).
They scan independently and feed the reusable aggregate. Semgrep receives only the named scan
token for eligible events. Missing credentials and denied events fail instead of skipping.
Keep raw scanner output and reports out of logs and artifacts. The
[security CI plan](../docs/plans/security-ci.md) records source and hosted verification status.

[Corpus source generation](../.github/workflows/corpus-source.yml) runs only on manual dispatch or
an explicit reusable call. It uses disposable PostgreSQL 17 with pgvector and local trust auth.
[CI helpers](../tools/ci/) guard migration bytes and checksum prefixes, permit at most one appended
migration per affected schema, derive new routine and trigger definitions, and reject edits or
removals needing explicit migrations. They verify clean and upgrade replay against canonical catalog
objects and run `internal/corpus/schematest` with skipped or absent tests rejected. The allowlisted
artifact contains corpus sqlc output, new migrations, checksums, and a verification report.
The read-only workflow rejects generated drift and never commits output.
Auth and corpus generation are separate lanes. Review downloaded artifacts before importing them.

## Evidence standards

Match evidence to the claim:

- Unit and short package tests can prove coded behavior.
- Database integration can prove schema and query behavior.
- Real-document evaluation can prove corpus behavior.
- A hosted smoke check can prove the deployed MCP contract.
- Only a successful deployment and post-deployment check can prove deployed behavior.
- Fake Google and in-process HTTP tests can prove protocol handling without proving Google or MCP
  client compatibility.
- A configured hosted browser smoke check can prove one named Google and client flow for the tested
  commit and origin.

Record the exact commit, workflow run, inputs, and scope of each result. A short-test result cannot
substitute for PostgreSQL integration, transaction races, cleanup, or runtime/operator restrictions.

## Isolated inputs and missing-run gates

- Keep production credentials and production connections out of pull request jobs. Use disposable
  service databases and synthetic credentials. Only Semgrep scan jobs may receive the repository
  scan token under the [security CI trust boundary](../docs/specs/security-ci.md). Fork and Dependabot
  PRs receive no scan token. Real-corpus checks need isolated versioned snapshots.
- Add hosted inputs and runners before enabling source integration, pipeline execution, corpus or
  workflow evaluations, tagged ONNX builds, real Google/client checks, or release automation.
  The current check matrix does not prove those behaviors.
- Keep retrieval and workflow evaluation evidence separate. Check every affected jurisdiction.
- Do not claim hosted proof until GitHub runs the exact published commit. Missing, skipped, or
  unavailable required checks are gates, not passes. Review generated drift before dependent work.
- Apply [local execution and merge rules](../AGENTS.md#verification-and-review).
