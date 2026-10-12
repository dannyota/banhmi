# Security CI

The owner selected Gitleaks for secrets and Semgrep for static application security testing
(SAST) and software composition analysis (SCA). The checks are coded. Initial GitHub scans failed;
successful hosted evidence remains pending.
The owner confirmed `SEMGREP_APP_TOKEN` uses Service (CI) Agent (CI) scope and that Code and Supply
Chain are enabled for the repository. Live product and scan proof remain unverified.
The [implementation plan](../plans/security-ci.md) owns delivery and evidence gates.

## Decisions

- **Decision:** Require `gitleaks`, `semgrep-sast`, and `semgrep-sca` in the existing CI gate.
  **Why:** Each scanner covers a separate failure class and must produce its own result.
  **Rejected:** Optional scans, successful skips, and replacing Semgrep SCA with an OSS dependency tool.
  **Detail:** Keep every current check, trigger, concurrency rule, and generation boundary.
- **Decision:** Use authenticated Semgrep Code and Supply Chain with the OSS engine.
  **Why:** Product selection remains explicit and all scanner executable bytes stay pinned.
  **Rejected:** An automatic Pro engine download, Semgrep Secrets, and dependency builds during scans.
  **Detail:** `--oss-only` limits engine coverage. Do not claim Pro cross-file or full reachability analysis.
- **Decision:** Trust authorized same-repository branch writers with a repository-scoped scan token.
  **Why:** The owner approved normal repository-secret access for CI.
  **Rejected:** Fork credential access, `pull_request_target`, inherited secrets, and a new approval flow.
  **Detail:** This model does not protect a repository secret from a malicious authorized branch writer.

## Jobs and aggregation

Add three independent jobs to `.github/workflows/reusable-checks.yml`. Each has the same ID and
display name as its check above, `contents: read`, a bounded timeout, and no job-level condition.
Use the existing pinned checkout action with `persist-credentials: false`.
Add all three to the reusable aggregate's `needs`, `REQUIRED_JOBS`, and required-job inventory.
The reusable aggregate and outer `CI` retain `always()` and reject every non-success result.
Missing, skipped, cancelled, timed-out, or failed scans block `CI`.
Do not grant write permission, upload SARIF, add third-party actions, or use `continue-on-error`.

The caller explicitly maps only `SEMGREP_APP_TOKEN` to an optional `workflow_call` secret.
Map its value only for an allowed event; otherwise map an empty string:

| Event | Token eligibility |
| --- | --- |
| Push | Existing `master` trigger, actor is not `dependabot[bot]` |
| Manual dispatch | Same-repository dispatch, actor is not `dependabot[bot]` |
| Pull request | Head repository equals the current repository; actor and PR author are not `dependabot[bot]` |
| Other event | Denied |

The original PR head repository and author govern reruns. A maintainer rerun cannot upgrade a
fork or Dependabot PR. Each Semgrep job checks eligibility and token presence before launching
the scanner. Failure says which setup condition is missing, without printing credentials.
Fork and Dependabot PRs cannot pass these required scans directly. Authorized maintainers may
review their changes on a same-repository branch; there is no tokenless success fallback.
Expose the token only in the Semgrep scan step and container environment, never as a command
argument or to other jobs. A reusable-workflow input cannot override event eligibility.

## Pinned scanners

| Scanner | Immutable input |
| --- | --- |
| Gitleaks | `gitleaks_8.30.1_linux_x64.tar.gz` from the upstream `v8.30.1` release |
| Archive SHA-256 | `551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb` |
| Semgrep | `semgrep/semgrep:1.180.0@sha256:529ee8a277ec8adc5b534d7c74eea0a47e9de21d62852b6ba7ac6ba9566845c3` |

Verify the Gitleaks archive before extracting or executing the binary. Run the Semgrep image
on Linux amd64. A pin update includes contract review and hosted verification, not just a tag edit.
Never use `latest`, unpinned downloads, or scanner self-updates.

