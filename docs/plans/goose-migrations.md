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

**Status:** Design and plan passed independent review. Implementation is in progress;
no generated output or hosted result is accepted yet.
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
| CI worker | `.github/workflows/{ci,reusable-checks,corpus-source}.yml`; `tools/ci/` migration guards, generation/replay/artifact scripts, associated Python tests, workflow contracts, `required-jobs.txt`, and scoped `semgrep-legacy.json` records; no scanner policy changes |
| Primary | Documentation and indexes; hosted artifact review/import into `pkg/store/`, `go.mod`, `go.sum`, and formatting-only Go patches; exact-commit evidence, Git, and integration |
| Independent reviewer | Read-only design/plan review, implementation review, adversarial migration/CI/security review, and fix confirmation in separate jobs |

Migration and CI workers can prepare disjoint changes together after design/plan review. CI execution
and artifact import depend on the integrated migration source. Generated stores must be reviewed and
accepted before any future persistence consumer starts.

Each brief includes the spec, this plan, repository authorities, owned paths, forbidden actions,
required hosted checks, and a report of changed paths, contracts, static findings, and remaining gates.

## Review focus

- A changed, deleted, renamed, or temporarily edited old migration fails trusted-history admission.
- A populated legacy database retains its rows, sequence behavior, ledgers, and replay idempotence.
- New functions and triggers preserve reviewed bodies and cross-layer references in runtime order.
- Auth grants, capability roles, memberships, and bootstrap guards retain their existing restrictions.
- Missing suites, nested skips, generated drift, artifact provenance gaps, and skipped jobs fail admission.

## Task 1: Review the transition contract

- [x] Obtain independent Sol xhigh design and plan review. Resolve findings through the authors and
  require separate fix confirmation before dispatching implementation.
- [ ] Record the trusted transition base and the five pending additive DDL inventories in the spec.
  Treat the name-only count of 33 tables, 9 functions, and 31 triggers as an inventory, not database proof.
- [ ] Confirm all pending DDL is additive and executable in the existing runtime order. An incompatible
  alteration stops dependent work for a reviewed explicit forward migration; do not rewrite history.

## Task 2: Replace schema generation with reviewed migration source

**Consumes:** Approved transition inventory and existing migration chains.
**Produces:** Reviewed Goose SQL chains and sqlc configuration consumed by Task 3.

- [ ] Add meaningful regression cases before implementation. Pin embedded SQL availability, corpus
  order and ledger names, auth isolation, complete routine parsing, and pending trigger behavior.
  Keep existing integration cases valid; delete only tests whose sole contract is removed checksum code.
- [ ] Write these additive migrations with explicit Goose statement boundaries for routine bodies:

  | Layer | New file |
  |---|---|
  | Bronze | `deploy/migrations/bronze/00002_corpus_redesign.sql` |
  | Silver | `deploy/migrations/silver/00009_corpus_redesign.sql` |
  | Gold | `deploy/migrations/gold/00003_corpus_redesign.sql` |
  | Ingest | `deploy/migrations/ingest/00006_corpus_redesign.sql` |
  | Config | `deploy/migrations/config/00008_corpus_redesign.sql` |

- [ ] Copy only pending additive DDL, preserving its reviewed semantics, dependencies, constraints,
  indexes, defaults, comments, function bodies, and triggers. Add no destructive Down SQL for immutable
  evidence. Do not add an auth or extension migration for this transition.
- [ ] Point the six `sqlc.yaml` schema entries at their own migration directories. Preserve queries,
  overrides, package names, interfaces, and output paths. Use `deploy/authmigrations/*.sql` for auth
  to exclude bootstrap SQL. Confirm the pinned sqlc parses actual Goose Up SQL without a preprocessor.
  Remove the six `sql/*/schema.sql` files only
  with the transition parity gate in Task 3; do not maintain a second schema authority.
- [ ] Remove `tools/migragen/`, seven `atlas.sum` files, runtime checksum functions and calls, and
  `cmd/migrate -check`. Embed SQL only. Preserve runtime configuration, Goose calls, order, and ledgers.
- [ ] Remove `migrate-gen` and obsolete Atlas help from `Makefile`; keep migration application and
  hosted sqlc generation contracts clear. Do not broaden formatter or developer-stack cleanup.
- [ ] Inspect the complete diff against the trusted base. Hand off exact migration paths, versions,
  order, ledger names, and integration cases to the CI worker and independent reviewer.

## Task 3: Require history, replay, generation, and artifact evidence

