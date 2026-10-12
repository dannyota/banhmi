"""Static contracts shared by the hosted workflow policy tests."""

from __future__ import annotations

import copy
import json
import re
from pathlib import Path

from security_scan import TOKEN_EXPRESSION


PINNED_ACTION = re.compile(r"[^@\s]+@[0-9a-fA-F]{40}")


def check_workflow(workflow: dict, root: Path) -> list[str]:
    errors: list[str] = []
    jobs = workflow.get("jobs", {})
    for job_id, job in jobs.items():
        if job.get("secrets") == "inherit":
            errors.append(f"{job_id}: inherited secrets are forbidden")
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


def check_security_workflows(workflows: dict[str, dict]) -> list[str]:
    """Keep the named token boundary and all three unconditional scans explicit."""
    errors: list[str] = []
    caller = workflows.get("ci.yml", {})
    reusable = workflows.get("reusable-checks.yml", {})
    if "pull_request_target" in caller.get("on", {}):
        errors.append("security: pull_request_target is forbidden")
    checks = caller.get("jobs", {}).get("checks", {})
    if checks.get("secrets") != {"SEMGREP_APP_TOKEN": TOKEN_EXPRESSION}:
        errors.append("security: caller must map only the named token for eligible events")
    call = reusable.get("on", {}).get("workflow_call", {})
    if (not isinstance(call, dict) or call.get("inputs")
            or call.get("secrets") != {"SEMGREP_APP_TOKEN": {"required": False}}):
        errors.append("security: declare only the optional named secret without trust inputs")
    jobs = reusable.get("jobs", {})
    for scanner in ("gitleaks", "semgrep-sast", "semgrep-sca"):
        job = jobs.get(scanner, {})
        timeout = job.get("timeout-minutes")
        if (job.get("name") != scanner or job.get("permissions") != {"contents": "read"}
                or "if" in job or job.get("needs") or "env" in job or "uses" in job
                or job.get("runs-on") != "ubuntu-latest"
                or type(timeout) is not int or not 1 <= timeout <= 20):
            errors.append(f"security: {scanner} must be an independent bounded read-only job")
        steps = job.get("steps", [])
        if len(steps) != 2:
            errors.append(f"security: {scanner} must contain only checkout and scan")
            continue
        checkout, step = steps
        expected_with = {"persist-credentials": False}
        if scanner == "gitleaks":
            expected_with["fetch-depth"] = 0
        if (checkout.get("uses") != "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"
                or checkout.get("with") != expected_with or "if" in checkout):
            errors.append(f"security: {scanner} checkout contract changed")
        expected_env = {} if scanner == "gitleaks" else {"SEMGREP_APP_TOKEN": "${{ secrets.SEMGREP_APP_TOKEN }}"}
        if (step.get("run") != f"python3 tools/ci/security_scan.py {scanner}"
                or step.get("env", {}) != expected_env or "if" in step or "uses" in step):
            errors.append(f"security: {scanner} must run its unconditional private scan")

    # Token references are allowed only in the caller mapping, declaration,
    # and the two Semgrep scan-step environments checked above.
    remaining = copy.deepcopy(workflows)
    remaining.get("ci.yml", {}).get("jobs", {}).get("checks", {}).pop("secrets", None)
    call = remaining.get("reusable-checks.yml", {}).get("on", {}).get("workflow_call")
    if isinstance(call, dict):
        call.pop("secrets", None)
    for scanner in ("semgrep-sast", "semgrep-sca"):
        steps = remaining.get("reusable-checks.yml", {}).get("jobs", {}).get(scanner, {}).get("steps", [])
        if len(steps) == 2:
            steps[1].pop("env", None)
    if "SEMGREP_APP_TOKEN" in json.dumps(remaining):
        errors.append("security: token exposed outside the two Semgrep scan steps")
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
