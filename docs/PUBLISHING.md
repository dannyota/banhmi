# Publishing the MCP servers to agent directories

Checklist for listing the six jurisdiction MCP servers (banhmi, laksa, rendang, kaya, tomyum, amok)
in connector directories — written against the ChatGPT apps submission requirements
(learn.chatgpt.com/docs/submit-plugins); the same assets fit Claude/Gemini directories.

## What the server already provides (per domain)

| Requirement | URL / mechanism |
|---|---|
| **MCP endpoint** | `https://<domain>/mcp` (Streamable HTTP, no auth) |
| **Website** | `https://<domain>/` (landing page) |
| **Privacy policy** | `https://<domain>/privacy` |
| **Terms of service** | `https://<domain>/terms` |
| **Support** | `https://<domain>/support` → GitHub Issues |
| **Demo recording** | `https://<domain>/demo.mp4` → 302 to `s3://danny-banhmi-public/demo/<name>-demo.mp4` (public read) |
| **Domain verification** | `/.well-known/openai-apps-challenge` — served live from `s3://danny-banhmi-public/challenge/<name>` (private prefix), read on every request |
| **Tool annotations** | all 5 tools set `readOnlyHint: true`, `openWorldHint: false`, `destructiveHint: false` explicitly |
| **Tool schemas** | typed input schemas, no `["null", X]` unions (strict-scanner safe) |

- **Demo upload (later):** `aws s3 cp <file> s3://danny-banhmi-public/demo/<name>-demo.mp4` — keys are
  `banhmi|laksa|rendang|kaya|tomyum|amok`-demo.mp4.
- **Challenge token (at submission):** portal issues a token →
  `printf '%s' '<token>' | aws s3 cp - s3://danny-banhmi-public/challenge/<name>` — live on the
  next request, no redeploy.
- **No auth = no demo credentials needed.** No app UI component = no CSP domain list.

## Listing fields (fill per jurisdiction)

| Field | Value |
|---|---|
| **Name** | product name (`banhmi`, `laksa`, …) — customer-facing, matches the landing page |
| **Category** | Research / Legal reference |
| **Short description** | landing `Tagline` (see `cmd/server/landing.go`) |
| **Long description** | landing `Description` + the evidence-only paragraph from `/llms.txt` |
| **Logo** | emoji logo PNG (product emoji: 🥖 🍜 🍛 🍞 🍲 🐟) |
| **Website / Support / Privacy / Terms** | the per-domain URLs above |
| **Countries** | worldwide (service is public, no per-country legal terms beyond `/terms`) |

## Test cases (submission form wants exactly 5 positive + 3 negative)

Use the jurisdiction's own language for queries (VN: Vietnamese; MY/SG/KH: English;
ID: Indonesian; TH: Thai). VN examples below — translate the pattern per country and reuse
each landing page's `Examples` list for phrasing.

**Positive (expected: ranked provisions with exact citations, validity badges, official source links):**

1. Ask about online-banking security duties → `search` returns 50/2024/TT-NHNN / 09/2020/TT-NHNN provisions with Điều/Khoản cites.
2. Ask about eKYC account opening → `search` returns 17/2024/TT-NHNN Điều 16 evidence.
3. Open one provision in full → `document` (citation filter + `include=["chunks"]`) returns the verbatim article + source URL.
4. Ask what the corpus covers → `corpus_status` reports version, doc/chunk counts, sources.
5. Ask a question whose top hit is repealed law → hits carry an Expired/repealed badge and the in-force successor ranks first.

**Negative (expected: graceful, honest behavior — no hallucinated evidence):**

1. Off-domain question (e.g. traffic fines) → search abstains / flags out-of-scope; agent is told the corpus is banking + tech law only.
2. Query in the wrong language (English against VN) → degraded-rankings warning path: guide/description instructs the agent to translate first.
3. Ask for a document that does not exist (fabricated số ký hiệu) → empty result + gaps report, not invented citations.

## Starter prompts (per listing, in the corpus language)

Use the landing page `Examples` (4 per jurisdiction, already realistic and scenario-based) verbatim.

## Submission prerequisites (account side, not code)

1. OpenAI Platform publisher identity **verified** (individual or business) — submissions with
   mismatched identity are rejected.
2. Submitter needs **Apps Management write access** in the org.
3. Output schemas are *recommended* (not required) — deferred; propose before building.
