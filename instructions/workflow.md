# Workflow and ownership

## Task briefs

- Name the objective, authorities, constraints, owned paths, forbidden actions, required checks, and
  report contract. Use [roles and dispatch](../AGENTS.md#roles-and-dispatch).
- Keep work within the named scope. Propose adjacent work before implementation.
- Present material design choices with trade-offs and one recommendation. The owner decides new
  schema patterns, retrieval or chunking strategies, source access methods, and architecture changes.
- Start with the smallest design that solves the problem. Record stubs, skipped steps, and deferrals.

## Slices

- One slice is one pull request that one reviewer reads in one pass: one contract, package group,
  or document set with its tests. Split a task that spans more than one package group or needs more
  than one review focus.
- A milestone is an acceptance gate made of slices. Its exit evidence accumulates across slices.
  No slice waits for unrelated polish in the same milestone.
- Order slices by dependency: contracts, then persistence, then consumers. Freeze a contract in its
  own slice before a dependent slice starts.
- Resolve an owner decision in the slice that consumes it. Record it in the owning spec before
  implementing it.
- Name the slice's exact owned paths, the hosted checks that prove it, and the documents it must
  update in its brief and in its pull request.

## Ownership and handoffs

- Assign non-overlapping files to independent workers. Respect project concurrency limits.
- Preserve design, implementation, and review ownership through each handoff.
- A handoff names changed paths, contracts, dependencies, evidence, and remaining gates.
- Inspect changes and evidence before dependent work starts. A worker report is supporting evidence,
  not acceptance. Resolve shared-document conflicts before dependent work resumes.

## CI failures

1. The primary reads the failing job and extracts the error lines and any generation artifact.
2. The primary briefs the troubleshooter with that evidence, the failing commit, and owned paths.
3. The troubleshooter finds the root cause and fixes it. It never weakens a test to hide a defect.
4. The primary commits and shows the commits, pushes only when the owner asks in the current
   conversation, and reads the exact commit's CI result.
5. An independent reviewer confirms the fix against that hosted result.

## Review findings

- Report each finding with severity, file and line, concrete failure, supporting evidence, and the
  required correction. Separate defects from optional suggestions.
- Return findings to the implementer. The independent reviewer confirms fixes before acceptance.
- Record unavailable reviewers and unresolved findings as gates, never as passes.

## Completion procedure

1. Inspect the complete diff and preserve unrelated changes.
2. Confirm effects with evidence under [verification](verification.md). A clean command exit alone
   does not prove the intended effect.
3. Resolve findings and obtain independent fix confirmation under [review rules](../AGENTS.md#roles-and-dispatch).
4. Update the documents that own changed facts. Record every skipped check and remaining gate.
5. Report changed paths, evidence, and open work. Use [Git rules](../AGENTS.md#git) for integration.
6. Before merge, answer and resolve every blocking review thread.
7. After landing, confirm the pull request has no open review thread. Mark only handled pull
   request, review, and failed-run notification threads done; leave unrelated and unresolved ones
   open.
