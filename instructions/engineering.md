# Engineering

## Architecture

- Treat [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) as the current system. Treat explicitly
  labeled redesign documents as proposals until their implementation is verified.
- Legacy packages named in [product boundaries](../AGENTS.md#product-and-evidence-boundaries) are
  frozen. They take owner-approved maintenance fixes only. New Vietnam code goes to the homes in the
  [approved layout](../docs/design/repository-layout.md) and never imports legacy mutation code.
- Pipeline layers exchange state through PostgreSQL. `pkg/base/` holds shared utilities. The
  `pkg/base/sourceurl` exception holds pure source URL formatting used by crawlers and serving. It
  must not import crawlers, extraction, pipeline code, or database stores.
- Keep each source self-contained under `pkg/ingest/` and wire sources in `pkg/app`.
- Keep Go HTML and DOCX reader mechanics in `internal/corpus/conversion`. Legacy `pkg/extract`
  functions preserve their public APIs and supply existing country policy. Conversion must not import
  legacy extraction, country modules, source clients, or database stores. Block-model conversion and
  native adapters remain separate work under the [conversion plan](../docs/plans/document-conversion.md).
- Keep pure rune estimation and text splitting in `internal/corpus/chunking` with standard-library
  dependencies only. Pipeline wrappers retain existing callers and the 512-token policy. Keep citation,
  tree, prefix, and persistence behavior in the pipeline until its owning migration slice.
- Define extraction, embedding, and retrieval interfaces at their consumers. Select implementations
  through configuration and jurisdiction descriptors.
- Keep evidence operations in `internal/evidence/service`, neutral contracts in
  `internal/evidence/api`, and retrieval in `pkg/rag`. MCP and website transports share these
  operations. Preserve stdio and Streamable HTTP behavior during the source refactor.
- Use `internal/app/serve` for serving composition. Keep corpus composition in `pkg/app` until its
  staged migration. Serving must not import corpus workers or source crawlers. Business logic uses
  plain constructors.
- Check every jurisdiction affected by shared code. Do not fork the codebase by jurisdiction.

## Data and generated code

- sqlc is the data layer. Handwritten Goose migrations are the only SQL schema source. Each sqlc entry
  reads the migration directory that its migrator embeds. Generation configuration stays in
  `sqlc.yaml`. Generated Go changes only through the hosted generation job: the primary reviews and
  imports its artifact, and the job fails on drift. Never hand-edit generated Go.
- Three kinds of migration chain:
  - Legacy corpus chains under `deploy/migrations/` serve the five maintained countries and the
    current Vietnam service. They are frozen. The hosted history check fails on any change under
    those directories. Their queries under `sql/{bronze,silver,gold,ingest,config}` and generated
    stores under `pkg/store/{bronze,silver,gold,ingest,config}` change only when regeneration from
    the frozen chains proves zero drift. The undeployed `*_corpus_redesign.sql` files stay in place
    and retire with the chains.
  - New chains for the Vietnam corpus writer and the serving release databases live in their own
    directories with their own Goose version tables, as the
    [corpus database spec](../docs/specs/corpus-database.md) names them. Their queries and generated
    stores live beside the code that uses them under `internal/`. A new chain is edited in place
    until a tagged release or a deployed database contains it. After that it is append-only.
  - The auth chain under `deploy/authmigrations/` is append-only. Preserve every existing path and
    byte. Its queries under `sql/auth/` and generated store under `pkg/store/auth` stay where they
    are and change through hosted generation.
- Hosted generation regenerates every sqlc entry, uploads the generated review artifact, and
  rejects drift before replaying chains or running tests on disposable PostgreSQL. A final check
  rejects repository changes after tests. [Verification](verification.md) owns the full order and
  check scopes. After a chain becomes append-only, the job also applies it to a
  populated database built from the previous release. There is no schema differ, migration
  generator, or checksum file.
- Use one primary-key column per table. Put composite business identity in `UNIQUE` constraints for
  idempotent upserts. Use no foreign keys across schemas.
- Put queryable scalar data in columns, queryable repeated data in child tables, and non-queryable data
  in JSONB. Preserve content hashes and provenance.
- Treat source fields as nullable and variant until multiple real samples prove a stricter contract.
- Store tunable scope terms, issuer codes, discovery terms, normalization dictionaries, and relation
  policy in configuration tables and seed files. Do not hardcode policy lists or document IDs in Go.
- Do not commit binaries or generated runtime artifacts.

## Go

- Follow Google Go style. Use MixedCaps and omit `Get` from getters.
- Group imports as standard library, external packages, then internal packages.
- Return errors from library code. Do not call `panic`, `log.Fatal`, or `os.Exit` there.
- Wrap errors with `%w` and a concise operation, such as `fmt.Errorf("fetch document: %w", err)`.
- Do not both log and return the same error. Use `log/slog` with structured fields.
- Keep linear logic inline. Extract helpers when reused or independently testable.
- Do not add `//nolint` without explicit approval.
