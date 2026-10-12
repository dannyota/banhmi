# Security CI implementation

The owner approved Gitleaks for secrets and Semgrep for SAST and SCA. Initial source is coded and its
independent review has no open findings. The initial diagnostic follow-up passed independent review.
The Semgrep metadata follow-up passed hosted hygiene and reports bounded finding locations.
Gitleaks and dependency scans pass on the revisions recorded below. The owner requested CI rules
that support the approved restructure. Task S4 defines the unimplemented transition gate; SAST
still blocks until its inventory, independent review, and hosted controls are complete.
This plan delivers the [security CI spec](../specs/security-ci.md)
as one bounded task within [Vietnam M0.3](vietnam.md). Other M0.3 work remains separate.

## Scope and ownership

Preserve the existing five reusable checks, both aggregate gates, all triggers, generation
artifacts, country foundation work, and build behavior. No obsolete job or test has been identified.
Keep current compatibility tests required by the approved layout. Task S3 permits only the bounded dependency,
connection-limit, and migration-identifier fixes below in response to hosted findings. Other runtime
changes, deployment, production admission, branch-rule changes, and a broad CI audit remain outside
this task.

| Owner | Paths and responsibility |
| --- | --- |
| Designer | This plan and `docs/specs/security-ci.md`; contract and review fixes only |
| Implementer | `.github/workflows/ci.yml`, `.github/workflows/reusable-checks.yml`; explicit token mapping, three jobs, aggregate wiring |
| Implementer | `tools/ci/security_scan.py`, `tools/ci/test_security_scan.py`; pinned invocation, private output, narrow proof validation and tests |
| Implementer after design review | `tools/ci/semgrep-legacy.json`; exact reviewed transition inventory, source bindings, dispositions, and evidence |
| Implementer | `tools/ci/required-jobs.txt`, `tools/ci/check_workflow_contracts.py`, `tools/ci/test_workflow_contracts.py`; job and trust contracts |
| Primary | `docs/README.md`, `docs/plans/vietnam.md`, `instructions/verification.md`; indexes, status, hosted check inventory |
| Independent reviewer | Read-only design, plan, code, credential-boundary review and fix confirmation |
| Primary | Hosted evidence, setup coordination, Git operations and final acceptance |

