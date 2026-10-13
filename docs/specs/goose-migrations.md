# Goose migrations as schema source

Status: independently reviewed for implementation. The owner approved removing Atlas and
`tools/migragen`, retaining Goose and sqlc, and simplifying the adjacent migration tooling.
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

Use `tools/ci/check_migration_history.py --base BASE --head HEAD` for auth, all five corpus layers,
and extensions. Pull request checks also pass `--check-pr-history` to inspect intervening commits.
It reads filenames and blob bytes from Git trees, never from a pre-generation working-tree
snapshot. No checksum generation, SQL rewriting, database access, or network lookup belongs in it.

- Resolve the comparison commit from trusted event metadata: the pull request base SHA, the push
  before SHA, or an explicitly fetched protected default-branch reference for manual execution.
  Checkout full history and bind the recorded candidate SHA to the actual tested tree.
- Fail closed on missing or malformed refs, missing history, missing migration roots, unexpected
  file types, duplicate numeric versions, and invalid migration filenames. Enforce each root's
  existing filename convention and require lexicographic filename order to equal numeric version
  order, including auth. sqlc and Goose must consume the same order.
- Every migration on the comparison base must retain its exact path and blob bytes. New versions
  must sort and compare numerically after the existing maximum in that directory. Check the file
  extension and root allowlist so renames or new unrecognized migration locations cannot escape.
- Compare the complete current trusted base directly against the tested candidate, even when
  histories diverge. Never substitute a merge base for this final comparison.
- Inspect every available PR commit for intermediate edits to inherited migrations, including
  edits restored at the head. For each historical commit, use the migration inventory of its
  common ancestor with the trusted base. This avoids requiring a later base-only addition in an
  earlier feature tree. Once that base addition is inherited, it is protected too. Fail closed
  when the required ancestry is unavailable or ambiguous. Newly introduced, unmerged PR migrations
  remain editable so hosted findings can be fixed before acceptance.
- A same-tree manual rerun still checks migration structure. The report distinguishes an empty
  comparison range from proof of a historical upgrade. Rewritten-away commits cannot be recovered
  from Git alone; do not claim otherwise.

All SQL already on the trusted base is frozen without a marker. Corrections to that history use
new forward files. The current one-new-file-per-layer transition is a scope limit for this change,
not a permanent Goose limit.

## Hosted checks and adjacent simplification

Preserve the existing required auth job names, security scans, read-only permissions, action pins,
credential-free checkouts, and both fail-closed aggregates. No job becomes optional to permit this
change. Add `corpus-validation` as a required reusable-checks dependency and update the independent
required-job inventory and contract tests. It calls the existing `corpus-source.yml` reusable
workflow, which remains independently dispatchable. This is synthetic persistence validation,
not corpus ingestion, real-document evaluation, or rollout.

Auth generation selects its sqlc entry by output/package identity instead of its old schema path.
Corpus generation selects the other five entries. Retain narrow lane outputs and use one simple
selection convention; do not build a general generation framework. Generation never writes SQL.
Remove first-migration creation, Atlas hash/validate calls, opaque-object delta generation, and
temporary differ databases. Retain reviewed formatting and dependency drift checks. Do not expand
this slice into cache policy, dependency automation, or global formatting changes.

| Lane | Required result |
|---|---|
| Migration history | Trusted-base and earlier-PR comparisons plus negative contract tests |
| Auth generation | Migration-backed sqlc, formatting/dependency drift, clean and upgrade schema/role parity, narrow review artifact |
| Auth PostgreSQL | Existing bootstrap denial, capability roles, runtime/operator restrictions, identity, migration, cleanup, and OAuth behavior |
| Corpus validation | Migration-backed sqlc, clean replay, populated trusted-base upgrade, catalog comparison, full existing schema suite, narrow review artifact |
| Existing required CI | Build, vet, short tests, browser tests, hygiene, Gitleaks, Semgrep SAST/SCA, and both aggregates |

