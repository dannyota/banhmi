# Verification

GitHub executes all builds, tests, lint, and corpus evaluations. On a workstation, contributors may
read files, edit files, inspect diffs, and perform other lightweight static review. Do not start a
stack, install dependencies, run builds, run tests, run lint, execute pipelines, or run evaluations
locally.

## Initial CI baseline

The first baseline consists of `.github/workflows/ci.yml` calling
`.github/workflows/reusable-checks.yml`. It must use read-only permissions, pinned actions, cancellation
for superseded runs, and one aggregate result that always reports.

The reusable workflow covers:

- repository hygiene and portable public documentation;
- the default Go build;
- `go vet`;
- short Go tests that need no credentials or external services.

Do not claim hosted proof until GitHub has run the published commit. Record the workflow run and commit
when they exist. A skipped or unavailable check is not a pass.

## Checks outside the initial baseline

The initial workflow does not prove database integration, corpus evaluations, workflow evaluations,
tagged ONNX builds, source integration, pipeline behavior, migrations against a database, or release
automation. Add hosted inputs and runners before enabling those checks. Keep credentials and
production connections out of pull request jobs.

Match evidence to the claim:

- Unit and short package tests can prove coded behavior.
- Database integration can prove schema and query behavior.
- Real-document evaluation can prove corpus behavior.
- A hosted smoke check can prove the deployed MCP contract.
- Only a successful deployment and post-deployment check can prove deployed behavior.
