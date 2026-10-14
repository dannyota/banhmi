# Verification

## Hosted check matrix

[CI](../.github/workflows/ci.yml) runs automatically on pushes to `master` and pull requests targeting
any branch. Feature-branch pushes and tag pushes do not trigger CI. Manual dispatch remains available.
Each PR update starts one automatic run. Only a superseded pull request run cancels. Push and manual
runs use distinct groups per run ID, so they never cancel or displace another commit's validation.
[Reusable checks](../.github/workflows/reusable-checks.yml) has no concurrency group.

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
| Semgrep SAST | Pinned full Semgrep Code scan with configured Go rules and reported Go paths; only exact independently reviewed transition entries may pass | Accepted findings and parser gaps stay visible; new, changed, stale, or unproved records fail; public reports do not prove every rule ran |
| Semgrep SCA | Pinned Semgrep Supply Chain scan with positive root `go.mod` resolution | Static dependency analysis; public reports cannot prove absence of partial dependency parser errors |
| Generation (`generation`) | Master and event-base migration guards, clean replay of all corpus and auth chains, six corpus schema tests on independent databases, omitted-function and omitted-trigger controls, clean whole-config sqlc 1.31.1 generation, regenerated-file artifact, and generated/dependency drift rejection | One disposable PostgreSQL 17 service with pinned pgvector and synthetic inputs; no populated upgrade, catalog comparison, or real-document proof |
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

The [SAST transition inventory](../docs/specs/security-ci.md#reviewed-transition-inventory) permits
reviewed source-only restructuring while retaining full scans and all required jobs. It binds each
accepted record to exact source bytes, supporting files, scanner rules, and complete locations.
Implementation and hosted controls must pass before the policy can admit a change. Keep valid
compatibility and regression tests. A package move alone does not make a test obsolete.
Transition CI success does not clear the plan's named ingestion and deployment gates.

The required `generation` job in [reusable checks](../.github/workflows/reusable-checks.yml) uses
one pinned PostgreSQL 17 service with pgvector. Checkout fetches full history without persisted
credentials. The [history guard](../tools/ci/check_migration_history.py) resolves the trusted base
from the pull request base SHA, push `before` SHA, or fetched `master` for manual dispatch when
`master` is the repository default branch. It binds the candidate to the event SHA, requires full
commit SHAs and available history, and compares final Git trees only. For every supported event,
it also fetches `refs/heads/master` from `origin` without tags and requires both comparisons to pass.
It compares every path, mode, object type, and object ID under the six frozen legacy directories,
including non-SQL files. It rejects edits, mode changes, or removals of existing auth files,
including bootstrap SQL, against both bases. Auth migration additions must follow the inherited numeric versions,
including those on the event base when a pull request targets a non-master branch.
Filename, extension, regular-file, and version validations remain required. Follow the
[chain rules](engineering.md#data-and-generated-code).

The [replay helper](../tools/ci/replay_generation.sh) runs `cmd/migrate` with the example config
and `BANHMI_DATABASE_*` variables against fresh corpus databases. It runs `cmd/auth-migrate`
with `BANHMI_AUTH_MIGRATION_DATABASE_URL` against a separate fresh auth database.
The [schema runner](../tools/ci/run_schema_tests.sh) runs `TestReleaseActivationRace` alone on
its own replayed database and the other five retained schema tests on a separate rollback database.
The [presence guard](../tools/ci/check_schema_tests.py) requires each of the six named tests
exactly once and rejects missing, duplicated, unexpected, skipped, or failing tests, including
nested skips. The runner then creates two disposable template copies of the rollback database.
It drops `bronze.reject_immutable_change()` with `CASCADE` in one and the
`observation_revision_immutable` trigger in the other. Each copy runs only
`TestImmutableEvidenceAndReviewConstraints` with `go test -json`. The expected-failure mode
requires exactly one run and one failure of that test, its package failure, no skips, and no other
test or package execution. The test output must contain the literal detection message
`operation succeeded, want PostgreSQL state 55000`. Test and package output containing
`connection refused`, `failed to connect`, or `fixture`, ignoring case, fails the control even
when the detection message appears. Event-capture errors fail the control. The Go tests remain unchanged.

After replay and schema tests, the [output helper](../tools/ci/sqlc_outputs.py) uses the hosted
runner's `yq` to read every `gen.go.out` in `sqlc.yaml`. It deduplicates and sorts the directories.
Each output must be a nonempty relative repository directory, without traversal, `.git` components,
symlink components, or unsafe path characters. Entries with Go generation must declare an output.
Every custom `output_*_file_name` must be a basename using only letters, digits, underscores,
dots, or hyphens and ending in `.go`. Every `output_files_suffix` must contain no path separators
or `..`. Discovery validates all entries before cleanup starts.
The helper deletes all contents of every validated output directory, including tracked files,
before `sqlc generate` reads the whole configuration. Git restores no files before comparison.
Files committed but not regenerated therefore appear as deletions and fail drift rejection.

The helper stages only regenerated regular files from those directories, preserving their repository
paths, and rejects symlinks, other non-regular files, or an empty artifact. The job uploads that
staged tree as `generation-${{ github.sha }}`, including regenerated hidden files, and retains it
for seven days. Artifact upload precedes [drift rejection](../tools/ci/reject_generation_drift.sh).
The drift check reports paths in the step summary and uses `git status --porcelain` and
`git diff HEAD --exit-code` over every discovered directory. It rejects tracked, untracked, and
ignored output changes, plus tracked, untracked, or ignored `go.mod` and `go.sum`. Review the generated
artifact before importing Go. The final committed tree must have zero drift. An unconditional
`Record generation scope` step records the job's limits even on failure.

Required reusable jobs are `hygiene`, `gitleaks`, `semgrep-sast`, `semgrep-sca`, `generation`,
`go`, `browser`, and `auth-postgres`. Both aggregates remain fail-closed. The Go, browser, and
auth PostgreSQL jobs depend on `generation`. Real-document, deployment, and release evidence
lanes remain separate workflows that gate milestones, not merges. The
[Goose migration plan](../docs/plans/goose-migrations.md#evidence-and-remaining-gates) retains
recorded transition evidence; those results do not prove this job on an untested commit.

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