**Consumes:** Task 2 SQL-only migration chains and unchanged runtime commands.
**Produces:** Required corpus validation, auth validation, and reviewable hosted sqlc artifacts.

- [ ] Add negative tests for trusted-base guard admission before implementing it: old SQL modification,
  deletion, rename, an edit later restored in the PR, version collision, earlier appended version,
  missing trusted history, a differently padded higher-version append, and a valid higher-version
  addition. Cover auth, extensions, and all layers.
  Accept corrections to new unmerged PR SQL while freezing every trusted-base SQL file. Keep valid
  diverged-history, base-advanced migration, and same-tree cases.
- [ ] Create `tools/ci/check_migration_history.py` and `test_migration_history.py`. The CLI takes
  `--base BASE_SHA --head CANDIDATE_SHA` and `--check-pr-history` for PR history checks. It reads
  Git tree paths/blob bytes for the seven allowed roots, validates regular SQL files and numeric
  versions, and requires filename lexical order to equal numeric version order in each root. Preserve
  the existing five-digit corpus and fourteen-digit auth filename conventions. Compare the current
  trusted base directly against the tested candidate. For each earlier PR commit, compare its SQL
  against the frozen inventory from its common ancestor with the current trusted base. Fail unavailable or
  ambiguous ancestry. Allow base-only additions absent before synchronization; protect them after
  inheritance. This ancestor inventory never replaces the final current-base comparison.
  A temporary old-file edit later restored fails; a correction to new PR SQL remains allowed.
- [ ] Resolve `BASE_SHA` from PR base SHA, push before SHA, or a fetched protected default-branch
  reference for manual runs. Bind `CANDIDATE_SHA` to the actual tested checkout and record PR head
  separately. Fetch full required history without persisted credentials. Fail on missing roots,
  malformed refs, unexpected file types/locations, invalid names, duplicate versions, or absent history.
  Never substitute a merge base for the final comparison or use a working-tree snapshot. Test
  diverged history, a base addition after branching, and same-tree manual
  reruns; reports distinguish structural checks from a historical upgrade.
- [ ] Remove every Atlas setup/hash/validate invocation and all SQL creation from generation scripts.
  Retain separate auth and corpus sqlc generation, allowlisted artifacts, and final drift rejection.
  Select the auth entry by output/package identity and the five corpus entries by the same narrow
  convention. Remove old canonical-versus-generated synchronization and Atlas-only tests/config.
- [ ] Verify the transition's clean corpus replay matches the trusted base's canonical schema catalog,
  including all pending tables, constraints, indexes, functions, and triggers. Use matching PostgreSQL
  and pgvector versions. Extract old canonical files to runner temporary storage only. Auth retains
  schema, role, grant, and membership parity and bootstrap negatives on independent PostgreSQL role
  states. Later bases without canonical files report transition comparison as inapplicable while
  every regular replay, behavior, generation, and required-job check still runs.
- [ ] Build the trusted-base migrator on GitHub, replay its old chain into an isolated upgrade database,
  seed synthetic legacy rows, then apply candidate migrations. Check retained data and legacy ledgers,
  new schema objects, and a second no-op replay. Compare clean and upgraded catalogs.
- [ ] Run `TestReleaseActivationRace` alone on a fresh independently migrated race database. Run
  every remaining rollback-based corpus schema test on a separate independently migrated database.
  The expected list is `TestCorpusSchemaBoundaries`, `TestImmutableEvidenceAndReviewConstraints`,
  `TestStageIdempotencyCompletionAndFence`, `TestReleaseActivationCompareAndSet`,
  `TestReleaseActivationRace`, and `TestReleaseManifestRequiresEvidence`.
  Combine both `go test -json` outputs against the complete expected test list, requiring each test
  exactly once; reject absent tests, no execution, nested skips, and failures. Add a negative contract
  rejecting the same database URL for both groups. Keep committed fixed race identities isolated
  from later tests that reuse those identities. Retain auth PostgreSQL/OAuth evidence in its existing
  lane; separate unfinished auth race isolation remains an M0 gate.
  Pin negatives for omitted required functions/triggers, untracked and deleted generated files,
  wrong-lane writes, malformed manifests, omitted suites, and nested skips. Keep existing behavioral
  assertions and raw-query fixtures rather than replacing them with table-existence checks.
- [ ] Add unconditional `corpus-validation` to reusable checks via `corpus-source.yml`. Update the
  required-job inventory, aggregate dependencies/lists, workflow contracts, and truthful summaries.
  Preserve all existing required jobs and security trust boundaries.
