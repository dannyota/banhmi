#!/usr/bin/env bash
set -euo pipefail
readonly validation_dir="${RUNNER_TEMP:?}/migration-validation"
mkdir -p "${validation_dir}"
python3 - "${validation_dir}" <<'PY'
import json
import os
import subprocess
import sys
from pathlib import Path
sys.path.insert(0, 'tools/ci')
from check_migration_history import check

event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
kind = os.environ['GITHUB_EVENT_NAME']
head = subprocess.check_output(['git', 'rev-parse', 'HEAD'], text=True).strip()
if head != os.environ['GITHUB_SHA']:
    raise SystemExit('candidate checkout does not match event SHA')
if kind == 'pull_request':
    base = event['pull_request']['base']['sha']
    pr_head = event['pull_request']['head']['sha']
elif kind == 'push':
    base = event['before']
    pr_head = None
elif kind == 'workflow_dispatch':
    branch = event['repository']['default_branch']
    if branch != 'master':
        raise SystemExit('manual comparison requires the protected master branch')
    subprocess.run(['git', 'fetch', '--no-tags', 'origin', 'refs/heads/master'], check=True)
    base = subprocess.check_output(['git', 'rev-parse', 'FETCH_HEAD'], text=True).strip()
    pr_head = None
else:
    raise SystemExit('unsupported migration comparison event')
report = check(base, head, kind == 'pull_request')
if pr_head:
    subprocess.run(['git', 'merge-base', '--is-ancestor', pr_head, head], check=True)
report.update(pr_head=pr_head, event=kind, run=os.environ['GITHUB_RUN_ID'],
              attempt=os.environ['GITHUB_RUN_ATTEMPT'])
Path(sys.argv[1], 'history.json').write_text(json.dumps(report, indent=2) + '\n')
with open(os.environ['GITHUB_ENV'], 'a') as env:
    env.write(f'BASE_SHA={base}\nCANDIDATE_SHA={head}\nVALIDATION_DIR={sys.argv[1]}\n')
PY
