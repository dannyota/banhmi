# Short-term work

This is the open execution queue. Remove an item when its stated evidence is complete and the work
is merged. Keep partial work here with only its remaining action. Record results in the owning plan;
do not turn this file into a changelog.

The [Vietnam plan](docs/plans/vietnam.md) owns milestone order and acceptance. [PLAN.md](PLAN.md)
owns the project roadmap. M0 is accepted as the gate for M1; its residual CI hygiene items are listed
below and do not block M1.

## Next: M1 slices

- [ ] Freeze the M1.13 codecs, configurable bounds, and synthetic fixtures under the approved formats. [M1.13](docs/plans/vietnam.md#m113-initial-quality-and-residency-gate)
- [ ] Land conversion catalog and retention persistence with the blob catalog guard, after M1.13. [M1.15](docs/plans/vietnam.md#m115-conversion-catalog-and-retention-persistence)
- [ ] Accept a gated revision atomically with the M1-prefix fixture, after M1.15. [M1.6](docs/plans/vietnam.md#m16-fenced-cross-store-acceptance)
- [ ] Move the source and revision adapters' raw SQL statements marked `RAW QUERY` into their query files and regenerate, with no behavior change. [M1.3](docs/plans/vietnam.md#m13-source-accounting-and-bronze)

## CI hygiene, not gating

- [ ] Isolate auth race databases; add JSON test-presence and skip checks with mandatory OAuth
      coverage. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Unify Make and CI formatting into one failure-propagating contract. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Assign cache writers and record cold and warm cache evidence. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Add dependency and action-pin update PRs without auto-merge. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Run the committed copies of the post-test generation checkers, so test code cannot rewrite them to pass. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Record repository protection evidence, allowed-action and release-environment checks, and
      disposable negative controls. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)

## Evidence and release holds

- [ ] Obtain approved frozen six-country snapshots and run the baseline comparisons; preserve missing-input records until then. This does not block the completed asset inventory. [Foundation evidence](docs/plans/country-modules.md#evidence-and-progress)
- [ ] Qualify conversion and corpus behavior on real documents under the protected evidence lane; synthetic reader and chunking tests do not establish corpus parity. [M2](docs/plans/vietnam.md#m2-real-vietnam-evidence)
- [ ] Before affected ingestion or deployment, close the Congbao intermediate-fetch and Thailand certificate-verification holds with reviewed fixes and source evidence. [Security holds](docs/plans/security-ci.md#sast-dispositions-and-release-gates)

Keep AWS services and the other five countries unchanged. Later milestones, owner decisions, and
deployment work stay in their owning plans rather than expanding this queue.
