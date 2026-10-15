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
| Generation (`generation`) | Master and event-base migration guards, tool setup, clean whole-config sqlc 1.31.1 generation, staged regenerated-file artifact upload and immediate generated/dependency drift rejection, administrator provisioning and non-superuser replay of writer, release, and control chains alongside legacy and auth replay, six retained corpus schema tests on independent databases with omitted-function and omitted-trigger controls, fresh chain and store tests, and unconditional repository integrity verification | One disposable PostgreSQL 17 service with pinned pgvector and synthetic inputs; no populated upgrade, legacy catalog comparison, or real-document proof |
| Default Go build, vet, and short tests | Shared tracked-Go formatting check with repair artifact on failure, `go build ./...`, `go vet ./...`, `go test -short ./...`; Go version from `go.mod`, CGO enabled | No live sources, services, database, corpus, ONNX, or evaluation inputs; short-mode guards skip tests |
| Website browser integration | Runner Chrome, pinned chromedp, `TestBrowser*` in `pkg/web`, fake auth and evidence over `httptest` | Synthetic browser interactions, not a deployed website or real Google |
| Auth PostgreSQL integration | Auth migrations, missing-role bootstrap guards, complete synthetic role bootstrap; identity, migration, cleanup, and mandatory OAuth package tests with JSON presence and skip checks | Disposable PostgreSQL 17; independent race and cleanup databases; distinct migration, runtime, and test URLs; no production connections |
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

