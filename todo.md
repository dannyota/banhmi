# Short-term work

This is the open execution queue. Remove an item when its stated evidence is complete and the work
is merged. Keep partial work here with only its remaining action. Record results in the owning plan;
do not turn this file into a changelog.

The [Vietnam plan](docs/plans/vietnam.md) owns milestone order and acceptance. [PLAN.md](PLAN.md)
owns the project roadmap. M0 is accepted as the gate for M1; its residual CI hygiene items are listed
below and do not block M1.

## Next: rules, schema reset, CI reduction

- [ ] Land the rule changes: legacy freeze, fresh corpus chains, slice sizing, and the approved CI
      contract. [AGENTS.md](AGENTS.md#product-and-evidence-boundaries)
- [ ] Revise the corpus database spec for fresh writer and release chains, then re-slice M1 into
      pull-request-sized slices. [Corpus database](docs/specs/corpus-database.md), [M1](docs/plans/vietnam.md#m1-hosted-intake-skeleton)
- [ ] CI reduction slice: resolve the adversarial review findings, pass hosted CI on the exact
      commit, review the generation artifact, and merge. [Hosted check matrix](instructions/verification.md#hosted-check-matrix)
- [ ] Schema reset slice: new writer and release chains, sqlc entries, generated stores under
      `internal/`, and replay in the generation job. [Corpus database](docs/specs/corpus-database.md)

## After the schema reset

- [ ] Start the first re-sliced M1 slice. [M1](docs/plans/vietnam.md#m1-hosted-intake-skeleton)

## CI hygiene, not gating

- [ ] Isolate auth race databases; add JSON test-presence and skip checks with mandatory OAuth
      coverage. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Unify Make and CI formatting into one failure-propagating contract. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Assign cache writers and record cold and warm cache evidence. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Add dependency and action-pin update PRs without auto-merge. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Record repository protection evidence, allowed-action and release-environment checks, and
      disposable negative controls. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)

## Evidence and release holds

- [ ] Obtain approved frozen six-country snapshots and run the baseline comparisons; preserve missing-input records until then. This does not block the completed asset inventory. [Foundation evidence](docs/plans/country-modules.md#evidence-and-progress)
- [ ] Qualify conversion and corpus behavior on real documents under the protected evidence lane; synthetic reader and chunking tests do not establish corpus parity. [M2](docs/plans/vietnam.md#m2-real-vietnam-evidence)
- [ ] Before affected ingestion or deployment, close the Congbao intermediate-fetch and Thailand certificate-verification holds with reviewed fixes and source evidence. [Security holds](docs/plans/security-ci.md#sast-dispositions-and-release-gates)

Keep AWS services and the other five countries unchanged. Later milestones, owner decisions, and
deployment work stay in their owning plans rather than expanding this queue.