Build baseline migrators from the trusted comparison tree. Do not call a candidate-tree binary
captured before generation the baseline. Use pinned PostgreSQL 17 and pgvector, disposable databases,
and synthetic credentials. Auth clean and upgrade comparisons retain independent PostgreSQL role
state so one side cannot create roles that mask failures on the other side.

For corpus replay, apply the candidate chain to an empty database. Separately apply the base chain,
insert representative legacy rows across affected layers, record their values and identities, then
apply the candidate chain. Compare normalized catalogs using the existing catalog inventory and
assert preservation of the populated rows. Keep functions, triggers, constraints, indexes, sequence
behavior, and existing Goose ledger rows in scope. Reapplying the candidate must succeed without
new schema or data changes. The newly added tables must not require seeding or starting workers.

Run `TestReleaseActivationRace` alone against a fresh candidate-migrated database because it commits
fixed fixture identities. Run the remaining rollback-based `internal/corpus/schematest` tests
against a separate fresh candidate-migrated database. Retain every behavioral assertion and
raw-query fixture. Combine both `go test -json` streams and require every expected test exactly
once; reject absent tests, empty selections, nested skips, and failures. Add a contract test that
rejects reuse of one database URL for both groups. This isolation belongs to the required corpus
lane; unrelated auth-race qualification stays in its owning M0 work. Do not replace behavior tests
with table-existence checks. Ordinary short tests remain separate evidence.

Retain artifact-before-drift-failure ordering. Artifacts contain only their lane's generated Go,
existing explicitly allowed formatting/dependency outputs, and a verification report. They no
longer contain generated SQL or checksums. Record exact candidate/base SHAs, tool/database versions,
actual checks, and generated additions, changes, deletions, and file digests. Reject unexpected
paths and inconsistent manifests. Hashes here describe artifacts, not migration history authority.
Review the artifact before import; only hosted generation may change `pkg/store/`. Require zero
generated drift on the final committed tree, including untracked output and removed generated files.

Delete only the seven Semgrep transition records whose primary source is removed
`tools/migragen/main.go`. Replace the `identity-callback-local-redirect` supporting-source binding
from `sql/auth/schema.sql` to the unchanged auth migration, with its verified bytes and independent
review. Preserve that record's disposition and all unrelated entries. Keep strict matching and
require a fresh hosted SAST run; do not replace the inventory with a broad baseline.

## Negative evidence and acceptance

Hosted contract tests must reject an edited, deleted, renamed, reordered, or duplicate old migration;
an inserted lower version; a differently padded append whose lexical and numeric orders disagree;
an extension or auth history mutation; an earlier-PR edit to inherited SQL; and unavailable history.
Recomputing a digest cannot make changed historical SQL pass. Include valid append, edits to new
unmerged SQL, diverged history, and an advanced base that added a migration after the feature branch
point, while retaining the restored-old-edit negative.

Database negatives must prove omitted required functions/triggers fail the retained behavior suite,
missing auth roles fail bootstrap, privilege violations remain denied, and populated legacy data
survives upgrade. Generation negatives must catch new untracked files, deleted generated files,
wrong-lane writes, malformed artifact manifests, and missing or skipped test suites. Workflow
contracts must reject removing a required child or weakening either aggregate or security scan.

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
| Hosted checks | `.github/workflows/{ci,reusable-checks,corpus-source}.yml`, migration/generation helpers and tests in `tools/ci/`, the scoped Semgrep inventory changes above; hand off required-job contracts, artifact manifest, replay fixtures, security record changes, and evidence scope |
| Primary integration | Artifact inspection/import into `pkg/store/`, generated dependency/formatting artifacts, documentation indexes, engineering/verification rules, architecture, auth/corpus runbooks and owning plans, `todo.md`, Git operations, and hosted evidence |

The primary resolves changed fact owners in the same integration change. Replace Atlas download
work with this removal contract while retaining unfinished M0 history, replay, role isolation,
artifact provenance, and broader CI gates. Do not mark M0 or corpus runtime qualification complete.
If hosted replay exposes a non-additive legacy discrepancy or sqlc incompatibility, report the
specific object or query and obtain a bounded reviewed correction before importing generated code.