The required `go` job runs [the formatting contract](../tools/ci/check_format.sh) immediately
after its existing `actions/setup-go` step and before builds. This placement reuses the pinned
toolchain without adding setup to hygiene. The script requires the exact `go.mod` version from
the installed Go toolchain and uses that toolchain's `bin/gofmt`, with no formatter fallback.
Both Go environment queries clear `GOROOT`, disable `GOENV`, and set `GOTOOLCHAIN=local`.
It enumerates `git ls-files -z` without a pathspec and selects the `.go` suffix in Bash,
so inherited Git pathspec settings cannot change the file set. It uses `gofmt -l` regardless of package,
build tag, or generated header. sqlc output stays in scope because
[sqlc 1.31.1 uses `go/format.Source`](https://github.com/sqlc-dev/sqlc/blob/v1.31.1/internal/codegen/golang/gen.go#L239).
Untracked and ignored untracked files are outside this file set. Missing tracked files, symlinks
in any source-path component, sources resolved outside the physical repository root,
tool errors, and any unformatted file fail. The script prints the unformatted paths.
`make fmt` writes with the same formatter and file set; `make fmt-check` runs the same check as CI.
Both propagate failures. Local execution remains governed by [repository rules](../AGENTS.md#verification-and-review).

On formatting failure, the Go job uploads `go-format-${{ github.sha }}` with seven-day retention
before the failed job completes. It reuses the existing pinned upload action and grants no new
permissions. The upload uses `failure()`, the formatting step's failed outcome, and its published
`artifact-path` output; it does not mask that failure. The script requires a fresh staging directory
and initializes an incomplete-result marker. Its exit handler packages that directory and publishes
the upload path only after packaging succeeds. An existing directory fails without publishing a path,
so an earlier result cannot upload under the current SHA.
The artifact contains one fixed-name `go-format.tar`. The tar contains `result.txt` and `files/`,
with canonical copies of unformatted files under their exact repository paths, including hidden
files, newlines, colons, question marks, and asterisks. Only the fixed archive name reaches the
upload action's path validator. An interrupted or failed check can leave an incomplete-result marker;
partial repairs are not a complete formatting pass.

The primary checks the artifact's workflow run and exact commit before import. Validate every tar
member before extracting any bytes. Run the following validator from the matching checkout with
the archive path as its sole argument. It rejects absolute, empty, dot, traversal, duplicate, link,
device, unexpected, and untracked repair paths. It reads Git's NUL-delimited tracked names without
pathspecs. It permits only regular `result.txt`, the `files/` directory and its directories, and
regular repairs for tracked `.go` files. It also rejects incomplete results.

```python
# Validate a Go formatting archive before import.
import os
import subprocess
import sys
import tarfile

tracked = {os.fsdecode(p) for p in subprocess.check_output(
    ['git', 'ls-files', '-z']).split(b'\0') if p.endswith(b'.go')}
allowed_files = {'result.txt'} | {'files/' + p for p in tracked}
allowed_dirs = {'files'}
for path in tracked:
    parts = ('files/' + path).split('/')
    allowed_dirs.update('/'.join(parts[:i]) for i in range(1, len(parts)))
seen = set()
with tarfile.open(sys.argv[1], 'r:') as archive:
    for member in archive:
        name = member.name
        if member.isdir() and name.endswith('/'):
            name = name[:-1]
        if (name.startswith('/') or any(p in ('', '.', '..') for p in name.split('/'))
                or name in seen):
            raise SystemExit('Unsafe or duplicate archive path: ' + repr(name))
        seen.add(name)
        if member.isdir() and name in allowed_dirs:
            continue
        if not member.isfile() or name not in allowed_files:
            raise SystemExit('Unexpected archive member: ' + repr(name))
    if not {'result.txt', 'files'} <= seen:
        raise SystemExit('Missing result.txt or files directory')
    report = archive.extractfile('result.txt').read()
    if (not report.startswith(b'Go ')
            or report.startswith(b'Go formatting check did not complete.')):
        raise SystemExit('Incomplete formatting result')
```

After validation, extract into a new empty staging directory. Review `result.txt` and the complete
repair set. Before copying repairs, reject symlinks in every destination path component and require
each resolved destination to remain beneath the physical checkout root. Never extract directly into
the checkout or follow archive links. Import only the reviewed `files/` tree. After import,
compare each changed Go file with its committed version with all whitespace removed. The only
allowed difference is a removed `;` statement separator, which gofmt drops when it splits
statements onto separate lines. Any other non-whitespace change stops the formatting-only import
for separate review. Generated Go still requires the
reviewed generation artifact and zero generation drift. Hosted Python tests cover file selection,
check/write parity, repair contents, inherited environment settings, source symlinks, and failure
propagation. Workflow contracts exercise stale-directory rejection and this pre-import validator,
including hostile archive members, and pin placement,
artifact settings, and the absence of failure suppression.

### Dependency and pin updates

[Dependabot](../.github/dependabot.yml) checks weekly and opens update pull requests against the
default branch. Each ecosystem groups minor and patch updates and permits at most three open
version-update PRs. All version updates have a seven-day release cooldown. Major updates remain
separate. Security-update PRs have a separate GitHub limit and do not use the cooldown.
The commit prefixes are `chore(deps)` for Go, `ci(deps)` for actions, and `build(deps)` for containers.

| Ecosystem | Configured directory | Tracked inputs |
|---|---|---|
| `gomod` | `/` | Root `go.mod` and `go.sum` |
| `github-actions` | `/` | `.github/workflows/*.yml`; GitHub requires root discovery |
| `docker` | `/deploy/containerfiles` | Literal `FROM` image references in `Containerfile*` recipes; `ARG` references are excluded |

No npm, pip, or other package-manager manifest is tracked. Python packages installed inline in
container recipes are not pip manifests. The Compose file `deploy/compose/banhmi.yaml` uses a
filename outside Dependabot's Compose discovery pattern, so no `docker-compose` entry is enabled.
Renaming that file needs a separate slice. Container update PRs cover discoverable base-image
references, not package installation commands or downloaded runtime/model artifacts.

Dependabot [does not update images specified through `ARG`](https://docs.github.com/en/enterprise-cloud@latest/code-security/how-tos/secure-your-supply-chain/manage-your-dependency-security/configure-private-registries#limitations-and-workarounds).
These base-image references in `deploy/containerfiles/` require manual reviewed updates:

| Recipe | Manual-update exclusions |
|---|---|
| `Containerfile` | `${GO_IMAGE}`, `${DEBIAN_IMAGE}` |
| `Containerfile.fetch-smoke` | `${GO_IMAGE}`, `${DEBIAN_IMAGE}` |
| `Containerfile.cloudrun.onnx` | `${DEBIAN_FETCH_IMAGE}`, `${GO_IMAGE}`, `${RUNTIME_IMAGE}` |
| `Containerfile.ecs.embedder` | `${DEBIAN_FETCH_IMAGE}`, `${GO_IMAGE}`, `${RUNTIME_IMAGE}` |
| `Containerfile.ecs.onnx` | `${DEBIAN_FETCH_IMAGE}`, `${GO_IMAGE}`, `${RUNTIME_IMAGE}` |
| `Containerfile.ecs.server` | `${GO_IMAGE}`, `${RUNTIME_IMAGE}` |
| `Containerfile.pipeline-gpu` | `docker.io/nvidia/cuda:${CUDA_TAG}` |

Keep remote actions pinned to full commit SHAs with inline version comments such as
`# v7.0.1` or `# v4`. Dependabot maintains this
[version-comment format](https://docs.github.com/en/code-security/reference/supply-chain-security/supported-ecosystems-and-repositories#github-actions).
Review the new SHA and its tag together. Local reusable workflow references remain local.

Dependabot does not update the lint-tool versions and checksums in `tools/ci/install_lint_tools.sh`
and `tools/ci/lint-tools.sha256`, the Gitleaks archive checksums and Semgrep image in
`tools/ci/security_scan.py`, the SAST transition inventory, the sqlc tool version passed to its setup
action, or either PostgreSQL service image digest in `.github/workflows/reusable-checks.yml`.
These pins require separate reviewed changes with official release evidence and hosted checks.

Dependency PRs require independent review and the same successful required `CI` as other changes.
No workflow automatically approves or merges them. The
[contract tests](../tools/ci/test_workflow_contracts.py) parse the Dependabot configuration with hosted
`yq` and enforce ecosystems, directories, schedules, cooldowns, groups, limits, commit prefixes, action pin
comments, and the reviewed workflow/action inventory without merge or approval automation.
These source checks do not prove repository settings or arbitrary script behavior.

The existing security trust boundary denies scan tokens to Dependabot PRs. Both Semgrep jobs fail
for those events, so their required `CI` cannot pass. Under the
[approved maintainer path](../docs/specs/security-ci.md#jobs-and-aggregation), a
maintainer adopts the update onto a new same-repository branch and opens a new pull request.
That pull request requires independent review and successful required `CI`. Rerunning the original
Dependabot PR remains denied because its original head repository and author govern eligibility.
Do not bypass or weaken these checks.

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
A preceding administrator step runs [corpus provisioning](../tools/ci/run_corpus_chain_tests.sh)
with `provision`: it creates separate writer, release, and control databases, installs the pinned
`vector` and `btree_gist` extensions in writer and release, creates synthetic password-free logins,
and runs each bootstrap with its psql role variables under trust authentication. Replay then runs
`cmd/corpus-migrate -chain <chain>` as each non-superuser migration login. After the retained schema
tests, the corpus helper runs the fresh chain replay and guard tests with the three chain-specific
test database URL variables. The tests provision independent disposable fixtures on the same instance.
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

Before replay and schema tests, the [output helper](../tools/ci/sqlc_outputs.py) uses the hosted
runner's `yq` to read every `gen.go.out` in `sqlc.yaml`. It deduplicates and sorts the directories.
Each output must be a nonempty relative repository directory, without traversal, `.git` components,
symlink components, or unsafe path characters. Entries with Go generation must declare an output.
Every custom `output_*_file_name` must be a basename using only letters, digits, underscores,
dots, or hyphens and ending in `.go`. Every `output_files_suffix` must contain no path separators
or `..`. Discovery validates all entries before cleanup starts.
The helper deletes all contents of every validated output directory, including tracked files,
before `sqlc generate` reads the whole configuration. Git restores no files before comparison.
Files committed but not regenerated therefore appear as deletions and fail drift rejection.
All eight entries read local migration files and query paths: intake and document use the writer
chain, auth uses the auth chain, and bronze, silver, gold, ingest, and config use their legacy chains.
No entry configures database-backed analysis, so generation does not require replay.

The helper stages only regenerated regular files from those directories, preserving their repository
paths, and rejects symlinks, other non-regular files, or an empty artifact. The job uploads that
staged tree as `generation-${{ github.sha }}`, including regenerated hidden files, and retains it
for seven days. Artifact upload precedes replay, every test step, and
[drift rejection](../tools/ci/reject_generation_drift.sh), so test failure cannot prevent the upload.
Immediately after upload, the drift check reports paths in the step summary and uses
`git status --porcelain` and `git diff HEAD --exit-code` over every discovered directory.
It rejects tracked, untracked, and
ignored output changes, plus tracked, untracked, or ignored `go.mod` and `go.sum`. Provisioning,
replay, and every test step run only after this check passes. Generated code therefore matches
the committed tree before any test code runs. A query change with stale generated Go still uploads
its artifact, then fails before tests until the reviewed artifact is imported.

After the strict pre-test drift check, before provisioning, replay, or tests, the job extracts
`tools/ci/reject_generation_drift.sh` and `tools/ci/sqlc_outputs.py` with `git show HEAD:<path>`.
It keeps adjacent copies in a fresh `committed-checkers.XXXXXX` directory under `$RUNNER_TEMP`,
outside the checkout and reserved for the checkers by convention. It records the directory, each
`git rev-parse HEAD:<path>` blob ID, and the commit ID in step outputs before tests can alter Git.
It verifies both extracted copies with `git hash-object --no-filters` before publishing the outputs.
After schema, chain, and store tests, the `always()` step copies these saved bytes into a fresh
`post-test-checkers.XXXXXX` directory under `$RUNNER_TEMP`. It checks both copies against the
recorded blob IDs and requires `HEAD` to match the recorded commit. Both the saved directory and
the execution directory must contain exactly the two expected regular files. Hidden extras,
directories, symlinked directories, and symlinked files fail validation. Missing outputs, missing copies,
hash mismatches, changed `HEAD`, and checker errors fail the step. The step runs
`bash "${checker_run}/reject_generation_drift.sh" --repository` from the repository root.
The shell copy invokes `sqlc_outputs.py` beside itself, so neither checker runs from the working tree.
Provisioning, replay, and tests must not read or write either reserved checker directory.
Extraction after tests would trust Git objects and `HEAD` that test code could replace.
It checks the entire repository with `git status --porcelain --untracked-files=all` and
`git diff HEAD --exit-code`. Any tracked
change or untracked non-ignored file fails, including changes to queries, migrations, configuration,
or generated code. The output helper reads committed `sqlc.yaml` through `git show HEAD:sqlc.yaml`
and the hosted `yq` parser to derive every schema, query, and Go output scope. Scalar and list input
paths are included. A glob guards its entire parent before the first wildcard component, so new
files cannot escape through glob expansion. The guard also includes all of `deploy/corpusmigrations/`,
`deploy/migrations/`, and `deploy/authmigrations/`, covering replay chains absent from sqlc.
`git ls-files --others --ignored --exclude-standard` rejects ignored additions in these scopes.
Working-tree configuration edits cannot shrink the scope. Ignored caches outside these scopes
remain allowed only by this post-test mode; the first drift check retains its strict ignored-output
and dependency checks. No repository-state snapshot file is used.

The post-test `--repository` mode holds status, guarded scopes, and ignored additions in shell
variables. A failed status, scope, or ignored-addition command fails the check. The decision uses
these in-memory values and the exit status of `git diff HEAD --exit-code`, with no capture files.
It writes the step summary and stderr report after deciding the result and never reads them back.
The fixed pre-test capture paths are unchanged.

Test-time edits restored before the final check remain undetected. This is an end-state check,
not isolation from same-user test code. The temporary directories are private by default but remain
accessible to tests running as the same user. Hash checks detect changes to saved checker bytes;
they do not isolate the runner, Git index, configuration, object database, tools, environment, or
runner command files. Git replacement refs and PATH or environment poisoning remain exposed.
A surviving same-user process can race checker verification and execution. Closing these residual
exposures requires process isolation. Generation after tests also
missed transient edits restored before generation. Review the generated artifact before importing Go.
The final committed tree must have zero drift. An unconditional `Record generation scope` step records the job's limits
even on failure.

The required `auth-postgres` job keeps the missing-role bootstrap guards before granting either
synthetic deployment login a capability role. It migrates and bootstraps seven disposable databases
with the same auth chain and runtime/operator logins. `banhmi_auth_test` runs ordinary identity,
auth migration, and OAuth server tests serially. Each isolated selection runs alone:

| Database | Test selection |
|---|---|
| `banhmi_auth_login_race` | `TestPostgresStoreIntegration/concurrent_login_consumption_has_one_winner` |
| `banhmi_auth_block_race` | `TestPostgresStoreIntegration/block_timestamp_follows_the_account_lock` |
| `banhmi_auth_code_race` | `TestOAuthPostgresConcurrentAuthorizationCodeReplay` |
| `banhmi_auth_refresh_race` | `TestOAuthPostgresConcurrentRefreshReplay` |
| `banhmi_auth_consent_race` | `TestOAuthPostgresConcurrentConsentApprovalAndDenial` |
| `banhmi_auth_cleanup` | All `cmd/auth-cleanup` tests, including `TestCleanupIsBoundedAndPreservesAudit` |

The ordinary runs exclude only those five race selections. The identity parent runs again for each
isolated subtest because those subtests do not need the ordinary subtests' account fixture. Each
isolated invocation points its owner, migration, and runtime URLs at the same isolated database.
Sequential execution also keeps the identity test's instance-wide lock probe from observing another
race. Cleanup receives its own database because it deletes expired rows across tables.
All invocations use `go test -json -count=1` without `-short` and capture events outside the repository.
Shell pipeline failures fail the test step. The unconditional
[auth presence guard](../tools/ci/check_auth_tests.py) rejects missing or malformed captures,
incomplete package or test execution, zero tests across the package list, any failure, and test or
package skips, including nested subtests. All four packages must finish successfully; `cmd/auth-migrate` currently
has no test files. Only its package-level skip is allowed, with the exact Go `[no test files]`
output and no test execution. The other packages must execute their required tests.
The [required test inventory](../tools/ci/required-auth-tests.txt) lists every current top-level test in
`pkg/oauthserver` and `pkg/identity`, plus cleanup and the two identity race subtests. Identity's full
inventory includes Google OAuth/OIDC login, callbacks, provider exchange, and pending-consent binding.
A listed test must run and pass. New unlisted tests are allowed but still may not skip or fail.
The repeated identity parent may pass more than once; every run must finish. These synthetic checks
do not prove real Google, named-client, or deployed OAuth compatibility.

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
