# Security CI

The owner selected Gitleaks for secrets and Semgrep for static application security testing
(SAST) and software composition analysis (SCA). The checks are coded. Hosted Gitleaks, SCA, and
ordinary checks pass on the recorded revisions. The owner requested a transition gate for the approved
restructure. The reviewed-inventory contract below is designed, not implemented or validated.
The owner confirmed `SEMGREP_APP_TOKEN` uses Service (CI) Agent (CI) scope and that Code and Supply
Chain are enabled for the repository. Hosted negative controls proved detection for both products.
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
- **Decision:** Exempt only the four historical public checksum findings listed below.
  **Why:** Independent inspection proved they verify public artifact bytes and contain no credentials.
  **Rejected:** Value, path, rule, or whole-commit exclusions; baselines; history rewriting.
  **Detail:** The wrapper generates four immutable commit fingerprints in a private ignore file.
  The implementation plan owns review status and hosted detection evidence.
- **Decision:** Keep full SAST scans and admit only exact, independently reviewed transition entries.
  **Why:** Unchanged legacy findings must remain visible without forcing unrelated runtime repairs
  into source-only restructuring. New and changed findings must block.
  **Rejected:** Directory or rule exclusions, automatic enrollment, Semgrep diff baselines, and
  blanket monitor policies. Diff scans hide unchanged debt and need a separate base-revision contract.
  **Detail:** A source-bound inventory replaces the universal SAST finding and error rejection only
  for listed findings and `PartialParsing` records. Gitleaks and SCA retain their strict contracts.

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
explicit private `--gitleaks-ignore-path` containing only the four checksum fingerprints below.
Do not load repository or environment allowlists.
Use exit code 1 for findings. No baseline, history rewrite, broad exclusion, or verbose output.
Capture both output streams and the report privately; never echo raw findings or attach reports.

### Historical public checksum exception

The complete exception inventory is:

| Commit | Repository path | Rule | Start line | Public checksum |
| --- | --- | --- | --- | --- |
| `eada71491b8224dca2981955a0fbc792c384b302` | `deploy/containerfiles/Containerfile.ecs.onnx` | `generic-api-key` | 25 | `TOKENIZER_SHA256` |
| `eada71491b8224dca2981955a0fbc792c384b302` | `deploy/containerfiles/Containerfile.ecs.onnx` | `generic-api-key` | 27 | `LIBTOKENIZERS_SHA256` |
| `622392a97f3e70219db128e6c4324379f2cd079a` | `deploy/containerfiles/Containerfile.ecs.embedder` | `generic-api-key` | 26 | `TOKENIZER_SHA256` |
| `622392a97f3e70219db128e6c4324379f2cd079a` | `deploy/containerfiles/Containerfile.ecs.embedder` | `generic-api-key` | 28 | `LIBTOKENIZERS_SHA256` |

