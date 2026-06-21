# banhmi plan

Living roadmap and progress tracker. Architecture detail in [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md);
conventions and the canonical agent guide in [`CLAUDE.md`](CLAUDE.md). Last updated: 2026-06-21.

## Vision

A self-hostable platform that collects Vietnamese banking and technology regulation, turns it into a
clean, citable knowledge base, and **serves it as evidence over MCP** so bank teams' own agents get
**accurate answers with exact Điều/Khoản citations**. The hard, valuable part is the **data**: good data
+ any decent model = good answers; bad data = *confidently wrong legal answers*, which is worse than
nothing. So we build the data first.

**banhmi provides data + MCP; the user brings the model.** There is no built-in answer LLM. Hosted agents
(Claude.ai, ChatGPT, Gemini, Grok) connect to banhmi's MCP server and reason over the evidence themselves.
If a turnkey "ask" experience is ever wanted, it is a **separate microservice**, not part of this product.

## Jurisdiction #2 — Malaysia (`laksa`) [PROPOSED 2026-06-21]

Extend banhmi to **Malaysian banking digital/technology regulation** as a **second jurisdiction in the
same repo** (jurisdiction = config dimension; not a branch/fork). VN production untouched. Full design +
verified source research in [`docs/design/MALAYSIA.md`](docs/design/MALAYSIA.md).

- **Endpoint:** `laksa.danny.vn` (food-themed, parallel to *bánh mì*).
- **Sources (verified live 2026-06-21):** **BNM** `bnm.gov.my` (primary tech regs) · **AGC LOM**
  `lom.agc.gov.my` (Acts + validity/relations + the P.U. gazette feed — `federalgazette.agc.gov.my` is
  dead) · **SC** `sc.com.my` (capital-market fintech, scoped). 3 sources vs VN's 4.
