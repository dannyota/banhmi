"""Reject incomplete CI evidence and report the observed child results."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path


def main() -> int:
    required = os.environ.get("REQUIRED_JOBS", "").split()
    error = ""
    try:
        needs = json.loads(os.environ.get("NEEDS_JSON", ""))
        if not isinstance(needs, dict):
            raise ValueError("needs must be an object")
    except (ValueError, TypeError) as exception:
        needs = {}
        error = f"Invalid needs JSON: {exception}"

    results = {}
    for job in sorted(set(required) | set(needs)):
        child = needs.get(job)
        result = child.get("result") if isinstance(child, dict) else None
        results[job] = result if isinstance(result, str) else "missing"
    passed = (
        bool(required) and not error and set(needs) == set(required)
        and all(result == "success" for result in results.values())
    )
    lines = ["### CI aggregate", "", "| Job | Result |", "| --- | --- |"]
    for job, result in results.items():
        # Render data as text rather than allowing a result to inject Markdown rows.
        job = str(job).replace("|", "&#124;").replace("\n", " ")
        result = result.replace("|", "&#124;").replace("\n", " ")
        lines.append(f"| {job} | {result} |")
    lines.extend(["", "CI aggregate: success" if passed else "CI aggregate: failed"])
    if error:
        lines.extend(["", error])
    summary = "\n".join(lines) + "\n"
    with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a", encoding="utf-8") as output:
        output.write(summary)
    print(summary, end="")
    return 0 if passed else 1


if __name__ == "__main__":
    sys.exit(main())
