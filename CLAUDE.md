@AGENTS.md

Use `.claude/agents/designer.md` for Claude design and research.
For Codex jobs, resolve `CODEX_COMPANION` to the newest installed Codex plugin, never a pinned
version path:

    CODEX_COMPANION="$(ls -d ~/.claude/plugins/cache/openai-codex/codex/*/ | sort -V | tail -1)scripts/codex-companion.mjs"

From the worktree that owns the job, run:

    node "$CODEX_COMPANION" task --background [--write] --fresh --model <model> --effort <effort> "<prompt>"
    node "$CODEX_COMPANION" status <job-id>
    node "$CODEX_COMPANION" result <job-id>

The brackets mark an optional flag, not literal command text.
Use `--write` only for a job authorized to edit its owned files.
Run status and result from the same directory as task.
Start fix passes with `--fresh` and a full brief. `--resume-last` resumes the most recent
job in that directory, which may be a different role's thread.
Wait for a job in a background loop that polls `status <job-id> --json` no more often than every
90 seconds and stops when the job is completed, failed, cancelled, or missing.
Use the role, model, effort, and review isolation required by AGENTS.md.

GitHub evidence and landing:

    gh pr checks <pr>                               # required check is `CI`
    gh run view <run-id> --log-failed               # error lines for a troubleshooter brief
    gh run download <run-id> -n <artifact> -D <dir> # generation artifacts; review before import
    gh api -X DELETE notifications/threads/<id>     # mark a handled notification done

Review a generation artifact under [verification](instructions/verification.md) and
[engineering](instructions/engineering.md) rules before import. For a formatting-only patch,
`git diff -w -- '*.go'` after applying it must print nothing; review every module change.
Answer each review thread, then resolve it; the `master` ruleset blocks merge while a thread is open.