- [ ] Stage only generated stores and approved generation outputs. Reports identify exact commits,
  trusted base, run, inputs, tool versions, additions/modifications/deletions, file hashes, and actual
  check scope. Artifacts contain no credentials, raw scanner output, checksum ledgers, or generated SQL.
- [ ] Remove exactly the seven records bound to deleted `tools/migragen/main.go`. Replace the deleted auth
  canonical supporting source for `identity-callback-local-redirect` with the equivalent initial
  auth migration binding after manual byte/semantic review. Preserve its disposition and all other
  bindings. Fresh hosted SAST must prove the remaining inventory; never refresh it automatically.
  Keep full scans and unrelated source/deployment holds. Obtain independent adversarial review.
- [ ] Obtain separate implementation and adversarial review. Return defects to the owning worker;
  require independent fix confirmation before primary acceptance.

## Task 4: Import hosted output and reconcile current facts

- [ ] Primary integrates the reviewed source and CI changes and publishes only under current owner
  push authorization. The first hosted generation run may reject drift while uploading review artifacts.
- [ ] Review artifact provenance, hashes, exact diff, and deletions. Import only allowlisted files.
  Never hand-edit generated stores or dependency sums; return generation defects to their owner.
- [ ] Update current facts in `instructions/engineering.md`, `instructions/verification.md`,
  `docs/ARCHITECTURE.md`, `docs/DEVELOPMENT.md`, `docs/README.md`, migration runbooks, and owning
  corpus/Vietnam/security plans. Include `docs/specs/corpus-database.md`, `docs/design/schema.md`,
  `docs/design/AUTH.md`, `docs/design/OAUTH-STORAGE.md`, `docs/plans/google-access.md`, and
  `docs/plans/corpus-redesign-source.md` where current schema facts change. Preserve historical
  hosted evidence. Replace stale Atlas/schema-generation claims and link this spec/plan.
- [ ] Remove the Atlas-download TODO only after the dependency is gone. Narrow M0 migration, corpus,
  and artifact queue items only by their completed evidence; retain unrelated auth race, format, cache,
  update, protection, real-corpus, and deployment gates.
- [ ] Obtain whole-candidate independent review and adversarial fix confirmation after artifact import.
  Do not accept a reviewer report without primary diff inspection.

## Hosted acceptance commands

GitHub runs these commands with disposable inputs. None runs locally.
For the corpus rollback database, select the five non-race tests by their exact anchored names;
do not run the race test again in that database or weaken the combined expected-test inventory.

| Check | Command or lane | Required result |
|---|---|---|
| Helper regressions | `python3 -m unittest discover -s tools/ci -p 'test_*.py'` | All history, workflow, artifact, and security contracts pass |
| Maintained docs | `python3 tools/ci/check_repository_hygiene.py` | No stale paths, malformed records, or maintained Markdown violations |
| Workflow/shell lint | `actionlint`; `shellcheck tools/ci/*.sh` after verified installs | Both pass without weakened contracts |
| Auth generation/replay | `bash tools/ci/generate_auth.sh`; `go run ./cmd/auth-migrate`; retained auth parity/bootstrap scripts | Migration-derived sqlc, clean/upgrade replay, roles/grants, and zero drift pass |
| Corpus generation/replay | `bash tools/ci/generate_corpus.sh`; `bash tools/ci/verify_corpus_generation.sh`; `bash tools/ci/check_corpus_generation.sh` | Trusted history, transition parity, populated upgrade, tests, and zero drift pass |
| Corpus schema tests | `go test -json -count=1 -run '^TestReleaseActivationRace$' ./internal/corpus/schematest` on the race database; remaining tests on a separate migrated database | Combined expected-test inventory is complete; no failure, nested skip, or absent suite |
| Default Go | `go build ./...`; `go vet ./...`; `go test -short ./...` | All pass on final source and imported generation |
| Existing required lanes | Auth PostgreSQL, website browser, Gitleaks, Semgrep SAST, Semgrep SCA, both aggregates | Every declared child equals `success` |

## Evidence and remaining gates

- [ ] Record exact final candidate commit, hosted run and attempt, trusted transition base, artifact
  identity/hash, synthetic inputs, PostgreSQL/pgvector/sqlc versions, and verified scope here.
- [ ] Require zero drift on the final candidate and up-to-date `CI`; missing, skipped, cancelled, or
  failing required results block merge. Primary resolves every blocking review thread before merge.
- [ ] Record landed default-branch and manual generation evidence before closing this plan.

Implementation is in progress. This plan does not authorize production migration, country deployment,
consumer implementation, or M0 closure. Synthetic replay does not establish real-corpus parity.
