# Documentation maintenance

## Fact ownership and indexes

- Give each fact one home: product scope, architecture, decisions, operations, or delivery plan.
  Link that home elsewhere. [AGENTS.md](../AGENTS.md) owns contributor rules and writing conventions.
- Update current architecture and plans when behavior changes. Remove stale claims from each owner.
- Link every durable design from the documentation index. Repair orphan documents and add an index
  for a multi-document topic.
- Pair each spec with a same-name plan under `docs/specs/` and `docs/plans/`. The spec owns behavior
  and contracts; the plan owns tasks, evidence, and remaining gates. Link each to the other.
- Existing document paths stay until the rename slice. Do not create placeholder specs or plans.

## Links and maintenance procedure

1. Identify the document that owns each changed fact and the indexes that link it.
2. Edit that home and replace duplicate rules with links to the authority.
3. Update affected links and anchors. Use current repository-relative paths and check targets exist.
4. Inspect maintained Markdown against [repository hygiene](../tools/ci/check_repository_hygiene.py).
   Hosted CI runs the checker; local work inspects text and diffs only.
5. Check architecture, plans, runbooks, and indexes for contradictory status or contracts. Record
   unresolved conflicts and stop dependent work until the shared documents agree.
