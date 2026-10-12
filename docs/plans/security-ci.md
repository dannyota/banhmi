# Security CI implementation

The owner approved Gitleaks for secrets and Semgrep for SAST and SCA. Initial source is coded and its
independent review has no open findings. The initial diagnostic follow-up passed independent review.
The Semgrep metadata follow-up is coded and independently reviewed; hosted evidence remains pending.
This plan delivers the [security CI spec](../specs/security-ci.md)
as one bounded task within [Vietnam M0.3](vietnam.md). Other M0.3 work remains separate.

## Scope and ownership

Preserve the existing five reusable checks, both aggregate gates, all triggers, generation
artifacts, country foundation work, and build behavior. Task S3 permits only the bounded dependency,
connection-limit, and migration-identifier fixes below in response to hosted findings. Other runtime
changes, deployment, production admission, branch-rule changes, and a broad CI audit remain outside
this task.

| Owner | Paths and responsibility |
| --- | --- |
| Designer | This plan and `docs/specs/security-ci.md`; contract and review fixes only |
| Implementer | `.github/workflows/ci.yml`, `.github/workflows/reusable-checks.yml`; explicit token mapping, three jobs, aggregate wiring |
| Implementer | `tools/ci/security_scan.py`, `tools/ci/test_security_scan.py`; pinned invocation, private output, narrow proof validation and tests |
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

