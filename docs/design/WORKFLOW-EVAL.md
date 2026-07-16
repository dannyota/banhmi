# Workflow Eval

Measures the MCP evidence contract end-to-end: can a stand-in agent (small model, no repo access)
complete realistic multi-step legal tasks using only the MCP tools?

## Pieces

| Component | Path | Purpose |
|-----------|------|---------|
| **mcpcall** | `tools/mcpcall/main.go` | Unix-socket bridge: holds one long-lived MCP stdio session so each tool call avoids the ~40s ONNX cold start |
| **Workflow golden set** | `deploy/eval/workflow_golden_vn.json` | 10 VN pilot cases: multi-step tasks requiring search + document + relations |
| **Scorer** | `pkg/eval/workflow.go` | Scores agent transcripts: citation correctness, abstain accuracy, relation compliance |

## mcpcall

Two modes:

1. **Server** — `go run ./tools/mcpcall -serve [-jurisdiction vn]`
   - Spawns `go run -tags onnx ./cmd/mcp` with ONNX env, holds the stdio session.
   - Listens on Unix socket (default `/tmp/mcpcall-{jurisdiction}.sock`).
   - One connection at a time (MCP is single-threaded).

2. **Client** — `go run ./tools/mcpcall <tool> '<json-args>'`
   - Connects to the socket, sends one call, prints result JSON to stdout.

## Execution model

The orchestrator (a Claude Code session) runs one stand-in agent per case:

1. Start the mcpcall server once: `go run ./tools/mcpcall -serve`
2. For each case, spawn a sub-agent with the task + the mcpcall contract.
3. The agent calls tools via `go run ./tools/mcpcall <tool> '<args>'`, reasons over results, and
   outputs a transcript JSON.
4. Collect transcripts and score with `pkg/eval` functions.

### Per-case agent prompt template

```
You are testing the banhmi MCP evidence service. Complete this task using
ONLY the provided tool. Do not use any other knowledge.

TASK: {case.task}

TOOL: Run `go run ./tools/mcpcall <tool> '<json-args>'` where tool is one of:
  - corpus_status (no args)
  - search       (required: "query"; optional: "top_k", "detail", "in_force_only", "include_related")
  - document     (use "doc_number" or "document_id"; optional: "include", "citation")
  - guide        (no args)
  - quality_gaps (optional: "category", "limit")

RULES:
- Search in Vietnamese.
- Check relations when a document may have been amended or replaced.
- If the corpus lacks coverage, say so — do not fabricate citations.

OUTPUT: Return ONLY this JSON (no other text):
{
  "case_id": "{case.id}",
  "cited": [{"doc_number": "...", "article": "..."}],
  "abstained": false,
  "tool_calls": [{"tool": "search", "args": {...}}, ...]
}
```

### Scoring

- **citation_correct** — fraction of expected citations found in the agent's cited list
  (case-insensitive doc_number; article must match when expected specifies one).
- **abstain_correct** — agent's `abstained` matches `expect_abstain`.
- **followed_relations** — if `must_check_relations`, agent must have called the `document` tool
  at least once.

## Golden set (VN pilot, 10 cases)

| ID | Type | Relations | Citations |
|----|------|-----------|-----------|
| wf-pin-amendment-check | Amendment chain verification | yes | 50/2024 + 77/2025 |
| wf-online-banking-security-history | Supersession chain | yes | 50/2024 + 35/2016 + 35/2018 |
| wf-cross-border-data-chain | Law + implementing decree | yes | 91/2025 + 356/2025 |
| wf-fintech-sandbox-scope | Multi-provision read | no | 94/2025 D1 + D8 |
| wf-infosys-level3-details | Same-doc navigation | no | 09/2020 D5 + D14 |
| wf-payment-intermediary-current | Current vs superseded | yes | 52/2024 + 101/2012 |
| wf-esig-legal-basis | Legal hierarchy traversal | yes | 23/2025 + 20/2023 |
| wf-cybersec-law-amendment | No-amendment confirmation | yes | 116/2025 |
| wf-abstain-labor-law | Out-of-scope (labor) | no | (abstain) |
| wf-abstain-insurance-capital | Out-of-scope (insurance) | no | (abstain) |