Independent private inspection verified that all four matches are 64-character lowercase
hexadecimal SHA-256 checksums consumed by `sha256sum`. The tokenizer checksum matched the bytes
of the public [tokenizer artifact](https://huggingface.co/onnx-community/Qwen3-Embedding-0.6B-ONNX/blob/main/tokenizer.json).
The library checksum matched the official release API digest for `libtokenizers.linux-arm64.tar.gz`
in [tokenizers v1.27.0](https://api.github.com/repos/daulet/tokenizers/releases/tags/v1.27.0).
The immutable repository locations identify the reviewed bytes; the tokenizer's `main` URL is
provenance, not a promise that future upstream bytes remain unchanged. No matched values are needed
in this policy or its diagnostics.

Pinned Gitleaks `v8.30.1` matches `commit:path:rule:start-line` fingerprints by exact equality.
Its [implementation](https://github.com/gitleaks/gitleaks/blob/v8.30.1/detect/detect.go)
also accepts a commit-free `path:rule:start-line` form. That broader form is prohibited.
The fingerprint does not include matched content or columns. The exception therefore covers the
reviewed rule matches on those exact immutable lines, not a general assertion that a variable name
or hexadecimal value is safe. A new commit, another path, another rule, or another start line is
outside this exception, even when its value or variable name is identical.

Keep the four entries as fixed wrapper constants. Write exactly those entries to a private temporary
file and pass its path explicitly. Do not derive entries from scanner reports, caller inputs, repository
ignore files, environment configuration, or discovered findings. Keep default rules and
`--ignore-gitleaks-allow`. Every returned finding and every scanner execution failure still fails.
Do not subtract four from finding counts or accept a failure because its count equals four.
Delete the private file with the other scanner temporary files. A scanner pin change must recheck
fingerprint semantics and the hosted negative tests. Any inventory expansion needs a separate
evidence-backed policy change and independent adversarial review.

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

- CLI version `1.180.0`, valid JSON, list-shaped `results`, `errors`, and `skipped_rules`, and no
  skipped rules. SCA requires empty findings and errors. SAST applies the transition inventory only
  after all scanner and source proof below succeeds. Monitor status never grants an exception.
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

Keep `--no-suppress-errors` and reject every nonzero CLI exit, including exit 1. This pinned `ci`
command does not support `--strict`; only reviewed SAST `PartialParsing` records may pass under the
transition contract. Other public errors and all skipped rules fail. The public `subprojects`
report omits dependency parser errors. A resolved subproject with a positive dependency count
can still be partial. These checks prove reported root resolution, not the absence of partial
dependency parser errors. Do not add instrumentation or private API polling to close that gap.

The pinned CLI derives `paths.scanned` from target candidates and emits an empty public
`skipped_rules` list. Those fields cannot prove every configured rule executed. Hosted negative
inputs must prove actual SAST and SCA detection before accepting the gate.

The private configuration flag and experimental subproject schema are bound to the pinned
upstream types. Schema drift fails the check and requires a reviewed pin update.
Do not copy generated upstream code. Keep validation limited to these proof fields.
Do not add platform ignores, scanner ignore files, Semgrep diff baselines, or monitor-only policies.
The following inventory classifies full-scan output in the wrapper; it never changes scanner inputs
or removes findings from the private report or Semgrep project.

### Reviewed transition inventory

This gate permits source-only merges during the approved layout transition. It does not approve
deployment, source ingestion, or reuse of unsafe behavior in the new Vietnam system. Keep every
ordinary job and valid regression test. Existing compatibility contracts remain required.

Store the reviewed inventory in `tools/ci/semgrep-legacy.json`, separate from the helper. This avoids
a self-referential checksum when a finding belongs to `tools/ci/security_scan.py`. Finalize helper
bytes before recording its hash. Never omit source binding for that file or hash only its call site.
Load only this fixed tracked file from the checked-out revision, with no environment or caller override.
The inventory itself must equal its tracked regular Git blob. It cannot enroll its own contents or
appear as a supporting source, which would create another self-referential hash.

Use JSON schema version 1 with exactly `schema_version`, `semgrep_version`, `semgrep_image`,
`findings`, and `partial_parsing` at the top level. Pin the version and image to the scanner above.
Both entry lists may be empty. Reject duplicate JSON keys, unknown inventory fields, malformed
values, duplicate IDs, and duplicate matching identities. Do not load a general scanner baseline.

Every entry has `id`, `path`, `source_sha256`, `supporting_sources`, `disposition`, `rationale`,
and `evidence`. `supporting_sources` is a list of objects containing only `path` and `source_sha256`.
IDs use 1 to 80 lowercase letters, digits, and hyphens. Evidence identifies the reviewed revision,
hosted run, and independent disposition record using public repository links or repository-relative
paths. `rationale` is a nonempty string; `evidence` is a nonempty list of nonempty link/path strings.
These are review records, never scanner-controlled text or log output.
Finding dispositions are `false-positive`, `fixed-but-audit`, `trusted-operator-audit`, or
`legacy-risk`. Parser records use only `coverage-gap`. A known defect is not a false positive.

| Entry | Additional identity fields |
| --- | --- |
| Finding | `rule_id`, `rule_sha256`, `start`, `end`; match `check_id`, path, and the complete start/end positions |
| Partial parsing | `code`, `level`, `rule_id`, `rule_sha256`, `locations`, `spans`; match the entire tagged `type: ["PartialParsing", locations]` and the separate span list |

Positions contain `line`, `col`, and `offset`. Lines and columns are positive integers at most
1,000,000; reject booleans. Preserve reported offsets, including the pinned schema's dummy `-1`;
use null only for an absent offset. Nonnegative offsets cannot exceed the source byte length.
Start must not follow end. A parser location contains `path`, `start`, and `end`. Require a nonempty
list, sort by the complete location tuple, retain multiplicity, and bind every location to the entry's
single path and source hash. A different path, extra location, or changed position does not match.
Parser `code` is an exact integer and `level` is exactly `error`, `warn`, or `info`. Parser `rule_id`
and `rule_sha256` are both null if and only if the reported rule ID is absent. Otherwise require
the configured rule ID exactly once and its full canonical rule hash, as for findings below.
A reported top-level error path must agree with the entry. Inventory `spans` is null only when the
report omits `spans`; otherwise it is a list. Preserve an empty list as empty, not null. An explicitly
null report field or another unsupported shape fails. Normalize each pinned `ErrorSpan.file` to
`path` and retain its complete `start` and `end`. Apply the same path, position, source-hash, and
single-file checks as for `locations`. Sort spans by the complete location tuple without deduplicating.
Never merge spans with `locations` or discard positions because both fields report the same range.
Match span presence and the complete normalized list, including multiplicity, against the inventory.
For this bounded extension, a nonempty span list must also equal the complete normalized `locations`
list. The three hosted parser records have exactly this shape; a different relationship fails pending
a reviewed schema extension.

Only `file`, `start`, `end`, and the pinned optional `source_hash`, `config_start`, `config_end`,
`config_path`, `context_start`, and `context_end` fields are allowed in a reported span. The config
and context fields must be absent or null; reject every non-null value instead of dropping additional
positions. An optional scanner `source_hash` must be a string but is not trusted, recorded, or printed;
the independent complete-file hash required by source proof supplies identity. Inventory span objects contain
only `path`, `start`, and `end`. Unknown error types, empty type-location lists, malformed variants,
unknown span fields, and unsupported span geometry cannot enroll.

Use the existing diagnostic path and rule grammar. Normalize only the exact `/src/` prefix in report
paths. Require each path to be a regular tracked file at the scanned `HEAD`, with no symlink in the
resolved path. Verify the mounted file bytes equal the Git blob bytes and their SHA-256 equals the
entry's 64-character lowercase `source_sha256`. Hash the complete file privately. A HEAD-only hash
does not prove the bytes Semgrep read. Require the path in `paths.scanned` and reject unavailable
source proof. Changes anywhere in the file invalidate its entries, including unrelated edits.
Apply the same byte and Git proof to each supporting source, without requiring nonscanned data
assets in `paths.scanned`. Bind files whose guards, configuration, or embedded assets establish the
disposition: redirect storage validation, static response assets, and trusted executable configuration
are examples. Reviewers determine these dependencies from the code, not the scanner's sink alone.
An empty list requires a self-contained rationale. Reject duplicate supporting paths and the entry's
own path. A changed or missing supporting source invalidates the entry and requires renewed review.
Independent review must also examine new callers and changed input boundaries. A source-bound
inventory cannot prove that a newly added caller leaves an unchanged sink safe.

Require every finding rule and non-null parser rule in `config.rules.rules` exactly once.
Each non-null `rule_sha256` is the 64-character lowercase SHA-256 of that
complete configured rule serialized as UTF-8 JSON with sorted object keys, compact separators, and
ASCII escaping; array order stays unchanged. Reject nonfinite numbers and duplicate JSON keys.
Do not print the rule object. A changed rule body or scanner pin needs renewed review even if its ID
is unchanged. Finding identity uses the source hash, full positions, and rule hash, not volatile
Semgrep fingerprints, messages, or a guessed match-count subtraction.

First validate exit status, report shape, version, product, full-scan settings, rules, Go coverage,
and skipped rules. Only then validate inventory and source proof and compare report records.
Require a bijection: each entry consumes exactly one matching finding or parser record and each
report record consumes exactly one entry. Duplicate findings, duplicate errors, extra occurrences,
unmatched entries, removed files, missing rules, and missing expected records fail. A stale entry
must be removed through review; never silently ignore it or regenerate the inventory from a scan.
All new or changed findings and errors block until remediated or explicitly reviewed under this
contract. Enrollment is limited to demonstrated false positives or constrained audits, existing
unchanged legacy risks, and the recorded parser gaps. It cannot convert a newly introduced defect
into inherited debt.
Moves and copies do not inherit entries. Each addition, rebinding, or removal requires independent
adversarial review and exact-revision hosted evidence. Source hashes are review aids, not protection
from an authorized writer changing both policy and code; the existing branch trust model still applies.

Emit separate counts for total findings, accepted false positives, accepted audits, deferred legacy
risks, accepted parser gaps, unmatched records, and stale entries. Identify accepted entries by validated inventory
ID and the existing bounded location diagnostics. Never print `findings 0` when findings were accepted.
A passing result means no unmatched reported regression under this contract. It does not prove
absence of newly introduced vulnerabilities, zero risk, or complete parsing.

Thailand's disabled certificate verification remains an ingestion and deployment gate until a
reviewed verifier and Thailand compatibility evidence resolve it. Congbao's certificate-supplied
intermediate download needs destination and redirect restrictions before affected ingestion or
deployment. Its custom root and hostname verifier can explain a scanner false positive without
resolving that separate fetch risk. Keep both named gates in the plan, even if no scanner finding
directly identifies a risk. Transition CI success never clears these release gates.

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

A report with pinned version `1.180.0` and list-shaped findings, errors, and skipped rules may log at most 40
finding locations and 20 errors. Unmatched records and all execution or proof failures block the check.

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