This bounded follow-up is coded and passed independent design and adversarial code review.
Hosted verification remains pending. It changes only the Gitleaks
exception policy. Semgrep contracts, scanner pins, workflow trust, and required gates stay in force.
The [diagnostic hosted run](https://github.com/dannyota/banhmi/actions/runs/38218949484) reported
the four Gitleaks locations in the spec. Independent private inspection proved their public checksum
provenance without exposing matched values. That evidence establishes the false positives; it does
not establish that the proposed exception preserves detection.

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

The dependency seed is coded and independently reviewed. Hosted dependency resolution, generated
checksum review, and clean CI remain pending. Do not run dependency tools locally.

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

## Remaining SAST acceptance blockers

Read-only triage of [run 38220631794](https://github.com/dannyota/banhmi/actions/runs/38220631794)
at `f479adb8e4382413a59cb60276fa8f3f2d401a33` classified 26 findings and three parser errors.
Triage does not waive the all-findings gate. No Semgrep ignores or policy exclusions were added.

| Group | Current evidence and required work |
| --- | --- |
| Thailand certificate verification | `pkg/ingest/ocs/client.go` disables certificate verification for both listing and legal-text calls. Repair needs a trusted chain or reviewed verifier plus Thailand compatibility evidence. Removing the flag without that evidence can stop ingestion. Existing services remain unchanged. |
| Vietnam certificate lookup | The Congbao custom verifier checks roots and hostname, but its certificate-supplied intermediate-certificate URL fetch lacks destination and redirect restrictions. Egress hardening needs a separate reviewed slice. |
| Bounded code defects | Task S3 owns the connection-limit overflow and configured migration database identifiers. Correctly quoted dynamic DDL may still match the broad audit rule. |
| Audit findings with constrained inputs | Triage found fixed redirect destinations, explicit response media types, escaped or static HTML, fixed HTTPS URLs, configured shell-free executables, a canonical link, a value-type partial result, and counters populated by the outer loop. These findings need reviewed disposition; rewriting safe APIs to evade patterns is not remediation. |
| TLS minimum declarations | Existing Go and Chrome-preset clients require TLS 1.2 or newer. A TLS 1.3 autofix could break sources. The Congbao insecure-flag match alone does not account for its custom root and hostname verifier. |
| Parser coverage | Two AWS launcher heredocs and the truncated BPK HTML fixture produce partial parsing. Hosted syntax and parser reproduction plus fixture assertions are required before any equivalent source repair. Preserve captured evidence and deployment behavior. |

The current all-findings policy keeps these audit findings and parser gaps blocking. Changing that
policy requires an explicit, reviewed contract change; successful dependency or Gitleaks checks
cannot substitute for SAST acceptance or country evidence.

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
| Scan proof | Missing product, empty rules, suppression, merge base, public errors, code without applicable rules, and absent or unresolved Go subproject cannot pass |
| Findings | Any returned finding blocks; monitor status does not turn a finding into success |
| History | A disposable hosted Git repository with a fake secret added and later removed still fails the real Gitleaks history scan |
| Checksum exception | Exactly four fixed commit fingerprints; real pinned-scanner proof of exact matching and near-miss rejection; a removed fake secret at an excepted path and line in a new commit still fails with the production inventory active |
| Redaction | Canary values in mocked stdout, stderr, findings, malformed responses, unknown rules, and unproved paths never appear in helper output or artifacts |
| Diagnostics | Rejections show fixed Semgrep counts; Gitleaks locations require known commits, paths in those commits, bounded lines, and fixed rule names; Semgrep metadata requires current-tree paths, bounded lines, configured rule IDs, and fixed error types; unsafe metadata is withheld and findings still fail |
| Isolation | No package-manager/build invocation, source mutation, Docker socket, or GitHub token enters the scanner container |
| Live integration | Both jobs prove requested product and rules; SAST reports Go source and SCA reports positive root resolution; all current CI checks still pass |

Synthetic process/report tests prove wrapper behavior, not live Semgrep entitlement or coverage.
Before accepting the gate, use a temporary hosted scan input with a nonsecret SAST finding and a
known vulnerable Go dependency to prove the authenticated products detect and block findings.
Do not merge vulnerable fixtures, upload raw reports, or run the fixture's code.
Record the exact scanner pin, tested revision, hosted run URL, requested product, and safe counts.
Report static dependency analysis accurately; the OSS engine does not prove full reachability.
The public subproject report cannot prove the absence of partial dependency parser errors.
Keep CLI errors unsuppressed and reject errors exposed in the report. Do not add instrumentation
or private API polling.

## Setup and completion gates

Independent design and credential-boundary review of the initial code are complete. The reviewer confirmed
supported CLI flags, Go-specific SCA rules, and separate Gitleaks finding and execution messages.
Initial static diff inspection found no whitespace errors. No local tests or scans ran.
The [initial hosted run](https://github.com/dannyota/banhmi/actions/runs/38217325467) used head
`2e0368b50e13418b32eb32ec66d019b02926f620` and scanned PR merge revision
`fc250401a227e4aa5e36425dedf91979ee809418`. Repository hygiene passed, but Gitleaks reported four
findings and both Semgrep jobs failed report validation. Successful scan evidence remains pending.
The diagnostic follow-up passed independent adversarial review. Task S2 records the checksum
finding evidence and the remaining exception gates.
The [diagnostic hosted run](https://github.com/dannyota/banhmi/actions/runs/38218949484) used head
`0f431ec03e97bd7a5635456aedf6b6390929599b`, PR merge
`694dc9f9b3166d08be18a911487ba4c2849e316d`. All five non-security jobs passed.
SAST returned 26 findings and three errors; SCA returned nine findings and no errors.
Both reported the pinned version and zero skipped rules. Counts cannot identify remediation
targets. The Semgrep metadata amendment passed independent design and adversarial code reviews.
Its exact-revision hosted run remains pending. Scanner findings and errors remain unresolved.
The auth generation job reported formatting drift in two Vietnam asset files. The reviewed hosted
formatting patch is imported; generated SQL, migration, and dependency artifacts were unchanged.
The source is not validated, merged, or deployed.

The owner confirmed `SEMGREP_APP_TOKEN` uses Service (CI) Agent (CI) scope and that Code and Supply
Chain are enabled for this repository. Live scans still need to prove the requested products and
applicable policies. Do not read the value into logs or conversation. A missing setup item leaves checks red;
there is no tokenless fallback or temporary success exception.

The independent reviewer checks both documents before implementation, then reviews the patch for
token release, untrusted events, scanner output leaks, executable pins, product selection, and gate
bypass. The reviewer never implements the slice. Primary inspects the diff and evidence before
accepting the report. Workers do not run local tests, scanners, builds, installs, or Git writes.

Completion requires the exact revision's hosted required checks, meaningful negative tests, live
product proof, and independent review. Until then, describe the work as designed or coded, not
validated. Findings in existing history or code are reported for remediation; do not hide them
with broader ignores or rewrite history within this slice. Only the spec's four proved public
checksum fingerprints are excepted. Their independent reviews passed; hosted proof remains a
completion gate.
