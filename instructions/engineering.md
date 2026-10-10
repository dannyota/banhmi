# Engineering

## Architecture

- Treat [`docs/ARCHITECTURE.md`](../docs/ARCHITECTURE.md) as the current system. Treat explicitly
  labeled redesign documents as proposals until their implementation is verified.
- Pipeline layers exchange state through PostgreSQL. `pkg/base/` is the shared exception and must not
  contain source-specific or layer-specific behavior.
- Keep each source self-contained under `pkg/ingest/` and wire sources in `pkg/app`.
- Define extraction, embedding, and retrieval interfaces at their consumers. Select implementations
  through configuration and jurisdiction descriptors.
- Keep evidence and citation logic in shared `pkg/rag` and `pkg/mcp` code so stdio and Streamable HTTP
  expose the same behavior.
- Keep dependency injection in the `pkg/app` composition root. Business logic uses plain constructors.
- Check every jurisdiction affected by shared code. Do not fork the codebase by jurisdiction.

## Data and generated code

- sqlc is the data layer. Edit schema and query sources under `sql/`. Do not edit generated code under
  `pkg/store/`.
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
