# Short-term work

This is the open execution queue. Remove an item when its stated evidence is complete and the work
is merged. Keep partial work here with only its remaining action. Record results in the owning plan;
do not turn this file into a changelog.

The [Vietnam plan](docs/plans/vietnam.md) owns milestone order and acceptance. [PLAN.md](PLAN.md)
owns the project roadmap. The foundation and compatibility moves are merged; M0 remains in progress.

## Next: complete the M0 checks

- [ ] Verify the downloaded Atlas archive against a recorded checksum in both auth and corpus generation lanes; prove a wrong digest stops execution. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Compare auth, corpus, and extension migration history with the trusted base, including earlier PR commits; reject changed, deleted, renamed, or rehashed old migrations. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Add isolated race databases and JSON test-presence checks; require OAuth tests and reject missing suites or nested skips. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Wire required corpus validation into PR CI, add populated base-to-candidate replay, and review hosted generation artifacts before importing them; require zero drift on the final tree. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Make auth and corpus artifacts describe additions, modifications, deletions, file hashes, and actual verification scope; reject missing or altered provenance. [M0.3](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Unify Make/CI formatting into one failure-propagating contract. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Assign cache writers, remove unnecessary check dependencies, and record cold/warm cache evidence. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Add dependency and action-pin update PRs without auto-merge. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Finish repository protection evidence, allowed-action and release-environment checks, and disposable negative controls. [M0.4](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)
- [ ] Close M0 only after its remaining required lanes, reviewed generation, independent review, and final PR/default-branch/manual evidence pass. [Exit evidence](docs/plans/vietnam.md#m0-core-layout-and-ci-gate)

## After M0

- [ ] Resolve applicable open decisions and freeze M1 consumer contracts, then split persistence, workflow composition, Vietnam source capture, and block conversion into owned slices. Start dependent implementations only after their contract and artifact handoffs. [M1](docs/plans/vietnam.md#m1-hosted-intake-skeleton)

## Evidence and release holds

- [ ] Obtain approved frozen six-country snapshots and run the baseline comparisons; preserve missing-input records until then. This does not block the completed asset inventory. [Foundation evidence](docs/plans/country-modules.md#evidence-and-progress)
- [ ] Qualify conversion and corpus behavior on real documents under the protected evidence lane; synthetic reader and chunking tests do not establish corpus parity. [M2](docs/plans/vietnam.md#m2-real-vietnam-evidence)
- [ ] Before affected ingestion or deployment, close the Congbao intermediate-fetch and Thailand certificate-verification holds with reviewed fixes and source evidence. [Security holds](docs/plans/security-ci.md#sast-dispositions-and-release-gates)

Keep AWS services and the other five countries unchanged. Later milestones, owner decisions, and
deployment work stay in their owning plans rather than expanding this queue.
