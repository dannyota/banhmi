# Development and verification

Use GitHub pull requests for development checks. Hosted checks and isolated cloud workers provide
the required build, database, corpus, model, and deployment environments.

Read [`AGENTS.md`](../AGENTS.md) before changing the repository. Detailed contributor rules live in:

- [AGENTS.md roles](../AGENTS.md#roles-and-dispatch) and [Git rules](../AGENTS.md#git) for delegation and integration.
- [`instructions/workflow.md`](../instructions/workflow.md) for briefs, ownership, and handoffs.
- [`instructions/verification.md`](../instructions/verification.md) for required evidence.
- [`instructions/engineering.md`](../instructions/engineering.md) for Go, SQL, and migration rules.
- [`instructions/corpus.md`](../instructions/corpus.md) for corpus integrity and evaluation rules.
- [`instructions/documentation.md`](../instructions/documentation.md) for public documentation.
- [`instructions/security.md`](../instructions/security.md) for credentials and external systems.

## Hosted checks

Use the [hosted check matrix](../instructions/verification.md#hosted-check-matrix) for current
checks, inputs, limits, and evidence gates.

## Change requirements

- Change SQL sources under `sql/`. Do not hand-edit generated code under `pkg/store/`.
- Regenerate migrations and sqlc output when their sources change. Review generated diffs before
  accepting them.
- Preserve citation bytes, validity, relations, source provenance, and coverage gaps unless the
  approved change explicitly changes one of those contracts.
- Treat corpus validation as separate evidence from a successful Go test.
- Record every skipped or unavailable check in the pull request.
- Keep secrets out of YAML, Git, logs, artifacts, and review comments.

## Isolated runtime work

Corpus processing, database integration, ONNX checks, and deployment verification run on isolated CI
or cloud workers with purpose-built inputs. A worker must use a disposable or explicitly assigned
database. It must not use a live service database for development checks.

The portable runtime commands and configuration contract remain documented in
[`DEPLOYMENT.md`](DEPLOYMENT.md). That reference covers migrations, seeding, pipeline stages, query
embedding, MCP serving, storage, and release order. Run those commands only within the scope of an
authorized hosted task and suitable isolated infrastructure.

Workflow evaluation has its own inputs and limits in
[`design/WORKFLOW-EVAL.md`](design/WORKFLOW-EVAL.md). Retrieval and workflow evaluations remain
manual hosted checks until CI has the required corpus and model inputs.
