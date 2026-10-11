@AGENTS.md

Use `.claude/agents/designer.md` for Claude design and research.
For Codex jobs, resolve `CODEX_COMPANION` to the installed Codex plugin's
`codex-companion.mjs`. Do not store its installation path in the repository.
From the worktree that owns the job, run:

    node "$CODEX_COMPANION" task --background [--write] --fresh --model <model> --effort <effort> "<prompt>"
    node "$CODEX_COMPANION" status <job-id>
    node "$CODEX_COMPANION" result <job-id>

The brackets mark an optional flag, not literal command text.
Use `--write` only for a job authorized to edit its owned files.
Run status and result from the same directory as task.
Start fix passes with `--fresh` and a full brief. `--resume-last` resumes the most recent
job in that directory, which may be a different role's thread.
Use the role, model, effort, and review isolation required by AGENTS.md.
