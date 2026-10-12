# Engineering

## Architecture

- Treat [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) as the current system. Treat explicitly
  labeled redesign documents as proposals until their implementation is verified.
- Pipeline layers exchange state through PostgreSQL. `pkg/base/` holds shared utilities. The
  `pkg/base/sourceurl` exception holds pure source URL formatting used by crawlers and serving. It
  must not import crawlers, extraction, pipeline code, or database stores.
- Keep each source self-contained under `pkg/ingest/` and wire sources in `pkg/app`.
- Keep Go HTML and DOCX reader mechanics in `internal/corpus/conversion`. Legacy `pkg/extract`
  functions preserve their public APIs and supply existing country policy. Conversion must not import
  legacy extraction, country modules, source clients, or database stores. Block-model conversion and
  native adapters remain separate work under the [conversion plan](../docs/plans/document-conversion.md).
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

- sqlc is the data layer. Edit schema and query sources under `sql/` and generation configuration in
  `sqlc.yaml`. Change sqlc output under `pkg/store/` only through hosted generation driven by
  `sqlc.yaml`, never by hand. Review generated artifacts before importing them.
- Use one primary-key column per table. Put composite business identity in `UNIQUE` constraints for
  idempotent upserts.
- Keep pipeline state in `ingest`, raw captures in `bronze`, normalized evidence in `silver`, retrieval
  data in `gold`, and operator policy in `config`.
- Put queryable scalar data in columns, queryable repeated data in child tables, and non-queryable data
  in JSONB. Preserve content hashes and provenance.
- Treat source fields as nullable and variant until multiple real samples prove a stricter contract.
- Store tunable scope terms, issuer codes, discovery terms, normalization dictionaries, and relation
  policy in `config` and seed files. Do not hardcode policy lists or document IDs in Go.
- Tagged releases exist, so migrations are append-only. Change the source schema, generate a new
  migration, and preserve existing migration files and checksums.
- Do not commit binaries or generated runtime artifacts.

## Go

- Follow Google Go style. Use MixedCaps and omit `Get` from getters.
- Group imports as standard library, external packages, then internal packages.
- Return errors from library code. Do not call `panic`, `log.Fatal`, or `os.Exit` there.
- Wrap errors with `%w` and a concise operation, such as `fmt.Errorf("fetch document: %w", err)`.
- Do not both log and return the same error. Use `log/slog` with structured fields.
- Keep linear logic inline. Extract helpers when reused or independently testable.
- Do not add `//nolint` without explicit approval.
