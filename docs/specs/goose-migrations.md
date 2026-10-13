# Goose migrations as schema source

Status: implemented and independently reviewed. Required hosted checks validate migration-backed
generation and synthetic database replay with zero drift. Atlas and `tools/migragen` are removed;
Goose and sqlc remain.
The [delivery plan](../plans/goose-migrations.md) owns implementation and hosted evidence.

## Outcome and boundaries

Handwritten Goose SQL becomes the only executable schema source. sqlc reads the same migration
directories that the existing migrators embed. Existing migration filenames, bytes, versions,
directory order, and Goose version tables remain unchanged. There is no squash, reset, history
adoption, baseline replacement, deployment, production connection, or cloud change.

The pending corpus redesign becomes reviewed additive migration source. This does not wire its
adapters, start workers, enable a country module, promote evidence, or complete M1. Existing AWS
services and all six deployed databases remain unchanged. Runtime and corpus qualification still
follow their owning plans.

## Decisions

**Decision:** Author forward Goose migrations and generate only Go stores with sqlc.
**Why:** One executable schema removes the canonical-DDL, differ, migration, and checksum chain.
**Rejected:** Another schema differ, generated migration SQL, or a retained canonical authoring copy.
**Detail:** Each sqlc entry reads its own migration directory. Queries remain under `sql/` and
generated Go remains under `pkg/store/`. Existing type overrides and package boundaries stay.

**Decision:** Replace Atlas checksum verification with trusted Git history checks and embedded SQL.
**Why:** A digest stored beside SQL in the same executable cannot establish independent provenance.
Git comparisons protect reviewed history without maintaining a second checksum format.
**Rejected:** Reimplementing Atlas hashes, a new runtime manifest, or a mutable freeze marker.
**Detail:** Required hosted checks reject changes to prior SQL. Goose tracks applied versions;
embedding binds SQL to the built artifact. This does not detect an operator altering an already
applied database or a substituted executable. Those are separate operational integrity concerns.

**Decision:** Preserve pending corpus definitions as additive forward migrations in this change.
**Why:** The current queries and schema tests already depend on those definitions. Keeping a
second draft migration system would add another state and promotion mechanism.
**Rejected:** Discarding pending SQL, disabling schema tests, or enabling unfinished persistence.
**Detail:** Copy pending definitions faithfully, including functions and triggers. Hosted catalog
parity and populated upgrade checks must establish preservation before canonical files disappear
from the accepted change. Unexpected non-additive differences block acceptance and require an
explicit reviewed correction; a generator must not infer a destructive reconciliation.

## Migration and generation contract

Keep the existing 24 corpus SQL files, including the extension migration, and the existing auth
migration byte-for-byte. Corpus order remains `extensions`, `bronze`, `silver`, `gold`, `ingest`,
`config`. Keep `public.goose_db_version`, the five `public.goose_db_version_<layer>` tables, and
the auth migrator's `goose_db_version_auth` setting. Do not copy role/bootstrap changes from another
system. Auth capability roles, separate connections, and bootstrap guards remain intact.

Append these five files. Each contains the existing pending DDL for its layer, with Goose Up and
statement boundaries for function bodies. Do not add destructive Down SQL for immutable evidence.

| New path under `deploy/migrations/` | Pending objects identified by static inventory |
|---|---|
| `bronze/00002_corpus_redesign.sql` | 4 tables, 1 function, 4 triggers |
| `silver/00009_corpus_redesign.sql` | 7 tables, 1 function, 7 triggers |
| `gold/00003_corpus_redesign.sql` | 7 tables, 2 functions, 8 triggers |
| `ingest/00006_corpus_redesign.sql` | 13 tables, 4 functions, 10 triggers |
| `config/00008_corpus_redesign.sql` | 2 tables, 1 function, 2 triggers |

These counts compare names, not database semantics. Carry every associated index, constraint,
default, comment, and function body from the pending source. Preserve existing-object definitions
through replay. Do not change queries or store APIs merely to make generation pass.

