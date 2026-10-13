# Documentation maintenance

## Fact ownership and indexes

- Give each fact one home: product scope, architecture, decisions, operations, or delivery plan.
  Link that home elsewhere. [AGENTS.md](../AGENTS.md) owns contributor rules and writing conventions.
- Keep [todo.md](../todo.md) as the short-term open queue, with one concrete action per checkbox
  and a link to its owning plan. The Vietnam plan owns milestone order; PLAN.md owns the roadmap.
  On completion, record exact evidence and remaining limits in the owning plan, then remove the
  queue item when its exit evidence is complete and the work is merged. Narrow partially completed
  items instead of checking off missing evidence. Do not keep a completed-task log in todo.md.
- Update current architecture and plans when behavior changes. Remove stale claims from each owner.
- Link every durable design from the documentation index. Repair orphan documents and add an index
  for a multi-document topic.
- Pair each spec with a plan: its same-name plan under `docs/plans/`, or one existing plan that the
  spec's status line names as its owner. A spec continuation `docs/specs/<name>-<concern>.md` shares
  its parent's owner and links both ways. The spec owns behavior and contracts; the plan owns tasks,
  evidence, and remaining gates. Link each to the other.
- Existing document paths stay until the rename slice. Do not create placeholder specs or plans.

## Links and maintenance procedure

1. Identify the document that owns each changed fact and the indexes that link it.
2. Edit that home and replace duplicate rules with links to the authority.
3. Update affected links and anchors. Use current repository-relative paths and check targets exist.
4. Inspect maintained Markdown against [repository hygiene](../tools/ci/check_repository_hygiene.py).
   Hosted CI runs the checker; local work inspects text and diffs only.
5. Reconcile todo.md and affected plan status in the same change. Check architecture, plans, runbooks,
   and indexes for contradictory status or contracts. Record
   unresolved conflicts and stop dependent work until the shared documents agree.