Gitleaks checks all fetched history with checkout `fetch-depth: 0` and
`gitleaks git --log-opts=--all --redact=100 --ignore-gitleaks-allow`.
Pass an explicit temporary config that extends pinned default rules without overrides, plus an
empty temporary `--gitleaks-ignore-path`. Do not load repository or environment allowlists.
Use exit code 1 for findings. No baseline, history rewrite, broad exclusion, or verbose output.
Capture both output streams and the report privately; never echo raw findings or attach reports.

## Semgrep contract

Run `semgrep ci --code` for SAST and `semgrep ci --supply-chain` for SCA. Both also use
`--oss-only --no-suppress-errors --disable-nosem --json` and a private
`--x-dump-scan-config-path`. Never enable `--allow-local-builds`, Secrets, or historical scans.
These are authenticated Platform scans, not `semgrep scan` substitutes.
Do not run package managers, repository build hooks, generated executables, or application code.
SCA reads the existing root `go.mod`; it does not create a replacement dependency graph.

Scan the full checked-out CI revision, including the PR merge revision. Run the container with
an explicit environment allowlist and a read-only source mount. Supply generic Semgrep repository,
branch, commit, and job metadata from the checked-out revision. Do not pass GitHub detection,
PR ID, or baseline variables that switch Semgrep to a diff scan or reset the checkout.
Provide writable temporary report and home directories, but no Docker socket or GitHub token.

A zero exit code alone is insufficient. A small version-bound report validator must require:

- CLI version `1.180.0`, valid JSON, empty `errors`, empty `skipped_rules`, and no findings.
  Fail on every returned finding, including monitor findings, independent of platform exit policy.
- Scan configuration `info.enabled_products` contains the requested product, `sast` or `sca`.
  `config.rules` contains rules for that product. Reject `engine_params.always_suppress_errors`
  and a nonempty `config.project_merge_base`. Fail on malformed or missing required proof.
- SAST has configured Go rules and Go source paths in the public report. A file count alone
  is not proof that each rule executed.
- SCA reports a `subprojects` entry whose `dependency_sources` includes root `go.mod`,
  `resolved` is true, and `resolved_stats.dependency_count` is positive. Require
  `resolved_stats.ecosystem: gomod` and `resolved_stats.resolution_method: LockfileParsing`.
  Require an SCA rule targeting `gomod`, including the supported `depends-on-either` shape.
  Reject unresolved subprojects and errors exposed in the public report.

Keep `--no-suppress-errors` and reject a nonzero CLI exit. This pinned `ci` command does not
support `--strict`; the report validator rejects public errors and skipped rules. The public `subprojects`
report omits dependency parser errors. A resolved subproject with a positive dependency count
can still be partial. These checks prove reported root resolution, not the absence of partial
dependency parser errors. Do not add instrumentation or private API polling to close that gap.

The pinned CLI derives `paths.scanned` from target candidates and emits an empty public
`skipped_rules` list. Those fields cannot prove every configured rule executed. Hosted negative
inputs must prove actual SAST and SCA detection before accepting the gate.

The private configuration flag and experimental subproject schema are bound to the pinned
upstream types. Schema drift fails the check and requires a reviewed pin update.
Do not copy generated upstream code. Keep validation limited to these proof fields.
Do not add platform ignores, repository ignore files, baselines, or monitor-only policies to
make findings disappear. Findings require review and remediation before a passing result.

## Output and setup

Keep raw scanner output, JSON, and scan configuration in private runner temporary files.
Delete them on completion. Disable shell tracing. Logs contain only fixed failure categories,
scanner versions, scanned revision, counts, a fixed version-match value, the bounded diagnostics
below, and a link to the authorized Semgrep project. Semgrep rejection summaries count findings,
errors, and skipped rules separately. Never print an unexpected version string.
Gitleaks may log at most 20 finding locations after independent Git metadata checks:

- The full lowercase 40-character commit ID appears in `git rev-list --all`.
- The path appears in that commit's `git ls-tree`. Remove only the exact checkout-root prefix.
  Require 1 to 240 ASCII letters, digits, dots, underscores, hyphens, or slashes. Reject absolute
  paths, control characters, and empty, `.` or `..` components.
