# Goose migrations implementation plan

> **For agentic workers:** Use `superpowers:subagent-driven-development` to execute owned tasks.
> Repository roles, hosted-only verification, and independent review take precedence.

**Goal:** Make reviewed Goose SQL the only schema source and remove Atlas and `tools/migragen`.

**Architecture:** Append the pending additive corpus DDL to the existing per-layer Goose chains.
Point each sqlc entry at its chain. Keep runtime ledgers and migration order unchanged.
Git history guards and disposable PostgreSQL replay replace Atlas checksums and schema generation.

**Tech stack:** Existing Go module, Goose, sqlc 1.31.1, PostgreSQL 17, pgvector, Python CI helpers.

**Spec:** [Goose migrations](../specs/goose-migrations.md).
The [Vietnam plan](vietnam.md) retains M0 acceptance authority.

**Status:** Complete. Implementation, generated-store import, independent adversarial review,
and required PR/default-branch/manual generation checks passed. The transition is merged with
zero generated drift. M0 acceptance remains separate.
**Transition base:** `1626a06ea0d1afc2760e80c47d951c976ad271d5`.
The transition source inventory uses base `1626a06ea0d1afc2760e80c47d951c976ad271d5`.
Event-resolved comparison SHAs remain the authority for each hosted history check.

## Constraints

- Preserve every existing migration SQL file's path and bytes, including auth and extensions.
- Preserve `dirOrder`, all seven Goose version-table names, auth separation, and bootstrap SQL.
- Add no checksum framework, runtime history manifest, migration generator, or new Go dependency.
- Do not hand-edit `pkg/store/` or `go.sum`. Generate, format, resolve dependencies, and test only on GitHub.
- Do not install dependencies, run apps, connect to production, read secrets, or change cloud resources.
- Workers never commit, push, deploy, or merge. The primary owns Git and artifact import.
- Preserve full security scans, pinned actions, read-only permissions, and both fail-closed aggregates.
- Do not start persistence consumers, change corpus policy, deploy migrations, or change any country service.

## Ownership and handoffs

Use separate branches and worktrees for implementation owners. The primary integrates their reviewed
changes into one candidate pull request. Neither owner edits the other's files.

| Owner | Exact paths and responsibility |
|---|---|
| Migration worker | `deploy/migrations/` SQL additions, embeds, and checksum deletions; `deploy/authmigrations/embed.go`, `embed_test.go`, and `atlas.sum`; `cmd/migrate/`; `cmd/auth-migrate/`; `tools/migragen/`; `sql/*/schema.sql`; `sqlc.yaml`; `Makefile`; `internal/corpus/schematest/` |
| CI worker | `.github/workflows/{ci,reusable-checks}.yml`; `tools/ci/` migration guards, generation/replay/artifact scripts, associated Python tests, workflow contracts, `required-jobs.txt`, and scoped `semgrep-legacy.json` records; no scanner policy changes |
| Primary | Documentation and indexes; hosted artifact review/import into `pkg/store/`, `go.mod`, `go.sum`, and formatting-only Go patches; exact-commit evidence, Git, and integration |
| Independent reviewer | Read-only design/plan review, implementation review, adversarial migration/CI/security review, and fix confirmation in separate jobs |

Migration and CI workers can prepare disjoint changes together after design/plan review. CI execution
and artifact import depend on the integrated migration source. Generated stores must be reviewed and
accepted before any future persistence consumer starts.

Each brief includes the spec, this plan, repository authorities, owned paths, forbidden actions,
required hosted checks, and a report of changed paths, contracts, static findings, and remaining gates.

## Review focus

