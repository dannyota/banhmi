# Workflow evaluation

Workflow evaluation tests whether a stand-in agent can use only banhmi's MCP tools to complete a
multi-step evidence task. It complements retrieval evaluation: retrieval eval scores ranked chunks,
while workflow eval scores a supplied agent transcript.

The repository does not run the stand-in model. An external orchestrator must create one transcript
per case. The repository supplies the MCP bridge, golden set, transcript types, and scorer.

## Current components

| Component | Path | Role |
|---|---|---|
| MCP bridge | `tools/mcpcall/main.go` | Keeps one stdio MCP process alive behind a Unix socket. |
| Golden set | `deploy/eval/workflow_golden_vn.json` | Ten Vietnamese pilot tasks with expected citations, abstention, and relation-check requirements. |
| Scoring library | `pkg/eval/workflow.go` | Loads cases and scores transcript JSON. |
| Scoring command | `tools/wfscore/main.go` | Reads transcript files and prints per-case and aggregate results. |

The current golden set covers amendment and supersession checks, legal-basis traversal,
same-document provision navigation, and two out-of-scope controls. It is a Vietnam-only pilot; no
workflow golden files exist yet for the other jurisdictions.

## Run the MCP bridge on an isolated worker

Provision the worker with an assigned database, ONNX model, tokenizer, and runtime library. Never
point workflow evaluation at a live service database. Start one server from the repository root:

```bash
go run ./tools/mcpcall -serve -jurisdiction vn
```

The server:

- starts `go run -tags onnx ./cmd/mcp -config config/config.yaml`;
- derives fixed ONNX model, tokenizer, and runtime paths from the worker account's user directory;
- requires the worker to provision those fixed locations because the helper does not accept model
  path overrides;
- injects `BANHMI_DATABASE_PASSWORD=banhmi`; the remaining database settings must select the
  assigned worker database;
- performs the MCP initialize handshake once;
- listens on `/tmp/mcpcall-vn.sock` by default;
- handles one socket connection at a time.

Override the socket with `-socket`. The client timeout defaults to 120 seconds. The bridge is an
evaluation helper, not the production split-service path.

Call a tool from another shell:

```bash
go run ./tools/mcpcall -jurisdiction vn corpus_status '{}'
go run ./tools/mcpcall -jurisdiction vn search '{"query":"an toàn ngân hàng trực tuyến","detail":"compact"}'
go run ./tools/mcpcall -jurisdiction vn document '{"doc_number":"50/2024/TT-NHNN","include":["relations","amendments"]}'
```

The available MCP tools are `guide`, `corpus_status`, `quality_gaps`, `search`, and `document`.
`mcpcall` forwards any tool name; the MCP server validates it.

## Produce transcripts

For each object in `deploy/eval/workflow_golden_vn.json`, give the stand-in agent the case `task` and
the command contract above. The agent may call MCP tools as needed, but it must not use repository
files or outside legal knowledge.

Write one JSON file per case to a transcript directory:

```json
{
  "case_id": "wf-pin-amendment-check",
  "cited": [
    {"doc_number": "50/2024/tt-nhnn", "article": "11"},
    {"doc_number": "77/2025/tt-nhnn"}
  ],
  "abstained": false,
  "tool_calls": [
    {"tool": "search", "args": {"query": "độ dài mã PIN ngân hàng trực tuyến"}},
    {"tool": "document", "args": {"doc_number": "50/2024/TT-NHNN", "include": ["relations", "amendments"]}}
  ]
}
```

`cited` is the result being scored. The transcript schema does not include the agent's prose answer.
Keep the actual call history in `tool_calls`; the scorer does not contact MCP or reconstruct calls.

## Score transcripts

```bash
go run ./tools/wfscore \
  -golden deploy/eval/workflow_golden_vn.json \
  -transcripts test/samples/eval/wf-transcripts
```

The command reads every `*.json` file in the directory. It prints the number of files scored against
the ten golden cases plus three aggregate percentages. It does not write a JSON report, apply
thresholds, or exit nonzero for missing cases or low scores.

| Metric | Exact rule |
|---|---|
| Citation correctness | Per case, the fraction of expected citations present in `cited`; aggregate is the unweighted mean of case fractions. Document numbers and articles use trimmed, case-insensitive exact matching. Extra citations do not reduce an in-scope case's score. An abstention case scores 1 only when `cited` is empty. |
| Abstention accuracy | Fraction of scored transcripts whose `abstained` value equals `expect_abstain`. |
| Relation compliance | Among cases with `must_check_relations=true`, fraction whose transcript contains at least one `document` call. The scorer does not verify that the call requested relations or that the agent read them. |
| Mean tool calls | Mean length of `tool_calls` across scored transcripts. The library computes it, but the current command does not print it. |

Search and document call counts are stored in each `WorkflowResult`. Only a `document` call satisfies
the current relation proxy; `search` with related hits does not.

## Interpretation limits

Workflow scores are evidence-use proxies, not proof that an answer is legally correct.

- The scorer checks expected citations, not the truth or completeness of answer prose.
- Citation matching is stricter than retrieval eval: it does not canonicalize document numbers or
  honor `alt_doc_numbers`.
- Relation compliance checks only for a recorded `document` call.
- Unexpected extra citations are ignored on in-scope cases.
- Missing, malformed, and unknown-case transcript files are reported and skipped. The aggregate uses
  only successfully loaded files, so always check the `cases scored: N/10` line.
- Duplicate transcripts for one case are not rejected and would weight that case more than once.
- The golden loader validates case IDs, tasks, and duplicate IDs, but it does not validate that each
  expected in-scope case has citations.

The baseline recorded in [PLAN](../../PLAN.md#current-state) came from Haiku stand-in agents and the
ten-case Vietnam pilot. Treat it as a dated reference, not a release gate. A new model, prompt,
corpus, or tool contract requires a new labeled run; do not compare it as if the conditions were
unchanged.

## Recommended completion checks

Before accepting a workflow run:

1. Confirm `cases scored: 10/10` and one unique transcript exists for every golden case ID.
2. Preserve the model name, prompt, code revision, corpus revision, and run date beside the results.
3. Inspect failures against `scoring_notes`; a citation miss, relation-call miss, and corpus gap need
   different fixes.
4. Run the retrieval eval separately. Workflow scores do not replace recall, ranking, validity, or
   abstention gates in `cmd/eval`.

The initial GitHub checks do not run this evaluation. A hosted workflow needs an isolated corpus,
model inputs, a transcript orchestrator, and explicit thresholds before it can become a merge gate.