Keep scanner pins and narrow helper logic in one module. Generate the fixed Gitleaks default-rule
config and the exact [checksum exception inventory](../specs/security-ci.md#historical-public-checksum-exception)
in a private temporary ignore file. Do not build a general scan framework.
No owner edits another owner's files without a handoff. Design approval does not authorize a worker
to push, deploy, change Semgrep policies, or retrieve credentials.

## Task S1: make the three scans required

1. Add hosted tests for the trust table, aggregate inventory, scanner selection, and failure contract.
   Tests use fake process results and small redacted reports based on the pinned public schema.
   Keep synthetic token-like strings out of tracked fixtures; create them only in temporary test data.
2. Implement the narrow scanner helper. Download and verify the pinned Gitleaks archive before use.
   Invoke scanners without a shell, capture output privately, validate proof, and emit fixed summaries.
   Pass metadata as data, not interpolated shell code. Clean temporary files on every exit path.
3. Add `gitleaks`, `semgrep-sast`, and `semgrep-sca` as independent reusable jobs with read permissions,
   existing pinned checkout, and bounded timeouts. Gitleaks fetches full history.
   Semgrep jobs validate event eligibility and token presence before scanner startup.
4. Map only the named secret from the caller using the spec's trusted-event condition.
   Declare that reusable secret optional so missing credentials produce explicit failed jobs.
   Limit token environment scope to Semgrep steps. Do not inherit secrets or use job-level skips.
5. Extend both gate contracts and required-job inventory without weakening existing checks.
   Primary updates the documentation index, hosted check matrix, and the obsolete M0.3 statement
   that no secret scanner has been selected. Remaining M0.3 gates stay unimplemented.
6. Complete independent credential-boundary review before hosted credential use.
   On an owner-authorized push, inspect the exact revision's hosted checks and scan summaries.
   Resolve failures through normal implementation and independent fix confirmation.

## Task S2: exempt the four proved public checksums

This bounded follow-up passed independent design and adversarial code review, a real repository
scan, and hosted negative controls. It changes only the Gitleaks
exception policy. Semgrep contracts, scanner pins, workflow trust, and required gates stay in force.
The [diagnostic hosted run](https://github.com/dannyota/banhmi/actions/runs/38218949484) reported
the four Gitleaks locations in the spec. Independent private inspection proved their public checksum
provenance without exposing matched values. The separate negative controls below prove detection
with the fixed inventory and exact fingerprint matching.

1. Complete independent read-only design review of the exact inventory and pinned fingerprint
   semantics before implementation. Preserve the public provenance and immutable locations in the spec.
2. Update only Gitleaks behavior in `tools/ci/security_scan.py` and its coverage in
   `tools/ci/test_security_scan.py`. Generate exactly four fixed commit fingerprints in the private
   ignore file. Keep explicit default-rule configuration and all existing failure and output handling.
   Do not add configurable ignores or report-based filtering.
3. Replace the empty-ignore assertion with an independent exact-inventory assertion. Check every
   commit, path, rule, and start line, uniqueness, and the prohibition on commit-free fingerprints.
   Show that repository and environment ignore settings cannot change the generated inventory.
   Keep tests for unknown findings, scanner errors, and private output.
4. Extend hosted real-Gitleaks negative coverage using temporary fake secrets. With the production
   four-entry inventory active, add and later remove a fake secret at an excepted path and line in a
   new commit. It must still fail the history scan. In an isolated scanner-contract fixture, prove
   an exact commit fingerprint suppresses only its finding and that changing each tuple field
   restores detection. Fixture fingerprints never enter the production inventory or wrapper inputs.
5. Obtain independent adversarial code review before the hosted scan. Inspect the exact revision's
   real repository scan and negative-test results. The four reviewed findings must be absent and all
   other findings must fail. A zero finding count alone does not prove detection remains active.
   Record the tested revision, run URL, scanner pin, safe counts, and independent fix confirmation.

## Task S3: remediate bounded hosted findings

The bounded fixes and dependency artifacts are coded and independently reviewed. Clean generation,
Go checks, browser and auth integration, and SCA passed on the integrated revision below. The pure
connection-limit and migration-name tests ran in that hosted short suite. Do not run dependency
tools locally. Live database creation, cleanup, and deployed behavior remain unverified.

| Finding | Bounded change | Evidence |
| --- | --- | --- |
| JOSE v3 parsing exhaustion and JWE panic | `github.com/go-jose/go-jose/v3` to `v3.0.5` | Upstream [parsing](https://github.com/go-jose/go-jose/security/advisories/GHSA-c6gw-w398-hv78) and [JWE](https://github.com/go-jose/go-jose/security/advisories/GHSA-78h2-9frx-2jm8) advisories |
| WebSocket mask predictability | `github.com/gorilla/websocket` to `v1.5.4-0.20240701034025-d67f41855da4` | The [upstream fix](https://github.com/gorilla/websocket/commit/d67f41855da42d7bccd9ef050c49f7e54e783b95) uses cryptographic randomness; tag `v1.5.3` still uses `math/rand` despite the advisory range |
| OpenTelemetry endpoint disclosure and response allocation | Align root, metric, trace, SDK, and affected trace/Zipkin exporters at `v1.45.0` | Upstream [endpoint](https://github.com/open-telemetry/opentelemetry-go/security/advisories/GHSA-8wmf-6v46-5gfg) and [response](https://github.com/open-telemetry/opentelemetry-go/security/advisories/GHSA-w8rr-5gcm-pp58) advisories |
| Connection-limit integer overflow | Parse `BANHMI_DATABASE_MAX_CONNS` at 32-bit width before converting; preserve valid positive values and fallback behavior | Boundary tests cover maximum, overflow, invalid, and nonpositive values without database access |
| Migration-tool database identifiers | Bound the temporary name before connecting and quote configuration-derived CREATE/DROP identifiers | Independent mutation-path review and hosted pure identifier tests are required; do not execute production DDL |

GitHub auth generation runs `go mod tidy` and uploads `go.mod` and `go.sum` before rejecting drift.
Import only reviewed dependency output from the exact candidate run. Expected companion changes
include OpenTelemetry SDK/metric, OTLP proto, logging, backoff, gateway, and generated RPC modules.
Do not accept unexplained graph changes. Require a clean subsequent generation run, Go checks,
auth and browser integration, and a zero-finding SCA scan. The temporary SCA negative input uses
upstream-proven vulnerable JOSE v3.0.3 without executing its code or merging the fixture.

## SAST dispositions and release gates

Read-only triage of [run 38220631794](https://github.com/dannyota/banhmi/actions/runs/38220631794)
at `f479adb8e4382413a59cb60276fa8f3f2d401a33` classified 26 findings and three parser errors.
The current [diagnostic run](https://github.com/dannyota/banhmi/actions/runs/38226210186) at
`503a938cd473a8fa12d99b790aa6217daa917aa0` reports 25 findings, three `PartialParsing` errors,
and zero skipped rules. The fixed connection-limit finding is absent. These safe diagnostics contain
line ranges but not every column, offset, parser location, or rule hash needed for enrollment.
No inventory entry is approved merely because it appears in this count.

| Group | Current evidence and required work |
| --- | --- |
| Thailand certificate verification | `pkg/ingest/ocs/client.go:59` is a genuine `legacy-risk`: both listing and legal-text calls lack certificate verification. A reviewed verifier and Thailand compatibility evidence remain gates for affected ingestion and deployment. Existing deployed services stay unchanged; source-only acceptance is not risk clearance. |
| Vietnam certificate lookup | The Congbao custom verifier checks roots and hostname, explaining the insecure-flag false positive. Its certificate-supplied intermediate URL fetch separately lacks destination and redirect restrictions. A reviewed hardening slice remains required before affected ingestion or deployment, regardless of the scanner finding's disposition. |
| Bounded code defects | Task S3 owns the connection-limit overflow and configured migration database identifiers. Correctly quoted dynamic DDL may still match the broad audit rule. |
| Audit findings with constrained inputs | Triage found fixed redirect destinations, explicit response media types, escaped or static HTML, fixed HTTPS URLs, configured shell-free executables, a canonical link, a value-type partial result, and counters populated by the outer loop. These findings need reviewed disposition; rewriting safe APIs to evade patterns is not remediation. |
| TLS minimum declarations | Existing Go and Chrome-preset clients require TLS 1.2 or newer. A TLS 1.3 autofix could break sources. The Congbao insecure-flag match alone does not account for its custom root and hostname verifier. |
| Parser coverage | `deploy/aws/run-embed.sh`, `deploy/aws/run-pipeline.sh`, and `pkg/ingest/bpk/testdata/detail_status_live_20260727.html` produce partial parsing. Record exact `coverage-gap` entries, not general error exceptions. Preserve captured evidence and deployment behavior; unchanged parsing gaps do not prove complete scanning. |

Current authenticated Vietnam redirect and static-response findings require code-specific false-positive
review; they are not all legacy code. The CI helper's fixed download and checksum also needs its own
reviewed entry after the helper is finalized. Correctly quoted migration DDL remains an audit match,
not justification for reverting the Task S3 fix or its valid regression tests.

## Task S4: enforce the reviewed transition inventory

This source-only policy amendment follows the owner's restructure request. It replaces the earlier
universal SAST finding/error rejection only as specified in the
[transition contract](../specs/security-ci.md#reviewed-transition-inventory). It keeps all eight required
jobs, workflow triggers, token trust, scanner pins, Gitleaks history policy, and strict SCA unchanged.
Do not change runtime code, dependencies, deployed services, cloud resources, or release workflows.

1. Complete independent design review. Primary reconciles `AGENTS.md`, verification instructions,
   and other owning records where needed before dependent implementation. No valid regression test
   is obsolete merely because a scanner audit rule still reports its now-constrained operation.
2. Use one reviewed, temporary hosted metadata capture against the exact source revision. Run the
   pinned scanner with unchanged product and output boundaries. Privately collect full locations,
   parser code/level/rule, source hashes and configured-rule hashes for findings and named parser
   rules. A parser rule hash is null if and only if its rule ID is absent. Publish only validated metadata
   and fixed proof fields, never raw reports, snippets, error messages, credentials, or rule bodies.
   Preserve every parser location and multiplicity. Current abbreviated diagnostics cannot seed
   the exact inventory. Unexpected shapes require a reviewed schema decision, not lossy conversion.
3. Review each disposition and enroll only exact findings and parser records with source evidence.
   Distinguish false positives, fixed and trusted-operator audits, the Thailand legacy risk, and the
   three parsing gaps. Bind supporting guard/configuration/asset files where the disposition depends
   on more than the matched file. Keep Congbao's separate intermediate-fetch risk in the release-gate
   table even though its TLS finding is a false
   positive. Record public reviewed-revision and run links with each entry. Do not bulk accept a
   count, directory, country, rule, or all current results. Independent review approves the initial
   manifest before the transition gate can pass.
4. In `tools/ci/security_scan.py`, validate scanner proof before matching the fixed JSON inventory.
   Verify actual mounted source against regular Git blobs and complete-file SHA-256. Match entries
   one to one, reject stale or duplicate entries, and retain strict nonzero exit handling. Keep raw
   output private and emit truthful accepted/unmatched counts. Do not weaken token or container
   isolation, add a diff scan, or mutate source to avoid findings.
5. Finalize helper changes before binding its finding in `tools/ci/semgrep-legacy.json`. The manifest
   is outside the helper so that updating the hash does not change the hashed file. Re-capture the
   final helper's location and hash through the same hosted path, then independently review that
   rebinding. A prior revision's helper hash cannot authorize modified scanner code.
6. Replace only test assertions superseded by this explicit SAST policy. Keep tests of strict SCA,
   Gitleaks, unknown findings/errors, process failures, output privacy, and all ordinary behavior.
   Add the negative controls below. Review the complete implementation adversarially before hosted
   credential use, then obtain independent fix confirmation against the final exact-revision run.

Required hosted controls include:

- Exact reviewed records pass with separate accepted counts. An empty inventory passes only an
  otherwise valid scan with no findings or errors. Accepted findings never produce a zero-findings log.
- New path, changed file or supporting-source bytes outside the finding, line/column/offset drift,
  rule-ID/body drift, changed parser code/level, additional parser location, duplicate occurrence,
  and unknown error fail. Parser entries also reject a changed configured rule body, a missing or
  duplicate configured rule ID, and mismatched null rule-ID/hash pairs.
- Missing expected finding, deleted/renamed source, stale source hash, duplicate inventory key/ID,
  symlink, untracked file, changed working bytes with unchanged HEAD, and malformed inventory fail.
- Known entries never mask wrong version, missing product or Go rules, scan baseline, suppression,
  skipped rules, invalid JSON, lost source proof, nonzero exit, or SCA failure.
- Real pinned SAST detects a new nonsecret finding while an unchanged reviewed finding is accepted.
  A second hosted case changes bytes in the reviewed finding's file and proves rejection. Temporary
  parser fixtures prove an exact gap can match and an extra location or changed source cannot pass.
  Fixtures remain disposable; do not execute vulnerable code or merge unsafe runtime fixtures.
- Existing real Gitleaks removed-secret and SCA vulnerable-dependency controls still fail as intended.
  Canary values in raw output and unknown metadata never enter logs or metadata artifacts.

Record safe counts, scanner pin, exact revision, and hosted run URLs. Capture and mocked tests alone
do not prove the transition works. Final required checks and independent review remain merge gates.
Transition acceptance makes no new corpus, deployment, country compatibility, or release-safety claim.

## Hosted evidence

GitHub runs every test and scanner. Local work is limited to reading, editing, and static inspection.
Existing hygiene discovers `test_*.py` under `tools/ci`; keep these tests in that hosted path.

| Contract | Required evidence |
| --- | --- |
| Aggregation | Every existing job remains required; each new failure, skip, cancellation, or missing result blocks both aggregates |
| Trust | Same-repository PR, push, and dispatch receive only the named token; fork, Dependabot, other event, and maintainer rerun of a denied PR remain denied |
| Credential scope | Static workflow tests reject `secrets: inherit`, write permissions, `pull_request_target`, token exposure outside Semgrep steps, and input-based trust overrides |
| Missing token | Empty token fails both Semgrep jobs with a fixed setup message before scanner execution |
| Products | Exact SAST/SCA flags, no Secrets, no local builds, no Pro download, no baseline or GitHub auto-diff environment |
| Tool integrity | Corrupt archive, download failure, wrong CLI version, process failure, timeout, and malformed JSON fail closed |
| Scan proof | Missing product, empty rules, suppression, merge base, unreviewed errors, code without applicable rules, and absent or unresolved Go subproject cannot pass |
| Findings | SCA and unmatched SAST findings block; only exact independently reviewed SAST entries pass with visible accepted counts; monitor status grants no exception |
| History | A disposable hosted Git repository with a fake secret added and later removed still fails the real Gitleaks history scan |
| Checksum exception | Exactly four fixed commit fingerprints; real pinned-scanner proof of exact matching and near-miss rejection; a removed fake secret at an excepted path and line in a new commit still fails with the production inventory active |
| Redaction | Canary values in mocked stdout, stderr, findings, malformed responses, unknown rules, and unproved paths never appear in helper output or artifacts |
| Diagnostics | Rejections show fixed Semgrep counts; Gitleaks locations require known commits, paths in those commits, bounded lines, and fixed rule names; Semgrep metadata requires current-tree paths, bounded lines, configured rule IDs, and fixed error types; unsafe metadata is withheld and unmatched records still fail |
| Isolation | No package-manager/build invocation, source mutation, Docker socket, or GitHub token enters the scanner container |
| Live integration | Both jobs prove requested product and rules; SAST reports Go source and SCA reports positive root resolution; all current CI checks still pass |

Synthetic process/report tests prove wrapper behavior, not live Semgrep entitlement or coverage.
Before accepting the gate, use a temporary hosted scan input with a nonsecret SAST finding and a
known vulnerable Go dependency to prove the authenticated products detect and block findings.
Do not merge vulnerable fixtures, upload raw reports, or run the fixture's code.
Record the exact scanner pin, tested revision, hosted run URL, requested product, and safe counts.
Report static dependency analysis accurately; the OSS engine does not prove full reachability.
The public subproject report cannot prove the absence of partial dependency parser errors.
Keep CLI errors unsuppressed. Only exact reviewed SAST parsing gaps may pass; SCA and every other
error remain strict. Do not add instrumentation or private API polling.

## Setup and completion gates

Independent design and credential-boundary reviews are complete. The reviewer confirmed supported
CLI flags, Go-specific SCA rules, safe diagnostics, and the exact checksum exception. No local tests
or scans ran. These hosted results prove only the listed revisions and inputs:

| Evidence | Tested revision and result |
| --- | --- |
| [Repository checks, run 38221088866](https://github.com/dannyota/banhmi/actions/runs/38221088866) | Head `90f6c699f7d5f4bf6c8884d7aea7c2bb0006131f`. Gitleaks and all five ordinary jobs passed. SAST and SCA failed, so both aggregates failed. |
| [Gitleaks negative controls, run 38221369289](https://github.com/dannyota/banhmi/actions/runs/38221369289) | Temporary probe head `41aad9e97323c968cf297793b25bc218dfc29a5e`, Gitleaks `8.30.1`. The production inventory still detected one removed fake secret at an excepted path and line in a new commit, with zero final tracked files. The isolated fingerprint fixture had two findings, one after an exact ignore, and two for each changed commit, path, rule, or line. |
| [Dependency seed, run 38223042126](https://github.com/dannyota/banhmi/actions/runs/38223042126) | Head `3dcdf58d9158456f0b1d23ff6c48f70e143bd506`, PR merge `62118f081097c873ec4cfe509ddd2b1c08c19f46`. Hygiene, Gitleaks, and SCA passed. Semgrep `1.180.0` reported one scanned dependency file, 4,909 applicable rules, and zero findings. SAST reported 25 findings and three parsing errors. |
| Dependency generation | The same seed run generated artifact `auth-generation-62118f081097c873ec4cfe509ddd2b1c08c19f46`. Generation rejected expected `go.mod` and `go.sum` drift; formatting output was empty. Independent review confirmed expected dependency changes and unchanged auth output. Only `go.mod` and `go.sum` are imported. Downstream Go, browser, and auth integration jobs were skipped and do not count as passes. |
| [Integrated fixes, run 38223740194](https://github.com/dannyota/banhmi/actions/runs/38223740194) | Head `133df6c6a3c4b992baa6aed3faed0a8107ba6142`, PR merge `b780b3d7ae038da4dbcba2ed4d982f262afbafbd`. All five ordinary jobs, Gitleaks, and SCA passed. The short suite ran `pkg/base/db` and `tools/migragen`. Independent fix confirmation passed. SAST retained 25 findings and three parser errors, so CI failed. |
| [Semgrep negative controls, run 38225686934](https://github.com/dannyota/banhmi/actions/runs/38225686934) | Temporary probe head `7bc00c87fdd6db79d86ef68012a48b2f91a51f38`, Semgrep `1.180.0` at the spec's image digest. SAST fixture `bf9056f45dbedb990501d78379393bdcbfb14744` produced one intended TLS finding, one source file, and 154 applicable rules. SCA fixture `50983eb71b9bb87cfc690757eef483976bfaf822` produced three intended JOSE findings, two scanned files, and 4,909 applicable rules. Both actual reports were rejected. Test copies removing only expected results passed full product, version, source, and root-resolution proof with errors and skipped rules unchanged. Gitleaks controls also passed in this run. |

The temporary probe branch must not merge. All three scanner negative controls passed.
The source is not accepted, merged, or deployed. Passing bounded-fix checks do not waive
the remaining SAST findings, parser gaps, or aggregate CI requirement.

The owner confirmed `SEMGREP_APP_TOKEN` uses Service (CI) Agent (CI) scope and that Code and Supply
Chain are enabled for this repository. Hosted SAST and SCA detection and root resolution prove requested
product and applicable-rule access. Do not read the token
value into logs or conversation. A missing setup item leaves checks red;
there is no tokenless fallback or temporary success exception.

The independent reviewer checks both documents before implementation, then reviews the patch for
token release, untrusted events, scanner output leaks, executable pins, product selection, and gate
bypass. The reviewer never implements the slice. Primary inspects the diff and evidence before
accepting the report. Workers do not run local tests, scanners, builds, installs, or Git writes.

Completion requires the exact revision's hosted required checks, meaningful negative tests, live
product proof, and independent review. Until then, describe the work as designed or coded, not
validated. Findings in existing history or code are reported for remediation; do not hide them
with broader ignores or rewrite history within this slice. The four public-checksum exceptions retain
their passed independent review and hosted detection controls. Task S4's SAST inventory, review,
negative controls, and final CI remain gates. Its accepted risks and parsing gaps stay visible;
the named ingestion and deployment gates remain open after source-only acceptance.
