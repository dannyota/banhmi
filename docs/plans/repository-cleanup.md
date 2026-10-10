# Repository cleanup plan

Make contributor rules easy to find, keep public files portable, and establish reusable GitHub
checks before restructuring the application.

## Boundaries

- Keep the current product name, runtime behavior, corpus, and deployment unchanged.
- Preserve the existing design proposal and operator tooling while removing private context from
  their public documentation.
- Track `AGENTS.md`, `CLAUDE.md`, and `instructions/` as project documentation. Add only scoped
  repository ignore exceptions. Preserve global Git ignore settings.
- Use repository-relative links and configurable runtime locations. Do not publish workstation
  checkout paths, references to unrelated projects, personal accounts, or secret values.
- Run builds, tests, and validation in GitHub CI. Reading files and reviewing diffs are permitted
  on the workstation. Do not start a local stack or duplicate hosted checks.
- Do not commit, push, deploy, rename the repository, or modify cloud resources in this cleanup.

## Instruction structure

`AGENTS.md` becomes the shared entry point with product boundaries, authority, and a reading map.
`CLAUDE.md` loads it and adds no competing rules. Detailed rules have one home under `instructions/`:
workflow and roles, verification, engineering, corpus integrity, documentation, and security.

Keep independent design, implementation, and review ownership. The primary agent coordinates work
and owns Git operations. Worker briefs name files, limits, checks, and the expected report.
Preserve the owner's model routing and explicit model selection rules.

## Work

1. Replace the large tool-specific guide with the shared entry point, loader, and topic files.
   Preserve substantive engineering and corpus safeguards, and remove stale machine instructions.
2. Rewrite development guidance around hosted checks and cloud workers. Keep self-hosting and
   runtime reference material distinct from permission to run workloads on a workstation.
3. Remove references to unrelated projects and workstation locations from public documentation.
   Link the new instructions and this plan from the documentation index.
4. Add reusable GitHub checks with read-only permissions, pinned actions, cancellation of
   superseded runs, and one aggregate result that always reports.
5. Cover repository documentation and default Go build, vet, and short tests. State that corpus
   evaluations, tagged ONNX builds, database integration, and release automation remain separate
   work until their hosted inputs and runners exist.
6. Review all changed files, instruction links, Git tracking behavior, and workflow security.
   Record hosted checks as unrun until the changes are published and GitHub executes them.

## Review focus

- No duplicated authority between `AGENTS.md`, `CLAUDE.md`, and topic instructions.
- No loss of citation, validity, migration, source provenance, or secret-handling safeguards.
- No instruction that mistakes a proposal for deployed behavior or a skipped check for a pass.
- No credentials, write permissions, production connections, or unsafe pull-request execution in
  the initial CI jobs.
- No globally ignored tool state or credentials admitted by broad ignore exceptions.
- No public documentation that relies on another checkout or a particular maintainer's machine.

## Verification

Independent review checks the instruction structure and workflows. GitHub runs repository hygiene,
default Go compilation, vet, and short tests after publication. Local builds, tests, and pipeline
runs are outside this task's authorization. Report the exact hosted run and commit when available.