Current CI review follows the [hosted check matrix](../../instructions/verification.md#hosted-check-matrix):
trusted event bases, frozen legacy trees, append-only auth, isolated schema tests, generated-tree
artifacts, drift rejection, and complete aggregate dependencies. The evidence section retains the
exact transition inputs and results.

## Task 1: Review the transition contract

- [x] Obtain independent Sol xhigh design and plan review. Resolve findings through the authors and
  require separate fix confirmation before dispatching implementation.
- [x] Record the trusted transition base and the five pending additive DDL inventories in the spec.
  Treat the name-only count of 33 tables, 9 functions, and 31 triggers as an inventory, not database proof.
- [x] Confirm all pending DDL is additive and executable in the existing runtime order. An incompatible
  alteration stops dependent work for a reviewed explicit forward migration; do not rewrite history.

## Task 2: Replace schema generation with reviewed migration source

**Consumes:** Approved transition inventory and existing migration chains.
**Produces:** Reviewed Goose SQL chains and sqlc configuration consumed by Task 3.

- [x] Add meaningful regression cases before implementation. Pin embedded SQL availability, corpus
  order and ledger names, auth isolation, complete routine parsing, and pending trigger behavior.
  Keep existing integration cases valid; delete only tests whose sole contract is removed checksum code.
- [x] Write these additive migrations with explicit Goose statement boundaries for routine bodies:

  | Layer | New file |
  |---|---|
  | Bronze | `deploy/migrations/bronze/00002_corpus_redesign.sql` |
  | Silver | `deploy/migrations/silver/00009_corpus_redesign.sql` |
  | Gold | `deploy/migrations/gold/00003_corpus_redesign.sql` |
  | Ingest | `deploy/migrations/ingest/00006_corpus_redesign.sql` |
  | Config | `deploy/migrations/config/00008_corpus_redesign.sql` |

- [x] Copy only pending additive DDL, preserving its reviewed semantics, dependencies, constraints,
  indexes, defaults, comments, function bodies, and triggers. Add no destructive Down SQL for immutable
  evidence. Do not add an auth or extension migration for this transition.
- [x] Point the six `sqlc.yaml` schema entries at their own migration directories. Preserve queries,
  overrides, package names, interfaces, and output paths. Use `deploy/authmigrations/*.sql` for auth
  to exclude bootstrap SQL. Confirm the pinned sqlc parses actual Goose Up SQL without a preprocessor.
  Remove the six `sql/*/schema.sql` files only
  with the transition parity gate in Task 3; do not maintain a second schema authority.
- [x] Remove `tools/migragen/`, seven `atlas.sum` files, runtime checksum functions and calls, and
  `cmd/migrate -check`. Embed SQL only. Preserve runtime configuration, Goose calls, order, and ledgers.
- [x] Remove `migrate-gen` and obsolete Atlas help from `Makefile`; keep migration application and
  hosted sqlc generation contracts clear. Do not broaden formatter or developer-stack cleanup.
- [x] Inspect the complete diff against the trusted base. Hand off exact migration paths, versions,
  order, ledger names, and integration cases to the CI worker and independent reviewer.

## Task 3: Require history, replay, generation, and artifact evidence

The [hosted check matrix](../../instructions/verification.md#hosted-check-matrix) owns the current
`generation` job, final-tree history guard, clean replay, isolated six-test schema suite,
whole-config sqlc generation, artifact upload, and drift rejection. Security jobs and their
transition inventory follow the [security CI spec](../specs/security-ci.md) unchanged.
The [evidence record](#evidence-and-remaining-gates) retains accepted transition checks and artifacts.

## Task 4: Import hosted output and reconcile current facts

- [x] Primary integrates the reviewed source and CI changes and publishes only under current owner
  push authorization. The first hosted generation run may reject drift while uploading review artifacts.
- [x] Review artifact provenance, hashes, exact diff, and deletions. Import only allowlisted files.
  Never hand-edit generated stores or dependency sums; return generation defects to their owner.
- [x] Update current facts in `instructions/engineering.md`, `instructions/verification.md`,
  `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md`, `docs/README.md`, migration runbooks, and owning
  corpus/Vietnam/security plans. Include `docs/specs/corpus-database.md`, `docs/design/schema.md`,
  `docs/design/AUTH.md`, `docs/design/OAUTH-STORAGE.md`, `docs/plans/google-access.md`, and
  `docs/plans/corpus-redesign-source.md` where current schema facts change. Preserve historical
  hosted evidence. Replace stale Atlas/schema-generation claims and link this spec/plan.
- [x] Remove the Atlas-download TODO only after the dependency is gone. Narrow M0 migration, corpus,
  and artifact queue items only by their completed evidence; retain unrelated auth race, format, cache,
  update, protection, real-corpus, and deployment gates.
- [x] Obtain whole-candidate independent review and adversarial fix confirmation after artifact import.
  Do not accept a reviewer report without primary diff inspection.

## Hosted acceptance commands

Use the [current hosted check matrix](../../instructions/verification.md#hosted-check-matrix).
Recorded transition evidence below retains its tested scope and does not substitute for an
exact-commit run of the current generation job.

## Evidence and remaining gates

The test checkpoint at `a23883ba6cca191aec29c61e85dfd6f5ffa3d9ae` ran in
[CI 38336732921](https://github.com/dannyota/banhmi/actions/runs/38336732921), attempt 1,
using merge checkout `560c026c7b1a6d2009c1c8801fc70eb1bf9226d0`.
`TestEmbeddedMigrationDiscovery` rejected the missing pending migration version in each of the
five corpus layers. The other seven required jobs passed. This proves the regression test detects
the old chain; it does not validate the implementation, PostgreSQL replay, or generated stores.

[Generation run 38340635985](https://github.com/dannyota/banhmi/actions/runs/38340635985),
attempt 1, tested head `30032123eda4d689b7d580d83ad7f82adde4816b` through merge checkout
`c35493a402205e33f8b1e1abb8135ba194185146`. Their trees are identical. Corpus generation,
canonical/clean/populated-upgrade parity, retained rows and sequences, complete-ledger no-op replay,
the appended-ledger-row negative, all six required schema tests, and both omitted-function/trigger
negatives passed. Only expected generated drift failed the corpus lane before artifact import.

Corpus artifact `11786810973`, named `corpus-source-c35493a402205e33f8b1e1abb8135ba194185146`,
has archive SHA-256 `fa7e90bfe3b0d3fd7b6ec242f66494f292393abe0e4cb60b0c637007073e0089`.
Independent review and primary inspection checked event-bound provenance, archive and payload hashes,
report integrity, path admission, SQL/query/scan correspondence, and compatibility. Exactly 20 modified
Go files under the five corpus store packages were imported in
`693dc686cbc7ae0371ce2a86156fe51f156ac1d7`; no SQL, dependency, auth, or checksum files were imported.
Auth artifact `11786409628` has archive SHA-256
`64c6c069595cf5cea905b313e14c4d0d77411b6865703ed4edfcab9c55d0ee0a`.
Its seven repository outputs were unchanged and its formatting patch was empty; no auth import was needed.

The generation run used Go 1.26.3, sqlc 1.31.1, Goose 3.27.0, corpus PostgreSQL 17.10 with
pgvector 0.8.2, and auth PostgreSQL 17.6. Inputs were disposable databases and synthetic fixtures
at the tested source commit, not production snapshots:

| Input | SHA-256 |
|---|---|
| `tools/ci/corpus_legacy_seed.sql` | `719269fa8f4b364a215e67ee923af6b937cec1fe9a569b8034f6bcf453c5d428` |
| `tools/ci/corpus_legacy_snapshot.sql` | `554e7094d072e2f2c8d12a55c99f826af96eac8a6d1cf6e6a2b7a9997bd18e5f` |
| `internal/corpus/schematest/schema_integration_test.go` | `819a807d856fde29a85ab805512f9c4dd5d2a627892db786bdff67d25f9059d0` |

[CI 38341496033](https://github.com/dannyota/banhmi/actions/runs/38341496033), attempt 1,
tested imported source head `693dc686cbc7ae0371ce2a86156fe51f156ac1d7` through merge checkout
`1bd3d258a0b47c315dc305875a7e7faa00b1ada8`. Both use tree
`4cf91d961f072faafa1a1f59e61f1192d40bf557` and trusted base
`1626a06ea0d1afc2760e80c47d951c976ad271d5`. All nine required jobs and both aggregates passed.
Auth and corpus report zero generated drift. Auth canonical/clean/populated-upgrade schema,
roles, grants, memberships, bootstrap negatives, retained data and sequences, complete-ledger
idempotence, and ledger-append rejection passed. Corpus repeated the full scope above.
Independent whole-candidate code and adversarial CI review found no unresolved blocking findings.

[PR 11](https://github.com/dannyota/banhmi/pull/11) merged as
`421db727bcaa9fafc849c2f9f68af12c798bd8d2`. The final reviewed head was
`935d46a59549f574111978caf36768d4899d0fd8`; [PR CI 38342567488](https://github.com/dannyota/banhmi/actions/runs/38342567488),
attempt 1, passed all nine required jobs and both aggregates. Its tested merge
`fab2b2bc80c6a43c392325ed31faa7887ac4c548`, final head, and landed merge have identical tree
`ef992a41ec8143947451b376191826bcdd97ef55`. GitHub verified the signed commits and merge;
no review threads remained unresolved.

[Default-branch CI 38342935071](https://github.com/dannyota/banhmi/actions/runs/38342935071),
attempt 1, passed all nine required jobs and both aggregates at landed merge
`421db727bcaa9fafc849c2f9f68af12c798bd8d2`. Its trusted base was
`1626a06ea0d1afc2760e80c47d951c976ad271d5`, preserving the historical transition comparison.
[Manual CI 38342948520](https://github.com/dannyota/banhmi/actions/runs/38342948520), attempt 1,
also passed all nine required jobs and both aggregates at the landed merge.

[Manual corpus generation 38342946096](https://github.com/dannyota/banhmi/actions/runs/38342946096),
attempt 1, passed at the same landed merge. Candidate and trusted base were both that merge:
this run proves structural checks and repeated replay, not an additional historical transition.
The canonical comparison was correctly inapplicable after canonical source removal. Clean and
populated replay, retained rows and sequences, complete-ledger idempotence and its negative,
all six schema tests on isolated databases, both omitted-DDL negatives, and zero drift passed.
The 20 artifact files were unchanged; primary inspection confirmed every payload and report hash.
Artifact `11787129131`, named `corpus-source-421db727bcaa9fafc849c2f9f68af12c798bd8d2`,
has archive SHA-256 `f08546028610ad1cbb207bf3ba3fce19bd8ddcd2d0c914a997472021bdae8a62`.
It required no import. Tool versions and synthetic input bytes match the recorded generation run.

- [x] Record reviewed source commit, hosted run and attempt, trusted transition base, artifact
  identity/hash, synthetic inputs, tool versions, and verified scope.
- [x] Require up-to-date `CI` on the final candidate after documentation reconciliation; missing,
  skipped, cancelled, or failing required results block merge. Resolve blocking review threads.
- [x] Record landed default-branch and manual generation evidence before closing this plan.

Source implementation, synthetic validation, and delivery are complete.
This plan does not authorize production migration, country deployment, consumer implementation,
or M0 closure. Synthetic replay does not establish real-corpus parity.
