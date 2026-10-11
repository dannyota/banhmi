# Country modules implementation plan

Status: Proposed execution plan. No task, review, hosted check, or migration is complete.
Paired spec: [Country modules](../specs/country-modules.md).
Canonical Vietnam plan: [Vietnam rebuild](vietnam.md).

## Goal and authority

Build the shared country seam with Vietnam, then migrate Malaysia, Singapore, Indonesia, Thailand, and Cambodia as compiled, offline-tested modules in state `migrated-disabled`. This plan owns country-module tasks, status, and evidence. The paired spec owns behavior and qualification contracts. The Vietnam plan owns the canonical M0-M6 order.

O1-O3 approve the package boundaries, admission contract, and offline compatibility harness. Those decisions remain unimplemented. Vietnam is the only country eligible to reach `enabled` under this plan. No task enables another country, admits evidence for it, runs its workflow, changes an AWS service, or delays Vietnam.

## Shared work mapped to Vietnam M0-M3

The shared core and Vietnam work executes only through the canonical tasks in the [Vietnam plan](vietnam.md). This table records the country-module dependency and adds no second sequence or acceptance gate.

| Canonical milestone | Country-module work supplied there | Country-module proof carried by that milestone |
|---|---|---|
| M0 | Neutral country contracts, side-effect-free registry, Vietnam skeleton, six-country v1 inventory, frozen seeds, goldens, floors, and snapshot manifests | Registry/import denial before I/O, manifest hashes, existing CI, and recorded legacy baseline gaps. Other new modules remain `absent`. |
| M1 | Shared persistence, workflow composition, capture, sandbox boundaries, conversion contracts, and pure Vietnam source planners/decoders | Hosted generation, source/capture contracts, converter sandbox denial, retry ownership, and no v1 mutation calls. |
| M2 | Vietnam body, part, identity, relation, validity, subject, language, and qualification rules on protected real evidence | Exact native citations, part ownership, legal targets and dates, source coverage, privacy, and named gaps. Other-country migration remains out of scope. |
| M3 | Shared release, retrieval, evaluation, publication, and admission boundaries with the Vietnam write path run end to end | Exact-commit B/C/G/I/Q/R/L/P evidence, unchanged Vietnam floors, release closure, rollback, and independent review. M3 exit permits D to start. |

M3 may automatically accept only clean first-pass Vietnam revisions that pass inherited deterministic gates. D needs M3 hosted end-to-end evidence because its modules remain disabled and cannot accept evidence or run workflows. D does not depend on the M5 review API. Disputed evidence, reprocessing, accepted gaps, and legal-meaning corrections await M5 human approval under the Vietnam plan.

## Status

Update this table in D's integration change. Use only `not started`, `in progress`, or `done` with an evidence link.

| Task | State | Depends on | Evidence |
|---|---|---|---|
| D.1 Five disabled-country slices | not started | Vietnam M3 hosted end-to-end evidence | None |
| D.2 Admission and compatibility audit | not started | D.1 handoffs | None |

## D Disabled-country migration

Goal: Port five v1 modules after M3 hosted end-to-end evidence without opening their new runtime or delaying Vietnam.

1. **D.1 Five disjoint country slices.** Own `internal/country/{my,sg,id,th,kh}/`, one country per worker. Serialize `catalog` edits through the integrator.
   Reuse the full [country-modules reuse map](../specs/country-modules.md#v1-reuse-map): source packages in `pkg/ingest/`, country parsers [Malaysia](../../pkg/pipeline/malaysiaparse.go#L32), [Singapore](../../pkg/pipeline/singaporeparse.go#L22), [Indonesia](../../pkg/pipeline/indonesiaparse.go#L28), [Thailand](../../pkg/pipeline/thailandparse.go#L21), [Cambodia](../../pkg/pipeline/cambodiaparse.go#L20), and unchanged per-country seeds, goldens, and floors.
   Write pure roots, grammar, planners and sandbox decoders, source factories, locale and data manifests, and offline compatibility tests. Preserve the `cdcgov` package to `cdc` source mapping, source access conditions, native citations, languages, authority exceptions, and explicit capability gaps.
   All five entries finish as `migrated-disabled`. They never instantiate live sources, create pools, schedule or start workflows, submit provider jobs, promote releases, or expose routes or tools.
2. **D.2 Admission and compatibility audit.** Own new country contract fixtures and catalog integration after D.1 handoffs. Reuse the [v1 registry](../../pkg/base/jurisdiction/jurisdiction.go#L47) only in offline compatibility adapters.
   Write import, factory, and data completeness checks, shared helper extraction without cross-country imports, and disabled admission at CLI, workflow, child, provider, SQL, promotion, and read boundaries. Keep all six modules compiled and unit-tested without state-based skips.
   Remove wrappers only after caller and recovery audits. Any replacement that claims retrieval parity needs its own unchanged-floor gate.

Run at most four Codex jobs across this plan and the Vietnam plan. Use one country per D.1 worker within available slots. Reduce D concurrency first when Vietnam needs capacity. The integrator alone edits the catalog and shared helpers after inspecting country handoffs. Vietnam never waits for D reports or integration.

## Exit evidence

Require B/C and `vietnam-intake.yml` jobs `source-contracts` and `converter-sandbox` green for migrated decoders, using the exact hosted-check definitions in the [Vietnam plan](vietnam.md#hosted-evidence-contract). Offline tests prove source split, parser and citation contracts, no constructor effects, and denial of disabled starts, queued results, and routes before I/O.

Record L comparisons and unavailable cases for every affected country. L results do not gate D unless a replacement explicitly claims retrieval parity. Such a claim must meet the unchanged country floors from the spec: `vn .90/.66/.99/.95`, `my .92/.77/.99/.95`, `id .78/.60/.99/.98`, `sg .90/.75/.99/.90`, `th .86/.68/.99/.90`, and `kh .90/.70/.99/.95` for recall, mean reciprocal rank, current-law precision, and abstention. Never lower a floor or change an expected citation to pass migration.

Real-document evidence uses frozen v1 snapshots only. New-source qualification, new-format quality, and actual enablement require a separate country plan and explicit owner approval. Keep known v1 defects distinct from new-contract expectations. Record protected fixture or snapshot gaps, source access conditions, unsupported formats, new parts or validity gaps, persistent adapter gaps, and unavailable hosted lanes.

Independent adversarial review covers disabled-country admission and every touched shared mutation boundary. O1-O3 are settled. No D task crawls, provisions a writer, runs a country workflow, seeds a live database, exposes a route, changes an AWS service, or changes an existing country hostname or deployment.

