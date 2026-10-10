# Workflow and ownership

## Scope and decisions

- Read the current architecture, plan, and relevant design before changing behavior.
- Keep the change within the requested scope. Propose adjacent work before implementing it.
- Present material design choices with trade-offs and one recommendation. The owner decides new
  schema patterns, retrieval or chunking strategies, source access methods, and architecture changes.
- Start with the smallest design that solves the stated problem. Record every stub, skipped step, and
  deferral.
- Confirm effects instead of treating a clean command exit as proof. Distinguish coded, validated,
  deployed, and proposed states.

## Agent roles

The primary agent owns planning, coordination, verification, Git operations, and the final report.
Workers must receive a bounded brief with the objective, authority, constraints, owned paths,
forbidden actions, required checks, and report format. Use independent workers only for separable work
with no overlapping file ownership.

Select only model identifiers available in the current session. Set the model and effort explicitly
on every dispatch.

| Work | Model | Effort |
|---|---|---|
| Management, design, complex analysis, and planning | `gpt-5.6-sol` | `medium` |
| Implementation and debugging | `gpt-5.6-sol` | `medium` |
| Exploration, search, summaries, test execution, and mechanical edits | `gpt-6-luna` | `low` |
| Every code, defect, plan, and phase review, including fix confirmation | `gpt-5.6-sol` | `xhigh` |

- Preserve role independence. A designer must not implement the same slice. An author must not review
  their own work.
- Scale review depth with risk. Mutation paths, credentials, corpus integrity, and release workflows
  require adversarial independent review.
- The primary agent must inspect worker changes and verification evidence before accepting them.
- Workers must not commit, push, deploy, change cloud resources, or rewrite history.

## Git

- The primary agent performs Git operations and preserves unrelated changes in a shared checkout.
- Use Conventional Commits in imperative mood with a subject under 72 characters. Describe only the
  change and its purpose.
- Do not add AI attribution, `Co-authored-by`, `Signed-off-by`, or generated-by trailers.
- Do not create a branch unless the user or workflow requires one.
- Do not push unless the user explicitly requests a push.
- Preserve global and repository ignore settings. Add narrow repository exceptions only when a tracked
  project file requires one. Never change a global ignore file or `core.excludesFile`.
