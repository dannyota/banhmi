# Short-term work

Status: Archived. Development and maintenance have stopped. The unchecked items below record
unfinished work; no further development is planned. See [project status](AGENTS.md#project-status).

This was the open execution queue. The owning plans retain evidence and remaining gates.

The [Vietnam plan](docs/plans/vietnam.md) owns milestone order and acceptance. [PLAN.md](PLAN.md)
owns the project roadmap. M0 is accepted as the gate for M1; its residual CI hygiene items are listed
below and do not block M1.

## Next: M1 slices

- [ ] Implement M1.13-2D against the [residency contract](docs/specs/quality-gate-residency.md); keep production R2 admission open. [M1.13](docs/plans/vietnam.md#m113-initial-quality-and-residency-gate)

## CI hygiene, not gating

- [ ] Add protected release environments before M3 publishing. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)

## Evidence and release holds

- [ ] Obtain approved frozen six-country snapshots and run the baseline comparisons; preserve missing-input records until then. This does not block the completed asset inventory. [Foundation evidence](docs/plans/country-modules.md#evidence-and-progress)
- [ ] Qualify conversion and corpus behavior on real documents under the protected evidence lane; synthetic reader and chunking tests do not establish corpus parity. [M2](docs/plans/vietnam.md#m2-real-vietnam-evidence)
- [ ] Before affected ingestion or deployment, close the Congbao intermediate-fetch and Thailand certificate-verification holds with reviewed fixes and source evidence. [Security holds](docs/plans/security-ci.md#sast-dispositions-and-release-gates)

Later milestones, owner decisions, and deployment work remain archived in their owning plans.
