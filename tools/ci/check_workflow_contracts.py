"""Static contracts shared by the hosted workflow policy tests."""

from __future__ import annotations

import re
from pathlib import Path


PINNED_ACTION = re.compile(r"[^@\s]+@[0-9a-fA-F]{40}")


def check_workflow(workflow: dict, root: Path) -> list[str]:
    errors: list[str] = []
    jobs = workflow.get("jobs", {})
    for job_id, job in jobs.items():
        permissions = job.get("permissions")
        if not isinstance(permissions, dict):
            errors.append(f"{job_id}: declare job permissions")
        elif any(value not in ("read", "none") for value in permissions.values()):
            errors.append(f"{job_id}: permissions must be read-only")
        # No job or step may tolerate failure; there are no exceptions.
        for node in [job, *job.get("steps", [])]:
            if "continue-on-error" in node and node["continue-on-error"] is not False:
                errors.append(f"{job_id}: continue-on-error must be literal false or absent")
            reference = node.get("uses")
            if reference is None:
                continue
            if node is job and reference.startswith("./.github/workflows/"):
                path = root / reference
                if path.resolve().parent != (root / ".github/workflows").resolve() or not path.is_file():
                    errors.append(f"{job_id}: local workflow must exist in .github/workflows")
            elif not PINNED_ACTION.fullmatch(reference):
                errors.append(f"{job_id}: uses must pin a full commit SHA: {reference}")
            if reference.startswith("actions/checkout@"):
                if node.get("with", {}).get("persist-credentials") is not False:
                    errors.append(f"{job_id}: checkout must set persist-credentials: false")

    if "ci" in jobs:
        aggregate = jobs["ci"]
        needs = aggregate.get("needs", [])
        needs = [needs] if isinstance(needs, str) else needs
        if set(needs) != set(jobs) - {"ci"}:
            errors.append("ci: aggregate must depend on every other job")
        if aggregate.get("if") not in ("${{ always() }}", "always()"):
            errors.append("ci: aggregate must use always()")
        gate_steps = [step for step in aggregate.get("steps", [])
                      if step.get("run") == "python3 tools/ci/check_results.py"]
        if len(gate_steps) != 1:
            errors.append("ci: aggregate must call check_results.py once")
        else:
            gate = gate_steps[0]
            if "if" in gate and gate["if"] not in ("${{ always() }}", "always()"):
                errors.append("ci: gate condition must use always() or be absent")
            env = gate.get("env", {})
            if set(env.get("REQUIRED_JOBS", "").split()) != set(needs):
                errors.append("ci: REQUIRED_JOBS must match needs")
            if env.get("NEEDS_JSON") != "${{ toJSON(needs) }}":
                errors.append("ci: gate must read actual needs JSON")
    return errors


def check_required_jobs(workflows: dict[str, dict], inventory: str) -> list[str]:
    """Keep required jobs independent of workflow job and dependency edits."""
    errors: list[str] = []
    required: set[tuple[str, str]] = set()
    for line in inventory.splitlines():
        if not line or line.startswith("#"):
            continue
        fields = line.split()
        if len(fields) != 2 or fields[0] not in ("ci.yml", "reusable-checks.yml") or fields[1] == "ci":
            errors.append(f"Invalid required-job inventory record: {line}")
            continue
        workflow_name, job_id = fields
        record = (workflow_name, job_id)
        if record in required:
            errors.append(f"Duplicate required-job inventory record: {line}")
        required.add(record)
        jobs = workflows.get(workflow_name, {}).get("jobs", {})
        if job_id not in jobs:
            errors.append(f"{workflow_name}: required job {job_id} is missing")
        needs = jobs.get("ci", {}).get("needs", [])
        needs = [needs] if isinstance(needs, str) else needs
        if job_id not in needs:
            errors.append(f"{workflow_name}: aggregate needs missing required job {job_id}")
    for workflow_name in ("ci.yml", "reusable-checks.yml"):
        if not any(name == workflow_name for name, _ in required):
            errors.append(f"{workflow_name}: required-job inventory must not be empty")
    return errors