- Start and end lines are integers from 1 to 1,000,000, with the end at or after the start.
- Rule names are limited to `generic-api-key`, `private-key`, and `aws-access-token`, verified
  against the pinned default rules. Other rule names are withheld; their findings still fail.

Report withheld and omitted location counts. If Git metadata is unavailable, emit a fixed message
and keep the findings failure. Location diagnostics never print match text, descriptions, secret
values, arbitrary fingerprints, or source contents. These checks authorize metadata only.

### Semgrep diagnostics

The independently reviewed metadata contract is coded and needs hosted evidence. A report with
pinned version `1.180.0` and list-shaped findings, errors, and skipped rules may log at most 40
finding locations and 20 errors. Findings and all execution or proof failures still block the check.

- Prove paths against `git ls-tree` for the checked-out `HEAD`. Remove only the exact `/src/`
  prefix. Require the same bounded ASCII path grammar and component checks used for Gitleaks.
  Do not print untracked paths, arbitrary report strings, or source contents.
- Finding start and end lines must be integers from 1 to 1,000,000, with end at or after start.
  Error lines may come only from a proven first span or first `PartialParsing` location.
  Unsupported or unproved locations are withheld.
- Rule IDs must match a configured `config.rules.rules[].id` exactly. Require 1 to 240 ASCII
  letters, digits, dots, underscores, or hyphens, beginning with a letter or digit and with no
  empty dot-separated component. Unsupported rule IDs are withheld.
- Error types must match fixed public `ErrorType` labels in the pinned JSON types. For tagged
  variants, the type field contains only the fixed label. Apply the location checks above to
  the first `PartialParsing` location. Never print raw variant payloads, messages, help,
  descriptions, fingerprints, fixes, match text, package fields, or skipped-rule details.
- Report withheld and omitted counts. Failed Git proof emits a fixed message and withholds
  locations. Missing configuration withholds rule IDs. Unknown versions or malformed report
  proof withhold all diagnostics.

Read a valid report after a nonzero scanner exit only to emit these diagnostics. Keep a fixed
execution-failure message and a failing result even if the report is otherwise clean. Diagnostic
failures cannot replace or turn the original scanner failure into success.

Do not publish source snippets, matched secrets, raw errors, or scanner reports as artifacts.
Semgrep receives repository scan data through its authenticated service. These jobs do not read
runtime secrets, corpus data, or user questions.

Use a Semgrep Service (CI) token with Agent (CI) scope, also described as a scan token.
Account, administrator, personal CLI, and read-only API tokens are not the intended credential.
The operator must confirm token scope, repository association, Code and Supply Chain activation,
and nonempty applicable policies. Secret-name presence does not prove any of those conditions.
Invalid authentication, unavailable products, empty rules, unsupported reports, download failure,
and network failure fail closed with a fixed actionable message.

## Sources

- [Semgrep CLI reference](https://docs.semgrep.dev/cli-reference) defines product and error flags.
- [Semgrep token scopes](https://docs.semgrep.dev/deployment/tokens) defines CI credentials.
- [Supply Chain setup](https://docs.semgrep.dev/semgrep-supply-chain/set-up-and-configure) defines activation and local-build opt-in.
- [Supply Chain support](https://docs.semgrep.dev/semgrep-supply-chain/requirements-and-feature-support) describes Go inputs and coverage.
- [Pinned CLI implementation](https://github.com/semgrep/semgrep/blob/v1.180.0/cli/src/semgrep/commands/ci.py) defines product and engine selection.
- [Pinned scan response](https://github.com/semgrep/semgrep/blob/v1.180.0/cli/src/semgrep/app/scans.py) defines private configuration output.
- [Pinned JSON types](https://github.com/semgrep/semgrep/blob/v1.180.0/cli/src/semgrep/semgrep_interfaces/semgrep_output_v1.py) define subproject proof.
- [Pinned Gitleaks documentation](https://github.com/gitleaks/gitleaks/blob/v8.30.1/README.md) defines history and redaction flags.
