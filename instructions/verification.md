# Verification

## Hosted check matrix

[CI](../.github/workflows/ci.yml) runs on push, pull request, and manual dispatch. It calls
[reusable checks](../.github/workflows/reusable-checks.yml) with read-only permissions and an
always-reporting aggregate job. Actions and service images are pinned. Superseded CI runs cancel.
Preserve read-only permissions, pinned actions, superseded-run cancellation, and an always-reporting
aggregate result.

| Job | Checks and inputs | Limits |
|---|---|---|
| Repository hygiene | Python CI helper tests and portable maintained Markdown checks | Static repository rules, not runtime behavior |
| Auth source generation and drift | Auth sqlc entry, Go formatting and dependencies, Atlas checksum; canonical and migrated PostgreSQL 17 schemas, capability roles and memberships; allowlisted artifact and drift rejection | Disposable databases and synthetic credentials; no source write permission |
| Default Go build, vet, and short tests | `go build ./...`, `go vet ./...`, `go test -short ./...`; Go version from `go.mod`, CGO enabled | No live sources, services, database, corpus, ONNX, or evaluation inputs; short-mode guards skip tests |
| Website browser integration | Runner Chrome, pinned chromedp, `TestBrowser*` in `pkg/web`, fake auth and evidence over `httptest` | Synthetic browser interactions, not a deployed website or real Google |
| Auth PostgreSQL integration | Auth migrations, missing-role bootstrap guards, complete synthetic role bootstrap; identity, migration, cleanup, and OAuth package tests when present | Disposable PostgreSQL 17; distinct migration, runtime, and test URLs; no production connections |
| CI aggregate | Requires reusable checks to succeed | A skipped or failed dependency fails the aggregate |

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

- Keep credentials and production connections out of pull request jobs. Use disposable service
  databases and synthetic credentials. Real-corpus checks need isolated versioned snapshots.
- Add hosted inputs and runners before enabling source integration, pipeline execution, corpus or
  workflow evaluations, tagged ONNX builds, real Google/client checks, or release automation.
  The current check matrix does not prove those behaviors.
- Keep retrieval and workflow evaluation evidence separate. Check every affected jurisdiction.
- Do not claim hosted proof until GitHub runs the exact published commit. Missing, skipped, or
  unavailable required checks are gates, not passes. Review generated drift before dependent work.
- Apply [local execution and merge rules](../AGENTS.md#verification-and-review).
