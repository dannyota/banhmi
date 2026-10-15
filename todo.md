# Short-term work

This is the open execution queue. Remove an item when its stated evidence is complete and the work
is merged. Keep partial work here with only its remaining action. Record results in the owning plan;
do not turn this file into a changelog.

The [Vietnam plan](docs/plans/vietnam.md) owns milestone order and acceptance. [PLAN.md](PLAN.md)
owns the project roadmap. M0 is accepted as the gate for M1; its residual CI hygiene items are listed
below and do not block M1.

## Next: M1 slices

- [ ] Resolve the phase 2 owner questions in their consuming slices under [M1.13](docs/plans/vietnam.md#m113-initial-quality-and-residency-gate).
- [ ] Deliver M1.13-2A through 2D in the dependency order recorded by [M1.13](docs/plans/vietnam.md#m113-initial-quality-and-residency-gate).
- [ ] Move the source and revision adapters' raw SQL statements marked `RAW QUERY` into their query files and regenerate, with no behavior change. [M1.3](docs/plans/vietnam.md#m13-source-accounting-and-bronze)

## CI hygiene, not gating

- [ ] Confirm the recorded protections with disposable negative branches. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Add protected release environments before M3 publishing. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)

## Evidence and release holds

- [ ] Obtain approved frozen six-country snapshots and run the baseline comparisons; preserve missing-input records until then. This does not block the completed asset inventory. [Foundation evidence](docs/plans/country-modules.md#evidence-and-progress)
- [ ] Qualify conversion and corpus behavior on real documents under the protected evidence lane; synthetic reader and chunking tests do not establish corpus parity. [M2](docs/plans/vietnam.md#m2-real-vietnam-evidence)
- [ ] Before affected ingestion or deployment, close the Congbao intermediate-fetch and Thailand certificate-verification holds with reviewed fixes and source evidence. [Security holds](docs/plans/security-ci.md#sast-dispositions-and-release-gates)

Keep AWS services and the other five countries unchanged. Later milestones, owner decisions, and
deployment work stay in their owning plans rather than expanding this queue.