Set the auth sqlc schema input to `deploy/authmigrations/*.sql`, excluding its bootstrap directory.
Set each corpus input to its layer's migration directory. Goose-aware sqlc parsing ignores Down
sections. Keep the current sqlc pin and confirm its handling of the actual SQL in hosted generation.
Do not introduce a SQL preprocessor if parsing fails; report the exact failure for a bounded fix.

Delete `sql/auth/schema.sql` and the five corpus `sql/<layer>/schema.sql` files once their complete
definitions are represented by migrations in the proposed tree. For the transition run, reconstruct
the previous canonical files from the trusted comparison commit into runner temporary storage and
apply them only to isolated comparison databases. Compare them with candidate replay, including
roles and memberships for auth, before accepting the deletion. Later runs use replay and behavioral
contracts without a permanent canonical copy. A run with no canonical files on its comparison base
records that transition comparison is not applicable; it still runs all regular checks.

Remove `tools/migragen/`, its Make target, Atlas setup from both workflows, every `atlas.sum`,
checksum parser and checksum-only test, and runtime checksum calls. Remove `cmd/migrate -check`
and its documentation because it only verifies those checksums. Both migrators otherwise retain
their current connection, ordering, and Goose application behavior. Embed only the SQL files.
There is no committed Atlas configuration to replace at the inspected baseline.

## Trusted migration history

The [hosted check matrix](../../instructions/verification.md#hosted-check-matrix) owns the current
history guard, chain replay, schema tests, generation artifact, and drift contract.
The [chain rules](../../instructions/engineering.md#data-and-generated-code) distinguish frozen
legacy chains from append-only auth and editable new chains. The guard compares final trusted-base
and candidate trees; it does not inspect intermediate commits.

## Hosted checks and adjacent simplification

Use the required `generation` job in `reusable-checks.yml` for every sqlc entry and all migration
chains. Its generated-tree artifact replaces separate auth and corpus outputs. The
[hosted check matrix](../../instructions/verification.md#hosted-check-matrix) defines step order,
disposable database inputs, isolated race and rollback tests, and drift rejection. The
[delivery plan](../plans/goose-migrations.md#evidence-and-remaining-gates) preserves exact transition
runs, artifact hashes, and schema comparison evidence.

## Negative evidence and acceptance

Current hosted checks and negative contracts follow the
[verification rules](../../instructions/verification.md#hosted-check-matrix). Auth PostgreSQL
retains bootstrap denial and privilege behavior tests. Real-corpus qualification remains separate.

Independent review covers the design and plan before implementation. Independent adversarial code
review covers migration SQL, trusted-base resolution, auth safeguards, workflow mutation paths,
and artifact import. Reviewers never implement their fixes. Acceptance requires exact-commit hosted
results and fix confirmation; local source inspection is not runtime evidence.

## Implementation ownership and handoffs

The paired plan may serialize these owners to respect separate worktrees and the four-job limit.
No worker commits, pushes, deploys, accesses production, or edits another owner's paths.

| Owner | Exclusive implementation paths and handoff |
|---|---|
| SQL and runtime | `sqlc.yaml`, six canonical schema deletions, five new migrations, seven checksum deletions, `deploy/{migrations,authmigrations}/embed*.go`, `cmd/{migrate,auth-migrate}/`, `tools/migragen/`, `Makefile`, `internal/corpus/schematest/`; hand off exact schema inputs and immutable-history diff |
| Hosted checks | `.github/workflows/{ci,reusable-checks}.yml`, migration/generation helpers and tests in `tools/ci/`, the unchanged security inventory; hand off required-job contracts, generated-tree artifacts, replay scope, and evidence scope |
| Primary integration | Artifact inspection/import into `pkg/store/`, generated dependency/formatting artifacts, documentation indexes, engineering/verification rules, architecture, auth/corpus runbooks and owning plans, `todo.md`, Git operations, and hosted evidence |

The primary resolves changed fact owners in the same integration change. Replace Atlas download
work with this removal contract while retaining unfinished M0 history, replay, role isolation,
artifact provenance, and broader CI gates. Do not mark M0 or corpus runtime qualification complete.
If hosted replay exposes a non-additive legacy discrepancy or sqlc incompatibility, report the
specific object or query and obtain a bounded reviewed correction before importing generated code.