- **Language:** one main language per country — **VN Vietnamese, MY English** — index/serve/search in it
  only; native text is the binding ground truth; **banhmi never translates** (translation is the user's job).
- **Main new work:** a born-digital **PDF → Section/Subsection structure parser** (LOM has no HTML
  provision tree like VBPL); generalize the Điều/Khoản citation model to per-country **native** labels
  (MY: Part/Section).
- **Reused unchanged:** Medallion pipeline · MarkItDown+OCR · BGE-M3 + pgvector · MCP tools · deploy shape.
- **Status:** design only — no code. **PDF-structure parser SPIKED & proven 2026-06-21** on FSA 2013
  (17/17 Parts, 281/281 sections, 0 gaps). **Jurisdiction seam designed & VN-safe** via a 3-part code
  audit (share-common/customize; separate `laksa` DB on the same RDS; only DDL = relaxing one silver CHECK;
  `gold.chunk` untouched). **BNM fetch proven** (AWS WAF token mint-once → reuse in plain HTTP).
  **Golden citation regression guard landed**; the English/`provision_level` experiment was **reverted**
  per the one-language-per-country policy. **MY build started:** step 1 = the **MY structure parser**
  (`ParseMalaysianAct`, tested) and step 2 = the **jurisdiction config dimension** (`BANHMI_JURISDICTION`
  default `vn` + per-jurisdiction `buildSources`) are committed (VN unchanged). **Phase A (get documents):**
  the **agclom source is built + live-validated** (885 Acts; FSA 758 → dates + 59 P.U. relations + a 2.6 MB
  PDF; plain HTTP), **wired into `buildSources(my)`**, and **jurisdiction-aware scope + the MY
  vocabulary** (64 EN terms) are done — **live-validated: the MY vocab selects 21/885 federal Acts**, the
  exact banking/tech set. **PHASE A DONE & VALIDATED (2026-06-21):** ran the real Temporal pipeline against
  a local `laksa` DB with `BANHMI_JURISDICTION=my` — `discover agclom` (885 → scope-filtered to **21
  in-scope Acts** → enqueued) then `fetch agclom` (**18/21 born-digital Act PDFs downloaded into laksa
  bronze**; FSA 2013, IFSA 2013, CBMA 2009, PDPA 2010, Cyber Security 2024, Computer Crimes, Digital
  Signature, MSBA, AMLA, DFIA, Credit Reporting, Comms & Multimedia…). 3 repealed/no-reprint Acts
  (Payment Systems 2003, Comms Commission, e-Commerce 2006) landed as metadata only — follow-up.
  **sc source DONE & validated** (2026-06-21): SC tech/cyber/digital-asset guidelines, in-scope by
  construction (keyword-bypass) — discover=24/fetch 24 PDFs into laksa bronze. **laksa now holds 45 MY
  docs / 42 files.** Remaining source: **bnm** (the primary regulator — RMiT/e-KYC/e-money; needs the
  headless `chromedp` WAF mint + chromium in the worker container). Then **Phases B–F** (extract →
  normalize [wire ParseMalaysianAct] → index → serve → deploy: native provision labels + silver-CHECK
  relax, per-jurisdiction MCP brief, `laksa` DB on the same RDS + Cloud Run). All in MALAYSIA.md.

## The target — INPUT first, then deploy MCP + DB to the cloud

Two flows (see [`docs/design/PIPELINE.md`](docs/design/PIPELINE.md)):

- **INPUT** (crawl → fetch → extract → normalize → index): build a *trustworthy corpus* in the DB.
  **This is the product and the hard part.**
- **OUTPUT** (the MCP evidence service): retrieval + the MCP tools (`guide`, `corpus_status`,
  `quality_gaps`, `search`, `document`) that expose citations, validity, relations, provenance, and gaps.
  **No answer generation.**

**MVP1 deployment shape (split-cloud, scale-to-zero) — decided 2026-05-31, SHIPPED 2026-06-01:**

- **Worker runs locally** (uses the local **Intel Arc GPU** for extract / embed / index) and **writes the
  corpus over TLS to managed Postgres** — **AWS RDS PostgreSQL** (PG17, pgvector + HNSW; `ap-southeast-1`
  Singapore). *(Originally planned on **Neon** serverless; switched at deploy time — its 512 MB free cap
  overflowed mid-restore. See [Track B — DEPLOYED](#track-b--deployed--live).)*
- **The MCP server runs on GCP Cloud Run** as one scale-to-zero service that **embeds queries in-process**
  via the OpenVINO Runtime running the exact index BGE-M3 INT8 model (`-tags openvino`) — **no OVMS, no
  sidecar, single binary** on distroless/cc. It serves remote MCP over **Streamable HTTP** at `/mcp`;
  hosted agents (Claude.ai/ChatGPT/Gemini/Grok) connect over remote MCP. *(The original plan was an OVMS
  CPU embedder sidecar; the in-process OpenVINO build replaced it — one image, exact OVMS parity.)*
- **Public endpoint** is **`https://banhmi.danny.vn/mcp`** via **Firebase Hosting** (free Spark) routing to
  Cloud Run. *(Chosen over a Cloud Run domain mapping — preview/not-production — and an external HTTPS LB
  — ~$18/mo floor — both of which would have broken the free/scale-to-zero shape.)*
- **Why this split:** Cloud Run scales to zero and wakes on demand → ~$0 idle there; RDS carries a small
  idle cost (it is not scale-to-zero — the Neon swap; the $5/mo budget covers it). Cloud Run is the most
  mature wake-on-request container platform. **Co-locate the regions** (RDS `aws-ap-southeast-1` Singapore
  ↔ Cloud Run `asia-southeast1` Singapore) so cross-cloud query latency stays low.
- **Retrieval is vector-only** (pgvector). No ParadeDB/`pg_search`; our eval shows GPU-vector already
  beats BM25 and hybrid, so dropping BM25 in the cloud costs little.
- **Sequence: validate all dev locally first, then deploy** — done in that order (local gate MET, then
  Track B shipped). See Track B below.

> **Status convention:** "coded" = code written + tests; "validated" = checked on real SBV documents.
> Most of the spine is **coded but not validated** — validation *is* the MVP1 work.

**Latest (2026-05-31): whole-pipeline `RunAll` workflow + streaming Kaggle batch.** Added `RunAll` — a
one-shot/scheduled orchestrator (child workflows) that runs discover→fetch→extract→normalize→backfill
(loop, ≤3 rounds) → OCR → index → embed; operators un-pause the single `pipeline:run-all` schedule.
`EmbedAll`/`OcrAll` now **stream** input (DB cursor → upload JSONL) and output (downloaded JSONL →
per-row upsert), so memory is bounded regardless of corpus size — fixes an OOM that killed the worker
mid-embed on a 60 k-chunk cold build. Per-run Kaggle kernel slugs; `KAGGLE_API_TOKEN` now read from
config. **Cold build re-validated:** RunAll built 559 docs / 60,851 chunks; the streaming `EmbedAll`
then embedded **60,851/60,851 (0 missing)** on Kaggle with no OOM/heartbeat-timeout. The local corpus was
subsequently dump/restored into the cloud DB (no re-crawl) and the Cloud Run MCP image pushed — see
[Track B — DEPLOYED](#track-b--deployed--live).

## ✅ Priority 0 — Ingest flow fixes DONE (full-flow audit 2026-05-31)

A full-flow audit (Discover → Fetch → Extract → Normalize → Index + **Relation Backfill**) found the
corpus **silently misses authoritative gazette text**. Trigger: `42/2016/TT-BTTTT` + `63/2023/NĐ-CP`
ended up with only placeholder OCR though congbao has the real documents. **This is INPUT-quality work
and comes before Track A validation and Track B deploy.** Cross-source merge, binding-text selection,
and stub detection are correct — the breakage is upstream (search recall + drain orchestration).
Working detail: `/tmp/banhmi-ingest-audit.md`.

Root-cause chain: vbpl copy is a placeholder stub → stub gate fires → congbao fallback runs → (C1) the
congbao search misses the real doc, or (H1) it finds it but the fetch is never drained → meanwhile (M2)
OCR runs on the stub PDF and writes a useless 27-char row.

Fix in order:

| # | Sev | Issue | Where |
|---|-----|-------|-------|
| C1 | CRIT | congbao `SearchByNumber` misses real docs (page-1-only over a fuzzy ranker; exact số ký hiệu not in top 100) | `pkg/ingest/congbao/search.go:71,97-108` |
| H1 | HIGH | backfill/fallback discoveries never auto-drained (no stage re-runs Fetch; dev `-fetch` one-shot; recurring schedule paused) | `process_activities.go:139-167`, `relation_backfill.go:294-306`, `cmd/worker/main.go`, `pipeline.go:228` |
| H2 | HIGH | congbao search `file_extension` is a garbage path → unclassifiable file | `pkg/ingest/congbao/search.go:161-191` |
| H3 | HIGH | congbao search download URL may be unusable (raw `duong_dan` vs token-gated stream) | `pkg/ingest/congbao/search.go:182` |
| M1 | MED | relation backfill vbpl-only + stub-blind (never tries congbao; enqueues a stub vbpl copy) | `pkg/pipeline/relation_backfill.go:123-124,270` |
| M2 | MED | `OcrAll` OCRs known placeholder PDFs (the 27-char "đang cập nhật" rows) | `pkg/pipeline/ocr_all.go:259-280` |
| M3 | MED | `doc_key` normalization diverges Go vs SQL on Unicode whitespace | `process_activities.go:1113` vs SQL `:716,783,830` |
| M4 | MED | config keyword typo `giao dich điện tử` (phantom vbpl cursor) | `deploy/seed/discovery_keyword.csv` |

**STATUS (2026-05-31): all fixed + committed.** C1, H2, H1, M2, M3, M4 implemented; H3 was a non-issue
(the search `duong_dan` downloads directly); M1 is mitigated by C1+H1 (the fallback bridges stub vbpl
copies to congbao once the drain runs). **Validated end-to-end** on the two trigger docs: re-extracting
the vbpl stubs now fires the congbao fallback (C1 found `42/2016` → congbao `22071`, previously
impossible), and fetch→extract→normalize landed **real born-digital binding text** — `42/2016` 18,402
chars / 19 sections, `63/2023` 178,951 chars / 738 sections — cross-source merged (congbao+vbpl aliases),
**no OCR**. The old placeholder OCR rows remain but are correctly non-binding.

**Full recovery — DONE (2026-05-31).** `-drain` converged in 2 rounds with nothing new to fetch: the
backfill found **0 candidates** because the ~478 "unresolved relation targets" are **level-2** (their
source doc was itself relation-pulled; `824/825` have `provenance='relation'`). They are excluded by the
deliberate **one-level-deep** limit (`fd.provenance <> 'relation'`) — a crawl-**depth boundary**, not a
bug or a fetchable backlog. Going deeper is a scope decision (risks cascading toward the whole legal
corpus), not a fix. The two recovered docs were indexed + embedded on the local OVMS BGE-M3:
`42/2016` 23 chunks, `63/2023` 410 chunks — both searchable. Corpus now **572 docs / 62,350 chunks /
62,350 embeddings (100%)**. M4 applied: re-seeded (`discovery_keyword`=22) and dropped the phantom cursor.

**Open scope decision (not a bug):** whether to follow relations beyond one level (a configurable crawl
depth) to pull the 478 level-2 targets — deferred; would expand the corpus materially.

## ▶ Next — two tracks (Track A validated → Track B now open)

**Readiness verdict (2026-05-31): the local-validation gate is MET — Track B (cloud deploy) may open.** INPUT and
OUTPUT are both validated on real documents (see the [deploy-readiness checklist](#cloud-deployment-aws-rds--gcp-cloud-run)).
"Move to deploy" = yes. The MCP endpoint is **public by design** (any agent may connect, no key) — abuse
is bounded by per-IP rate limiting + a request body cap (in the server) plus Cloud Run `--max-instances`,
not by a key. API-key auth is **opt-in** (`BANHMI_MCP_API_KEY`) if access ever needs restricting.

**Track A — validate the dev system locally (DONE / validated 2026-05-31):**

1. ✅ **Done (2026-05-30) — evidence-only surface:** removed the `ask` MCP tool, `pkg/llm`,
   `pkg/rag/answer`, and the OpenAI-chat + web "ask" surfaces; repurposed `cmd/server` to serve MCP over
   **Streamable HTTP** at `/mcp`; made the embedder **mandatory** (vector-only). Verified locally
   (HTTP `initialize` + the five evidence tools; full test suite green).
2. ✅ **Done (2026-05-30) — clause-level currency as evidence:** `document` returns `incoming_amendments[]`
   (verbatim amending clauses + eff date + position); the connecting agent judges currency. banhmi does not
   derive section-validity (`section_validity_rows = 0` by design).
3. ✅ **Done (2026-05-31) — corpus cleaned + retrieval validated:** eval recall@k/MRR@k/current-law 100%;
   the Priority-0 ingest fixes recovered authoritative gazette text (e.g. `42/2016`, `63/2023`); the "stub
   relation targets" are now understood as the deliberate **one-level-deep** boundary (478 level-2 targets),
   not a defect. Corpus: **572 docs / 62,350 chunks / 62,350 embeddings (100%) / 100% validity coverage**.
4. ✅ **Done (2026-05-30):** validated the evidence contract over the HTTP `/mcp` endpoint — the remote
   deploy surface works.
5. ✅ **Extract OCR redesign — coded + validated (2026-05-30):** EasyOCR (`vi`) replaces Tesseract; OCR
   runs as a batch (`OcrAll`) local-CPU or Kaggle-GPU and Extract defers gate-failed scans. **End-to-end
   validated:** `-ocr-all -force` over 2 real SBV scans on Kaggle GPU completed clean (`processed=2
   failed=0`), wrote `ocr_extractive` with readable Vietnamese + correct diacritics (`2345/QĐ-NHNN`,
   `2872/QĐ-NHNN`; confidence 0.77–0.81; minor stamp/number noise only). **OCR is good; the OCR agent did
   good work.** See [EXTRACTION](docs/design/EXTRACTION.md).

**Track B — deploy DB + MCP (Cloud Run) — SHIPPED 2026-06-01.** What actually went out (final shape; the
ordered planning steps and the two deviations from the original Neon + OVMS-sidecar plan are folded into
the DEPLOYED block below and [Cloud deployment](#cloud-deployment-aws-rds--gcp-cloud-run)): managed
Postgres (**AWS RDS**) + pgvector, migrations applied over TLS, corpus dump/restored, Cloud Run MCP with
an in-process OpenVINO embedder behind a Firebase-Hosting custom domain, secrets via the provider.

<a id="track-b--deployed--live"></a>
**✅ Track B — DEPLOYED & LIVE (2026-06-01).** Split-cloud MVP1 is up: **worker local → corpus in AWS RDS
PostgreSQL (`ap-southeast-1`, PG17, pgvector/HNSW) → MCP on GCP Cloud Run (`asia-southeast1`, in-process
OpenVINO BGE-M3 for query embedding) → Firebase Hosting custom domain**. Live MCP at
**`https://banhmi.danny.vn/mcp`** (Firebase free Spark → Cloud Run; the `run.app` URL also serves). RDS
Postgres is reachable from `0.0.0.0/0` but **TLS-required (`rds.force_ssl=1`) + password-gated** (public
legal corpus); the **Cloud Run NAT was removed 2026-06-13** (it billed ~$35/mo and defeated scale-to-zero),
so GCP idle cost is now ~$0. Cost guard: **$5/mo GCP budget alert** + Cloud Run `max-instances=3`
(scale-to-zero). Deviations from the original plan, with reasons: **AWS RDS
instead of Neon** (Neon's 512 MB free cap overflowed mid-restore); **Firebase Hosting instead of a Cloud
Run domain mapping** (the mapping is preview/not-production, and an external ALB costs ~$18/mo, so the
free Firebase Spark plan routes the custom domain → Cloud Run, SSE confirmed working).

MCP evidence contract enhanced (deployed, no DB change / no re-embed): official **source links**
(`source`/`source_url` on hits, all `sources[]` on `document`), a ready-to-paste **`cite`**, English
**validity/quality badges** (`status_label`/`quality`), a document **`timeline`** (issued → effective →
amended/replaced → expired), and an **English-first agent contract** — English tool/param descriptions, a
server-level `instructions` brief (trust stance + **live coverage counts** + when/how to use + examples),
and read-only tool annotations so hosts can auto-approve. Optional **query-time filters** scope `search`:
**`as_of`** (point-in-time validity), **issued-date range**, and **issuer / doc-type facets** — gated so
the default path is byte-for-byte unchanged. Legal data stays Vietnamese verbatim; **content + source
links only, never files**. Hits also carry the **issued date**, and a derived **`validity.warning`** flags
source-side date inconsistencies (e.g. `effective_from` earlier than `issued_date`, a VBPL data-entry
error like `77/2025/TT-NHNN`) — surfaced, never auto-corrected (needs redeploy to go live). See
[RAG](docs/design/RAG.md#current-shape).

**Do not reopen these decisions:** **EasyOCR (`vi`)** is the OCR engine — it replaced OCRmyPDF/Tesseract
after a bake-off on real SBV scans (better diacritics, complete transcription, no hallucination; VLM
doc-parsers were rejected for dropping regions + plausible-wrong substitutions). BGE-M3 (GPU, OpenVINO) is
the pgvector embedder; Gemma 4 E4B OCR enhancement is MVP2; model-search is stopped. The extraction
cascade stays DOCX/HTML/DOC text first, then OCR as the last fallback — OCR now runs as a **batch**
(`OcrAll`, local-CPU or Kaggle-GPU per `ocr.engine`), mirroring bulk embedding.

---

## ✅ MVP1 completion pass — 2026-06-10 (correctness audit → fix wave → re-sync + redeploy)

A live-corpus review found one P0 accuracy bug plus several open MVP1 items; all fixed, validated
locally (eval + Haiku-over-MCP), then RDS re-synced and Cloud Run redeployed.

**Identity (P0).** `doc_key` was số-only, so distinct documents sharing a số merged — live doc 219
served the **Luật Giao dịch điện tử** text under the title "Nghị quyết 51/2005/QH11 Về nhiệm vụ năm
2006" (two VBPL pages merged). Identity is now **`<TYPE>|<NUMBER>`** (normalized loại văn bản +
số ký hiệu; un-numbered docs fall back to `source:external_id` — the VBPL "KHÔNG SỐ" sentinel is not an
identity). Audit found 8 collision groups: 3 real Luật/Nghị quyết pairs (41/2009, 42/2009, 51/2005), a
**6-way merge of un-numbered docs** (Hiến pháp 2013 + old laws all keyed "KHÔNG SỐ"), 2 English
"Bản dịch văn bản" renditions overwriting Vietnamese docs (now discovery-excluded via config), and 2
sbv_hanoi rows whose portal *category* ("Pháp luật ngân hàng") was stored as doc_type (parser now
validates against known loại văn bản). Number-only references resolve only while exactly one document
carries the number; ambiguous bare-số `document` lookups prefer the primary/indexed doc and disclose
`also_matches`. Data migration: 6 merged docs deleted + rebuilt from bronze, 553 keys recomputed in place.

**Scope gate (INPUT).** Relation-pulled documents that fail the config scope vocabulary are now
**`relation_context`** (new `silver.document.index_class`): text + relations stay served (document tool,
verbatim amendment clauses), but no chunks enter the searchable corpus. This dropped the out-of-domain
bulk (environment/housing/tax/fisheries/aviation law) — chunks went **61,211 → ~17.8k** while every
in-scope relation target (Luật An ninh mạng, Luật ATTT, NĐ 85/2016, NĐ 52/2024…) stayed primary.
Disclosed via `corpus_status.relation_context_unindexed` + note. Two VBPL title typos required
data-driven vocab additions ("thông tin khách hành", "thanh toán không dung tiền mặt").

**OCR floor (decision).** Documents with **no binding text at all** now serve their best *usable*
non-binding transcription: Normalize falls back to it (same quality bar; gate-failed extractions stay
rejected), so 2345/QĐ-NHNN, 2872, 2866, 631, 59/TT-NHNN are searchable — every hit badged
non-binding/needs-review through text provenance; `is_binding` stays false. 8 docs whose OCR is unusable
remain unindexed (disclosed).

**Phụ lục (chunker).** The parser now emits root-level `phuluc` sections (tight discriminator:
whole-line label or ALL-CAPS heading); appendix content chunks under "Phụ lục N" and Điều nested in an
attached Quy chế cite "Phụ lục X, Điều N". QĐ 2345's biometric-threshold tables are now exact, citable
chunks. Short-but-real provisions stay by design (labelOnlyChunk filters junk).

**Validity honesty.** Sources that provide **no status** no longer default to CHL/in_force — class
**`unknown`** ("Validity unknown — verify against the official source"), excluded from the current-law
pass, surfaced badged in the secondary pass. This stopped the repealed 2345/QĐ-NHNN from being served
"In force". A status-less observation can never downgrade a real status from another source.
**Drained fetches:** 117/2018/NĐ-CP (customer-info secrecy; in scope, indexed), 94/2025/NĐ-CP (sandbox —
real text replaced OCR-only), 14/2019 + 59/2020 (correctly relation_context).

**Serving.** Non-current pass: max 1 hit/document and ≤ min(3, top_k). Relations are listed on the first
hit of each document only (payload dedup). `related_hits.bm25_rank/bm25_score` → `rank` (vector order;
dead score dropped). The abstain floor now gates on real **cosine similarity** (RRF scores are
rank-derived) — `retrieve.abstain.min_score=0.3` seeded. Eval's current-law metric excludes the
deliberately-badged trailing non-current run (a non-current hit *above* current law still counts as a leak).

**Corpus truth (reconciliation).** The deployed RDS corpus had drifted from PLAN's "572 docs / 62,350
chunks" claim (RDS held a different snapshot, with only 11 ingest rows — pipeline state wasn't restored).
RDS held zero documents local lacked, so the validated local corpus replaced it wholesale (dump/restore,
including ingest state). **Current corpus: 570 docs · 283 indexed (primary) · ~17.8k chunks · 100%
embedded · validity classes incl. 6 `unknown` · eval recall@k 100% / MRR@k 89.1% / current-law 100% /
abstention 100%.** Hygiene: eval defaults to vector; dead config fields removed; silver `doc_number`
display-cleaned ("18 /2018", "số:"-prefix); KHÔNG SỐ stubs labeled by title; GitHub Actions CI
(build/vet/test/lint) added.

---

## ✅ vanban.chinhphu.vn added as source #2 (2026-06-19) — built + live-validated

**Trigger.** A recall miss: `134/2025/QH15` (Luật Trí tuệ nhân tạo, issued 2025-12-10) is absent from the
corpus. Root-caused (live-verified, not assumed): **vbpl.vn has not published it** — a vbpl title/number
search returns `total=0`, while the same-session `116/2025/QH15` (Luật An ninh mạng, also 2025-12-10) *is*
in vbpl and in our corpus. So it is not staleness, a missing keyword (`trí tuệ nhân tạo` has shipped since
commit 1), or a watermark bug — it is **upstream source lag**. The standalone AI Law *is* on
`vanban.chinhphu.vn` today (with its signed file). Compounding: `trí tuệ nhân tạo` was a `discovery_keyword`
but **not** a `scope_term`, so the local-filter feeds (congbao/SBV-Hanoi) would drop it too, and AI queries
`abstain` as `out_of_domain`.

**Decision (maintainer, 2026-06-19).** Add `vanban.chinhphu.vn` (Government "Hệ thống văn bản", 47k central
VBQPPL, freshest feed) as **source #2 after vbpl** — current central-law **discovery + authoritative file +
metadata**, *not* structure/relations (vbpl stays authoritative). Approved choices: **keep congbao** (runs
alongside, not replaced); **cold-start backfill central issuers 2018→now**; **add AI `scope_term`s**
(`trí tuệ nhân tạo`, `hệ thống trí tuệ nhân tạo`) — which also fixes the query-side abstain on the AI text we
already hold (`71/2025/QH15` Chương IV).

**Crawl mechanism (live-verified, not assumed).** The site is ASP.NET WebForms with a **hard 50-row/page
cap** (`drdRecordPerPage=500` ignored) and **no RSS/API**. The issuer-filtered search returns one page then
**does not paginate** (page 2 = empty); only the **unfiltered GridView `Page$N` postback** reproduces from a
plain HTTP client. So discovery is a keyword-less newest-first `Page$N` walk + `scope.Match` (the vanban
analogue of congbao RSS), not the issuer-scoped search first sketched. Spec:
[`docs/design/SOURCES.md`](docs/design/SOURCES.md#vanbanchinhphuvn--government-legal-database-văn-phòng-chính-phủ-source-2-after-vbpl).

**Footprint (built):** new `pkg/ingest/vanban/` (client/discover/detail/download + contract test, mirrors
`pkg/ingest/sbvhanoi/`); AI `scope_term`s added to `deploy/seed/scope_term.csv`; `vanban: enabled` in the
config sources; wired into `pkg/app` (`buildSources`), `RunAllParamsFromConfig`, `EnsureSchedules`, and the
worker discover/fetch source lists. **Extract/Normalize/Index unchanged.** No `config.issuer_code` rows (the
unfiltered walk needs no issuer filter). **Live-validated**: the Go `Discover` parsed real rows, `FetchDetail`
returned `134/2025/QH15` (Luật Trí tuệ nhân tạo, effective 2026-03-01, Quốc hội), and `Download` streamed its
1,060,110-byte signed PDF.

**Local pipeline run (2026-06-19, dev stack).** Re-seeded config (`scope_term` 73→75). Real Temporal
`Discover` walked the vanban list (pagination via the `Page$N` postback — fixed a bug where the served HTML
encodes `__doPostBack` quotes as `&#39;`) back to 2025-12-06 and enqueued **26 in-scope** docs; the AI Law
landed in `ingest.fetch_doc` (in_scope) matched via the new **`trí tuệ nhân tạo`** scope term, fetched to
`silver.document` id 642. **Finding:** vanban's authoritative file for brand-new laws is the **signed scan**
(`luat134.signed.pdf` — 20 pages, image-only; pdftotext yields only the e-signature stamp), so it correctly
routes to the **OCR floor** and serves as badged non-binding evidence until vbpl supplies a born-digital
DOCX. Not a bug — the designed scan path.

**RDS deployment — DONE & verified live (2026-06-20).** Pointed `BANHMI_DATABASE_*` at the managed Postgres
(password from the deployment secret store) and ran the backfill: re-seed config (AI scope
terms) → `-discover vanban` (bounded to 2025-09→now: **35 in-scope docs**, incl. the whole AI cluster) →
`-drain` → `-ocr-all` (Kaggle: 14 scans, incl. the AI Law's signed scan) → `-normalize-all` → `-index-all` →
`-embed-all` (Kaggle). **Corpus 568→586 docs (18 new), 298 indexed, 20,373 chunks all embedded.** Verified
via MCP: `document(134/2025/QH15)` and `search("…trí tuệ nhân tạo rủi ro cao")` both return the **AI Law +
its decree `142/2026/NĐ-CP`** (top hits, Điều 9 / Điều 8), badged non-binding/needs-review, `in_domain:true`
(no more abstain).

**Two fixes landed during the run:**
1. **Normalize selector (`ListFetchDocIDsNeedingNormalizeAfter`):** a scan normalized as textless during the
   pre-OCR drain got a doc-level `validity_period` (status unknown), so the selector treated it as done and
   never re-normalized it after OcrAll wrote the OCR text. Added a predicate: also select docs that have
   non-empty `document_text` but **no `document_section`** (self-clears once sections exist). Validated live.
2. **AI `scope_term`s** seeded to RDS — fixes the query-side `out_of_domain` abstain on AI content.

**Lesson (cost paid):** mid-run I ran `normalize-all -force` and killed it; the partial pass re-sectioned
much of the corpus, orphaning chunks → forced a full re-index + **full re-embed** (cascade-deleted all
embeddings via the `chunk_embedding`→`chunk` FK). Recoverable (the streaming `EmbedAll` restored all 20,373),
but **don't `-force` whole-corpus stages against the live cross-region DB** — use the targeted selectors.
Follow-up: `normalize-all`/`index-all` over the high-latency RDS link still need per-doc draining for the
largest backlog (workflow throughput), and the selector that picks the lowest-id fetch_doc per document can
pick a textless source observation — both worth hardening.

---

## MVP1 — INPUT (the corpus)

| Stage | State | What's actually left |
|-------|-------|----------------------|
| **Discover** (congbao + vbpl + sbv_hanoi; **+ vanban in design**, see 2026-06-19) | coded; SBV Hanoi runs after VBPL, skips duplicate `Số/Kí hiệu`, uses the shared discovery-keyword filter | build the vanban source #2 (closes the vbpl-lag recall gap); measure scope precision/recall on real results; tune `config` vocab; trim out-of-domain docs pulled in by relation fetch |
| **Fetch** (downloads) | coded; does not start Extract; VBPL `/diagram` relation fetch backfilled; relation wave mostly drained: 271 relation fetch docs, 269 complete | inspect the 2 not-complete relation docs; validate multi-file docs and source metadata refresh |
| **Extract — DOCX/HTML/DOC** | MarkItDown path coded; UTF-8 HTML + empty-shell/mojibake guards; legacy `.doc` via LibreOffice PDF bridge; born-digital `needs_review` blocks `original_scan` OCR replacement | validate fidelity on real docs: tables, diacritics, clause markers |
| **Extract — PDF born-digital** | MarkItDown + gate + OCR fallback coded | validate across real congbao/vbpl PDFs; gate failures must route correctly |
| **Extract — OCR** (EasyOCR, batched) | **coded + validated (2026-05-30):** EasyOCR (`vi`) replaces OCRmyPDF/Tesseract; `OcrAll` workflow/activity + Kaggle kernel wired; Extract defers gate-failed scans (`needs_review`); `-ocr-all -force` validated end-to-end on 2 real SBV scans (Kaggle GPU, `processed=2 failed=0`, readable Vietnamese `ocr_extractive` @ 0.77–0.81 conf) | re-OCR the full 13-scan backlog during the re-crawl; add a kagglebatch fake-server test |
| **Normalize** (Điều/Khoản tree) | DB dry-run passed on 261 binding texts; VBPL tree first, quote-aware text fallback | keep sampling hard amendment docs and old outline-only docs |
| **Index** (chunks) | **re-crawled 2026-05-31: 61,917 chunks + 61,917 BGE-M3 embeddings (100%) across 555 indexed docs**, embedded inline on the local OVMS GPU (Kaggle bulk offload bypassed — see OCR/Kaggle note); `IndexAll` only enumerates normalized docs missing chunks unless `-force` | appendix/form folding |
| **Validity — doc-level** | first live pass validated | `CCHL` maps to `not_yet` |
| **Validity — clause-level** | **surfaced as evidence (2026-05-30)** | `document` returns `incoming_amendments[]` verbatim (amending doc + eff date + position + text); the agent judges currency. `section_validity_rows = 0` is intentional — banhmi does not derive legal effect |
| **Relations** | coded; 2,445 promoted structured relation rows drive the confirmed graph; source-provided targets use exact `source:external_id` ref keys | VBPL `references[]` + `/diagram` confirmed; text/fallback links stay `weak_relation`. 436 unresolved/stub relation targets are noise to reduce |
| `config` (scope vocab, issuers, keywords) | done | seeded + read from DB; scope is keyword/issuer based, not doc-number-anchor based |
| DB schema (5 schemas / 27 tables, Atlas migrations) | done | forward-compatible |

**INPUT is "done" when** a real SBV digital/tech corpus sits in the DB with *validated* extraction
fidelity, correct Điều/Khoản structure, and trustworthy validity (incl. clause-level) + relations.

---

## OUTPUT — the MCP evidence service

The serving surface is **not "parked"** — it is the MVP1 output. It exposes evidence, never answers.

| Piece | State | Note |
|-------|-------|------|
| Index → `gold.chunk` | coded; local chunk pass completed | non-binding/OCR-only text stays in `silver.document_text` but is not answerable |
| Embeddings → `gold.chunk_embedding` | coded; **GPU OpenVINO BGE-M3 INT8 on the Intel Arc** — **required** | query embedding ~8.5 ms; the embedder is mandatory — production retrieval is vector-only (BM25 is eval-only) |
| Bulk embedding via Kaggle GPU (optional) | **coded + validated (2026-05-30)**: full 51,627-chunk reindex on 2× Tesla T4 in < 2 min GPU, ~0.998 cosine-aligned with local OVMS | `embed.engine=auto/local/kaggle` picks only the **bulk** engine; query-time stays local OVMS. Auth = `KAGGLE_API_TOKEN`. See [`docs/design/RAG.md`](docs/design/RAG.md#kaggle-batch-embedding-optional-bulk-engine) |
| Retrieval (vector-first + current-law filter) | coded; default = BGE-M3 vector; BM25/hybrid kept for local eval only | current filter is document-level; must surface validity uncertainty until clause relations are complete |
| **MCP server** (`pkg/mcp`) | coded; tools `guide`, `corpus_status`, `quality_gaps`, `search`, `document` (evidence-only, no `ask`) | **the product surface.** stdio via `cmd/mcp`; HTTP via `cmd/server`. Validated via a Haiku agent over MCP (2026-05-30) and re-validated on the current 572-doc corpus (2026-05-31): `search_ready`, recovered docs retrievable, out-of-scope abstains |
| **Streamable-HTTP MCP transport** | **done — mounted in `cmd/server` at `/mcp`** (SDK `StreamableHTTPHandler`) | the remote surface hosted agents (Claude.ai/ChatGPT/Gemini/Grok) connect to; verified locally (`initialize` + 5 tools) |
| Eval (`cmd/eval`) | coded; retrieval-only, scores recall@k/MRR@k (no answer model) | **grown to 18 cases (2026-05-31)** spanning cybersecurity, e-signature, payment-intermediary, e-KYC, online-banking + 2 out-of-scope controls. Vector-mode (cloud serve path): **recall@k 100%, abstention 100%, MRR@k 89%** (fine-grained cases rank within top-k, not gamed to 1) |

**Dropped from the product (done 2026-05-30):** the answer LLM (Vertex AI), the `ask`/`pkg/rag/answer`
cite-verify+abstain path, `pkg/llm`, the OpenAI-compatible chat endpoint, and the web "ask" UI — all
removed. Answering is the user's model or a separate microservice.

---

<a id="cloud-deployment-aws-rds--gcp-cloud-run"></a>
## Cloud deployment (AWS RDS + GCP Cloud Run) — SHIPPED 2026-06-01

The MVP1 output, started **only after Track A was validated** and **now live** (see
[Track B — DEPLOYED](#track-b--deployed--live)). **Worker stays local; only DB + MCP went to the cloud.**
Topology decided 2026-05-31 (see [target deployment shape](#the-target--input-first-then-deploy-mcp--db-to-the-cloud));
two deviations landed at deploy time — **AWS RDS** replaced Neon and an **in-process OpenVINO** embedder
replaced the OVMS sidecar (reasons below).

**Deploy readiness (2026-05-31): GATE MET — Track B opened, then shipped 2026-06-01.**

- ✅ **INPUT validated:** 572 docs, 100% binding-or-disclosed, 62,350 chunks / 62,350 embeddings (100%),
  100% validity coverage, 3,475 confirmed relations; scope-tuned; ingest fixes proven on real docs.
- ✅ **OUTPUT validated (current corpus):** the 5 evidence-only tools work over stdio **and** HTTP
  (`cmd/server` at `/mcp`); `search_ready=true`; recovered docs are retrievable; out-of-scope abstains;
  gaps disclosed via `quality_gaps`. No answer LLM.
- ✅ **Retrieval:** vector-only BGE-M3 + pgvector HNSW; migrations ready (7 schemas + `atlas.sum`).

**Why this topology (cost-first; Cloud Run scales to zero, RDS carries a small idle cost):**

- **AWS RDS PostgreSQL for the DB** (`ap-southeast-1`, PG17, pgvector + HNSW, over TLS). *Originally planned
  on **Neon** serverless (0.5 GB free, scale-to-zero, ~1–2.5 s resume); switched at deploy time because
  Neon's 512 MB free cap **overflowed mid-restore** of the corpus. RDS is not scale-to-zero, so it carries
  a small idle cost, but it holds the full corpus and the cost guard ($5/mo budget alert) covers it.* SG
  allows `0.0.0.0/0` on 5432, **TLS-required (`rds.force_ssl=1`) + password-gated** — no Cloud Run NAT
  (removed 2026-06-13).
- **GCP Cloud Run for MCP + embedder** — most mature scale-to-zero + wake-on-request container platform,
  generous always-free monthly grant, no load-balancer floor. One service = **Go MCP** (ingress, `/mcp`)
  **with the BGE-M3 query embedder in-process** (OpenVINO Runtime, `-tags openvino`, single distroless/cc
  binary). *Originally an **OVMS BGE-M3 CPU sidecar**; replaced by the in-process build — one image, exact
  OVMS retrieval parity (recall 100% / MRR 89.1%), simpler to ship.* Chosen over AWS (no true
  scale-to-zero + graceful wake for containers post-App-Runner) and Azure Container Apps (less mature wake).
- **Custom domain** `https://banhmi.danny.vn/mcp` via **Firebase Hosting** (free Spark) in front of Cloud
  Run. *Chosen over a Cloud Run domain mapping (preview/not-production) and an external HTTPS LB (~$18/mo
  floor) — both would break the free/scale-to-zero shape; the `run.app` URL also serves.*
- **Co-locate regions:** RDS `aws-ap-southeast-1` (Singapore) ↔ Cloud Run `asia-southeast1` (Singapore)
  → low cross-cloud query latency. Query egress is tiny (text + small result sets), so cross-cloud cost
  is negligible.

**Track B work (ordered) — image + serving hardening coded + locally tested (2026-05-31); all steps SHIPPED 2026-06-01:**
1. ✅ **Public-facing hardening (coded + tested local).** `/mcp` is **public by default** (no key).
   Defenses (all verified): **cross-origin protection** (stdlib `http.CrossOriginProtection` — MCP
   Origin-validation; cross-site browser request → `403`, server-to-server agent → `200`) + the SDK's
   localhost DNS-rebinding guard; **per-IP rate limiting** (trusted last-XFF only when
   `BANHMI_TRUST_PROXY=true`, else `RemoteAddr` — no spoof bypass; 50 rps/100 burst default); **1 MiB body
   cap**; HTTP read/idle **timeouts**; `nosniff`. Scanners hitting unknown paths get a cheap `404` (no DB/
   embedder). **Opt-in** API-key auth via `BANHMI_MCP_API_KEY` (→ `401`). Plus `$PORT` + SIGTERM +
   env-driven DB/embedder so **one image works local and on Cloud Run**. Browser origins (if any legit web
   client) allowlist via `BANHMI_MCP_ALLOWED_ORIGINS`.
   - **Edge upgrade (later, not MVP):** Cloud Armor (WAF/OWASP + bot/reCAPTCHA + edge rate-limit + L3/4
     DDoS) requires an external HTTPS LB in front of Cloud Run (~$18/mo LB floor — breaks pure
     scale-to-zero), so defer until traffic/abuse justifies it; app-layer defenses + `--max-instances`
     cover MVP.
   - **Image scan (Trivy, 2026-05-31):** the **shipped** in-process-OpenVINO image (step 2) scans **16 CVEs
     (0 HIGH/0 CRITICAL — distroless base LOW/MED), Go binary 0** — the single distroless/cc binary carries
     no OS package surface. Binary CVEs fixed via Go **1.26.3** + **pgx 5.9.2**. Re-scan on each base bump.
     *(Historical, the rejected OVMS-sidecar image: combined server binary **0**; OVMS base **31 (0
     HIGH/CRITICAL — 26 MEDIUM, 5 LOW)** after baking `apt-get upgrade` (was 48), the rest upstream Ubuntu
     24.04 with no Canonical fix — one more reason the in-process build won.)*
2. ✅ **Cloud Run image — in-process OpenVINO embedder (built + tested local; CHOSEN).** The Go MCP server
   embeds queries itself via the **OpenVINO Runtime running the exact `Fede90` BGE-M3 INT8 model the index
   uses** (`pkg/rag/embed/ovembed`, `-tags openvino`, own minimal CGO over `libopenvino_c.so` + daulet
   tokenizer) — **no OVMS server, no sidecar, single binary**. `deploy/containerfiles/Containerfile.cloudrun.ovino`
   on **distroless/cc**: **739 MB, 16 CVEs (0 HIGH/0 CRITICAL — distroless base LOW/MED), Go binary 0**.
   - **Exact parity (eval):** in-process OpenVINO (CPU) query vs the OVMS-GPU index → recall@k **100%**,
     MRR@k **89.1%**, abstention 100% — **identical to the OVMS baseline**, GPU index untouched, no re-index.
   - **The ~0.9996 cosine (not 1.0) is device physics, not a bug** — proven: CPU-OV vs GPU-OV on the *same*
     model + *byte-identical* tokens = 0.99966 (the OVMS embedder runs on the Arc GPU; ours on CPU). It
     doesn't flip any golden ranking (recall stays 100%).
   - Selected by `BANHMI_EMBED_QUERY=openvino`; default stays OVMS HTTP (local). **Alternative:** an
     in-process **ONNX** path (`onnxembed`, `Containerfile.cloudrun.onnx`, 678 MB) needs no OpenVINO runtime
     but uses a 3rd-party INT8 (~0.98, MRR 80) — kept as a no-OpenVINO fallback. (Earlier OVMS-sidecar
     images `Containerfile.cloudrun` 1.87 GB / `Containerfile.server` 31 MB also kept.)
3. ✅ **AWS RDS + pgvector (done 2026-06-01)** — provisioned RDS PG17 (`ap-southeast-1`); applied migrations
   via `cmd/migrate` over TLS; seeded config. *(Provisioned on Neon first; moved to RDS after its free cap
   overflowed mid-restore.)*
4. ✅ **Corpus sync (done 2026-06-01)** — local corpus dump/restored into RDS (no re-crawl); idempotent, TLS.
5. ✅ **Cloud Run deploy (done 2026-06-01)** — pushed the in-process-OpenVINO image; scale-to-zero
   (min-instances 0) + `--max-instances=3`; `--memory 2Gi` (OpenVINO + the 570 MB model). **Deploy env:**
   `BANHMI_DATABASE_HOST/USER/NAME` + `BANHMI_DATABASE_SSLMODE=require` (RDS TLS — default is `disable` for
   the local DB) + `BANHMI_DATABASE_PASSWORD` (secret) + `BANHMI_EMBED_QUERY=openvino` (baked in the `ovino`
   image) + `BANHMI_TRUST_PROXY=true` (per-IP rate limiting reads the trusted last `X-Forwarded-For`) +
   optional `BANHMI_DATABASE_MAX_CONNS`; region `asia-southeast1`. Custom domain `banhmi.danny.vn` fronted
   by Firebase Hosting (free Spark) → Cloud Run.
6. ✅ **DB firewall (done 2026-06-01; revised 2026-06-13)** — originally RDS SG locked to the worker IP +
   Cloud Run NAT IP + `sslmode=require`. **2026-06-13:** opened SG to `0.0.0.0/0` on 5432 (still
   `rds.force_ssl=1` + password-gated) and deleted the Cloud Run NAT + router + static IP — the always-on
   NAT (~$35/mo) defeated scale-to-zero, so GCP idle cost is now ~$0. The MCP server is the only public
   HTTP component. Cost guard: $5/mo GCP budget alert.

**Pre-deploy code review (full codebase, 2026-05-31):** 4 parallel reviewers across serving / pipeline /
ingest+extract / base+cmd, then synthesized + verified. **Serving path (mcp/retrieve/server) and pipeline
are clean** — parameterized SQL, rows closed + `rows.Err()`, transactional + idempotent `ON CONFLICT`
upserts with checked returns, graceful shutdown, no panics in library code. Findings were concentrated in
the DB connection layer and **fixed** (commit): libpq DSN value-escaping (Neon passwords with special
chars), pgxpool tuning for serverless Postgres (connect timeout + idle reap + lifetime + health check +
env max-conns), worker/ingest SIGTERM. **Disclosed non-blocker follow-ups:** add `io.LimitReader` caps on
the crawler's `ReadAll`/JSON-decode bodies (worker-local, trusted gov sources); `Embed` ignores `ctx`
(serialized, low-QPS — fine for MVP); index embeddings are written outside the chunk tx (best-effort) — so
the populate runbook must run `IndexAll`/`EmbedAll` to reconcile.

**Pre-deploy polish — DONE (2026-05-31):** the `abstain=true`+hits semantics is now documented in the
`guide` tool's evidence contract and the `abstain` field schema (abstain marks a blocking gap, not wrong
hits; read `gaps[].kind` — `out_of_domain`/`no_evidence`/`low_confidence` — and judge). The eval golden
set grew 11→18 (vector-mode recall@k 100%, abstention 100%, MRR@k 89%). Remaining **disclosed** gaps (16
text gaps mostly out-of-scope Bộ TT&TT, 22 needs_review, OCR backlog) are acceptable for MVP1 — surfaced
via `quality_gaps`, never hidden.

| Concern | Shipped | Open |
|---------|---------|------|
| Database | **AWS RDS PostgreSQL** (PG17, `ap-southeast-1`) + **pgvector/HNSW**, vector-only (no `pg_search`); over TLS *(Neon was the original plan — switched, its 512 MB free cap overflowed mid-restore)* | not scale-to-zero → small idle cost (covered by the $5/mo budget); pooled-connection tuning under real load |
| MCP server | `cmd/server` Streamable-HTTP MCP on **Cloud Run** (`asia-southeast1`), scale-to-zero, wake-on-request | public by default; rate-limit + body cap + `--max-instances=3`; API-key opt-in; cold-start incl. model load |
| Query embedder | **in-process OpenVINO** running the exact index model (no sidecar; distroless/cc, 739 MB, 0 HIGH/CRIT) — **exact OVMS parity** (recall 100% / MRR 89.1%) *(replaced the originally-planned OVMS CPU sidecar)* | ~0.9996 cosine = CPU-vs-GPU INT8 (physics, doesn't change ranking) |
| Public endpoint | **`https://banhmi.danny.vn/mcp`** via **Firebase Hosting** (free Spark) → Cloud Run; `run.app` URL also serves | *(domain mapping / external LB rejected — preview-only / ~$18/mo floor)* |
| Corpus sync | local corpus dump/restored into RDS over TLS (no re-crawl) | egress, idempotent re-sync, RDS connection limits |
| Cost/ops | Cloud Run scales to zero; RDS small idle cost; co-located Singapore regions; $5/mo budget alert | cross-cloud latency + egress when traffic grows |

---

## RAG DB review — 2026-05-30

DB-only retrieval-quality review (`go run ./cmd/eval -retrieval-only -retrieval-mode <mode> -review`).
Not ready for answer-quality claims — and answer quality is now the **user's** model's job.

**GPU vs BM25 (this session, 8 golden cases):**

| Mode | recall@k | MRR@k | query embed |
|------|---------:|------:|-------------|
| BM25 (no GPU) | 50.0% | 33.3% | — |
| **Vector (GPU BGE-M3)** | **62.5%** | **69.0%** | ~8.5 ms |
| Hybrid (RRF) | 62.5% | 58.3% | ~8.5 ms |

**Finding:** GPU vector clearly improves accuracy (recall +12.5pp, MRR roughly doubles) at negligible
serve-time cost. Pure vector even **beats hybrid on ranking** here (BM25 drags RRF down) — vector-only is
the right default and the right AWS choice. (8 cases is too few to retire hybrid in code; grow the golden
set first.)

**Corpus / contract state:**

| Check | Result | What remains |
|-------|--------|--------------|
| Indexed corpus | **re-crawled 2026-05-31: 61,917 chunks + 61,917 embeddings (100%); 555 indexed docs** (local-GPU embed) | scope tuned (dropped over-broad `công nghệ thông tin`); cross-cutting docs come via relation backfill; 17-scan OCR backlog pending (Kaggle batch issue) |
| Strict retrieval eval | vector 91.7% recall@k, 100% MRR@k, 100% current-law precision, 100% abstention (11-case golden) | every retrieved case at rank 1; the one partial is ekyc (gets the primary doc, not the amendment cite) |
| Clause-level validity | surfaced as evidence: `document.incoming_amendments[]` (verbatim amending clauses) | `section_validity_rows = 0` is intentional — agent judges currency, banhmi does not derive it |
| OCR-only docs | 13 non-binding-only docs have 0 chunks | acquire/verify binding text for important ones (`2345/QĐ-NHNN`, `2872/QĐ-NHNN`) |
| Chunk quality | 0 blank citations/contexts; **5 mojibake chunks**; 3,722 chunks < 80 chars | fix mojibake, short chunks, appendix/form folding |
| Relations | 2,445 confirmed edges; **436 unresolved/stub targets**; 304 indexed targets | reduce stub noise; relation-aware retrieval must use graph evidence |
| MCP contract (Haiku, localhost) | works: all 3 real questions returned exact Điều/Khoản + validity + relations over MCP alone | bugs below |

**MCP contract bugs found by the Haiku test (2026-05-30):**

- `ask` abstained (no answer LLM) — ✅ the tool was removed; the surface is evidence-only.
- **No Điều/Khoản-level validity** — agents can cite a doc but can't tell which clause is current.
- `related_hits`: not a bug — the field is `omitempty` and was empty for the test queries; it returns
  correctly for documents with confirmed relations (verified over both stdio and HTTP).
- `search` payloads are heavy (~90 KB; full relation metadata per hit) — trim / paginate.
- No pagination on `search`/`document`; no citation byte-span anchors.

---

## Deferred / Dropped

- **Answer LLM / Vertex AI / OpenAI-compatible chat / web "ask" UI:** dropped from the product (user
  brings the model; a separate microservice can answer later).
- **Watchdog reconcile half:** Fetch lease recovery covers MVP1; the *resolve-references* half folds into
  Relations / second-wave fetch.
- **phapluat.gov.vn** source (SBV-only for MVP1).
- **Figure extraction/OCR; Gemma 4 E4B OCR enhancement:** MVP2 — reopen only on a concrete corpus need.
- **Reranker:** eval-only; local rerankers were worse than BGE-M3 vector-only; revisit after a larger
  golden set.
- `bronze.source_document_history` — dropped; temporal model lives in silver `validity_period` +
  `amendment_event`.

---

## Next steps (ordered) — the improvement plan

**Done (2026-05-30):** removed the answer LLM (`ask` tool, `pkg/llm`, `pkg/rag/answer`, OpenAI-chat, web
"ask" UI); made the embedder mandatory (vector-only); wired Streamable-HTTP MCP into `cmd/server` (`/mcp`)
and validated the evidence contract over HTTP; tightened the evidence pack (capped related-hit snippets
~90→55KB, added the guide drill-down, dropped the dead `ask` tool); fixed the scope gate (added
`online banking` + `an toàn, bảo mật` vocab) so in-domain banking-security queries no longer falsely abstain.

Every phase below is deterministic, uses no hardcoded document IDs, and is validated by the eval + a
Haiku sub-agent over localhost MCP before moving on.

**Phase 1 — Điểm-aware chunking (foundation; biggest "give agents what they want" win).** The section
tree already detects the full hierarchy Điều → Khoản → **Điểm** (32,412 điểm rows), but the chunker
(`pkg/pipeline/index_activities.go`) stops at Khoản and splits long Khoản into artificial `Đoạn`
paragraphs (9,692 chunks; only 83 cite điểm). Make it recurse to Điểm — emit điểm chunks cited
"Điều X, Khoản Y, điểm z"; `Đoạn`-split drops to a last resort for long leaf prose only.
**Done + re-indexed (2026-05-30):** điểm citations 83 → 12,602; `Đoạn` 9,692 → 8,170 (rest are genuine
long prose); 51,627 chunks, 100% embedded. Tradeoff: recall@k dipped on one broad golden case because
finer điểm outrank a coarse expectation — addressed by Phase 1.5 + the Phase 4 golden re-baseline.

**Phase 1.5 — Hierarchical retrieval (parent roll-up + level navigation).** **Coded (2026-05-30):**
match at the fine (Điểm) level, then collapse ranked hits sharing a parent provision to their best rep
(`retrieve.rollup_level` = khoan default | dieu | none) so sibling Điểm/Đoạn don't crowd the top-k;
over-fetch before roll-up; each hit carries `parent_citation` so agents open the full Điều/Khoản via the
document tool — evidence at every level. Roll-up logic is unit-tested; **eval validated (2026-05-30):**
vector recall@k 50% / mrr@k 66.7% / current-law-precision 100% / abstention 87.5% — no regression vs the
pre-roll-up baseline; the 3 remaining misses are golden-granularity (Phase 4), not retrieval faults.

**Phase 2 — Clause-level currency, surfaced as evidence (NOT derived).** **Coded + validated
(2026-05-30):** banhmi must not judge legal effect, so it does **not** parse amendment text into derived
section-validity (rejected: risks marking valid law repealed, and `section_validity_rows` stays 0 by
design). Instead the `document` tool follows confirmed `amends_supplements`/`replaces` relations back to
the amending documents and returns their instruction clauses verbatim as `incoming_amendments[]`
(`amending_doc`, `amending_effective_from`, `relation_type`, `position`, raw `text`), plus a warning gap
that the document is amended. The connecting model reads the raw clauses and decides what changed —
handling cross-doc references, additions, and phrase substitutions a parser would get wrong. Amendment
lead-verbs are config-seeded (`amendment.lead_verbs`), not hardcoded. Validated on `40/2024/TT-NHNN`: 34
clauses surfaced, incl. `41/2025` mirrored "Sửa đổi, bổ sung khoản 1…" (Điều 10) and `22/2026` explicit
"Bãi bỏ Điều 16–18 … 41/2025"; the Haiku-over-MCP stand-in agent reasoned currency from them unaided.

**Phase 3 — Evidence-pack polish** (smaller, parallel). Contain raw `vbpl_type_N` relation codes in MCP
output (neutral label, keep `relation_type_raw`; do not guess legal semantics); further-trim per-hit
relation metadata on relation-heavy queries (~88KB); re-extract/normalize the few genuinely-garbled chunks
(e.g. `08/2001/L-CTN` "Toàn văn" table noise).

**Phase 4 — Grow the eval golden set.** **Done (2026-05-30):** golden 8 → 11 cases, gated on
corpus-verified evidence. Added `Diem` to `ExpectedCitation` (+ điểm matching in `cmd/eval`) for
điểm-level gates. New: `onbank-pin-length` (clause), `onbank-password-length` (**điểm** — passes at rank
1, confirming điểm retrieval survives roll-up), `infosys-level3-09-2020` (clause). Re-baselined two
mis-anchored cases against actual document text: `atttt-50-2024` → Điều 3 (general security principles,
not the narrow Điều 7/19); `sinh-trac-hoc-2345` → governing biometric provisions (Điều 11 K5 + Điều 10),
no longer `expect_abstain` (the đồng thresholds live in the un-normalized Phụ lục / non-binding QĐ 2345;
evidence-only surfaces the rule + gap, never abstains in-scope). Result: **recall@k 50→83.3%, mrr@k
66.7→90.0%, abstention 87.5→100%, current-law 100%**. One case kept failing on purpose:
`trung-gian-tt-40-2024` — retrieval ranks the replaced `101/2012/NĐ-CP` above current `52/2024/NĐ-CP`
Điều 22, exposing a current-law-preference/validity gap (next, see below).

**Phase 5 — Source fidelity & validity correctness (Codex audit 2026-05-30).** A multi-agent audit (37
findings, 12 high) found the root cause behind the `trung-gian`/`52/2024` failure is broader than ranking:
the VBPL integration drops ~27 detail fields and several validity defaults **fabricate currency**. Root
example: `52/2024/NĐ-CP`'s own enacting clause says effective 2024-07-01, but VBPL's structured
`effFrom=2027-07-01` + `isEffectAllDocument=false` (staggered effect) is dropped, so it is mis-classed
`not_yet` and hidden.

Empirical test of the live VBPL API (18 docs, no code change) refined the plan: `effStatus` is reliably
present and our parsing matches it (0 null, 0 drift); **`isEffectAllDocument` is always `false`** — a
default, not a staggered-effect signal (capturing it buys nothing). The real failure mode is rare VBPL
source errors, and each document's own enacting clause is ground truth. Work, highest-value first:

- **A. ✅ Done (2026-05-30) — correct wrong not-yet status from the enacting clause.** When VBPL says
  `not_yet` but the document's own `"… này có hiệu lực thi hành (kể) từ ngày D tháng M năm Y"` clause is
  already past, classify `in_force` with the clause date + `reason=enacting_clause_overrides_vbpl_not_yet`
  (raw `status_code` kept as provenance). Deterministic, self-reference-anchored, no hardcoded ids; fixed
  `52/2024` + `50/2025`, left genuine-future docs `not_yet` (8→6). Result: **eval mrr@k 100%, recall@k
  91.7%, current-law 100%**, `trung-gian` flips green. Defensive null→`unknown` deferred (needs a
  status_class enum value + migration; null never occurs in practice).
- **B. Capture skipped VBPL data (needs re-crawl to populate).** ✅ **Keystone done (2026-05-30):**
  `FetchDetail` now preserves the verbatim detail `data` JSON (minus the inline HTML body) as a bronze
  `detail_meta` raw payload, and populates `IssuerCode` from `organization.code`. Because the raw JSON is
  preserved, the remaining fields — `references[].referenceProvisions` + target `status/effFrom/effTo`,
  signer (`documentIssues[].personName`) — can be extracted into silver later by re-normalizing bronze,
  **without another re-crawl**. (HTML body is already kept as `content_html`; gazette comes from congbao.)
- **C. De-hardcode policy/vocab into config tables** (operator-editable, seeded CSV; read via `cfgQ`, so
  no `config.go` churn). **Done (2026-05-30):** `config.validity_status` (effStatus→class map + the
  current-law set via `is_current_law`) — normalize resolves codes through it, fallback to built-ins;
  `config.relation_type` (referenceType→label map + amending-types via `is_amending`) — the vbpl source
  and the MCP document tool read it. Both behavior-preserving (eval unchanged). Added a **deterministic
  vector tie-break** (`, c.id`) so retrieval is reproducible. **Deferred:** wiring the current-law
  *filter* to `is_current_law` — a config subquery perturbs the hottest query's plan/tie-break (borderline
  eval flip); needs a plan-stable build (Go-load classes → validated literal IN-list). **Remaining:**
  authority-rank, hierarchy labels, mojibake patterns, chunk/embed/prefix sizes, `-fetch/-discover all`
  source lists.
- **E. ✅ Current law as evidence, not a filter (2026-05-30).** Vietnamese law overlaps heavily (269
  amends_supplements + 70 replaces edges across indexed docs; some amended by up to 8 others), so "current
  law" is not a doc-level binary and the old `in_force`/`partial` hard pre-filter excluded ~30% of indexed
  docs (108 expired + 6 not-yet), pre-judging currency. Search now leads with current law (primary pass
  unchanged — eval recall@k 91.7% / mrr@k 100% hold) and appends a small, separate, badged pass of
  non-current matches (`outOfForceCTE`, cap 3) so repealed/overlapping law stays findable; the agent
  judges currency from the validity badge + amendment/replacement relations. Verified over MCP (e.g.
  `46/2014` surfaced `expired` after current law). `SearchOpts.InForceOnly=true` = strict current-only;
  `=false` = no filter. Next: relevance-gate the non-current pass; consider correcting `101/2012`-style
  stale `in_force` (replaced docs not refreshed) via VBPL status refresh.
- **D. Cleanup:** seed `retrieve.abstain.min_score` (the score floor is silently off); wire or delete the
  orphan `extract.pdf.*` keys; drop dead config fields (`SBVCategory`/`AgencyID`/`embed.enabled`);
  default `cmd/eval -retrieval-mode` to `vector` (matches production).
- **✅ Reset + re-crawl done (2026-05-31):** dropped data schemas, re-discovered (scope-tuned: dropped
  `công nghệ thông tin`, 421→266 primary), re-fetched (detail_json preserved), re-extracted, re-normalized
  (validity corrected via enacting clause, signer populated, relations), backfilled relation targets, then
  indexed + embedded **inline on the local OVMS GPU** → **555 docs, 61,917 chunks, 100% embedded, 0
  failures**. Eval improved: **recall@k 100% / mrr@k 100% / current-law 100% / abstention 100%**. Signer is
  real `người ký` (e.g. Lê Minh Khái, Trần Thanh Mẫn). `52/2024`→in_force via enacting clause.
- **OCR / Kaggle follow-up:** EasyOCR is validated end-to-end (the first 2-scan run wrote good Vietnamese).
  The 17-scan re-crawl OCR backlog is blocked by Kaggle **account/API state** (heavy create/delete churn
  this session → `datasets.get` 403 + create/version idempotency + retired slugs). Code hardened
  (no-delete, fresh slugs, reuse-on-exists). Embed was routed to the **local GPU** to bypass it. Re-run the
  OCR backlog once Kaggle clears, or run OCR locally; revisit the kagglebatch create/version + token scope.

**✅ Then Track B — cloud deploy (DONE 2026-06-01):** hardened `/mcp` (public by default, opt-in API key),
built the **in-process OpenVINO** embedder image (replaced the planned OVMS CPU sidecar), stood up **AWS
RDS** + pgvector (replaced Neon — free cap overflowed mid-restore), dump/restored the corpus, deployed the
single-container Cloud Run service, and put it behind the **Firebase-Hosting** custom domain
`https://banhmi.danny.vn/mcp`. See [Track B — DEPLOYED](#track-b--deployed--live).

---

## Decisions log

| Decision | Choice | Principle |
|----------|--------|-----------|
| **INPUT before OUTPUT** | MVP1 = a trustworthy corpus, validated on real docs; then deploy the MCP evidence service | the model is the user's; **data quality is the product** |
| **Evidence-only; no answer LLM** | banhmi exposes citations/validity/relations/gaps over MCP; users bring their model; answering is a separate microservice if ever needed | accuracy first — we own the data, not the answer |
| **Deploy shape** (shipped 2026-06-01) | worker local (local GPU) → writes corpus to **AWS RDS** Postgres (`ap-southeast-1`) → **GCP Cloud Run** MCP (Go server with **in-process OpenVINO** BGE-M3 embedder — exact index model, single binary) queries it, behind a **Firebase-Hosting** custom domain. *Deviations from plan: RDS replaced Neon (free cap overflowed mid-restore); in-process OpenVINO replaced the OVMS sidecar.* | save resources; ~$0 idle on Cloud Run; only DB + MCP are public |
| **Vector-only in the cloud** | pgvector, no `pg_search`/BM25 | GPU-vector already beats BM25/hybrid on eval; keeps the DB to plain pgvector |
| **"Coded" ≠ "validated"** | status tracks code-written vs checked-on-real-docs separately | never ship unvalidated extraction as "done" |
| No hardcoded policy lists | all tunable vocab in `config`; code reads DB at startup | edit CSV + re-seed, no code change |
| No AI as canonical parser | born-digital extraction is deterministic (MarkItDown; legacy DOC → LibreOffice PDF). Scanned/failed PDFs run EasyOCR (`vi`) as a batch | never replace legal body text from generative output |
| PDF engine | **MarkItDown** (`pdfminer.six`) for born-digital PDFs — no GPL/AGPL | one converter for DOCX/HTML/PDF, same quality gate |
| OCR baseline | **EasyOCR (`vi`)**, batched (`OcrAll`, local CPU / Kaggle GPU) — replaced OCRmyPDF/Tesseract | better diacritics; batch, not inline |
| Embedder | **BGE-M3** (OpenVINO INT8, GPU) for pgvector; Gemma is not the embedder | Gemma never replaces `gold.chunk_embedding` |
| No composite primary keys | surrogate `BIGINT GENERATED ALWAYS AS IDENTITY`; business keys are `UNIQUE` | idempotent `ON CONFLICT` upserts |
| Containers: podman-first | `Containerfile`, no host installs | `podman compose up` one command |
| Pre-release: migrations mutable | renumber/squash until first tagged release; after release append-only | no fix-up migrations pre-v1 |
| Validity-aware retrieval | current-law pre-filter (`in_force` + `partial`) before search; surface validity badge + gaps | never present repealed law as current |
| Relation confidence split | confirmed VBPL structured effects separate from weak exact-number links; weak links expand context but cannot drive validity | deterministic INPUT records evidence; OUTPUT gets enough context to reason safely |
