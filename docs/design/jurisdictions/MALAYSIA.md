# Malaysia jurisdiction (laksa) — design

**Status: LIVE — built, deployed (`laksa.danny.vn`), and validated (2026-06).** Captures the decision +
verified source research for extending banhmi to **Malaysian banking & financial regulation and
technology law** —
the **first expansion**, whose build generalized into the shared [`PLAYBOOK.md`](PLAYBOOK.md)
(jurisdiction model, language policy, seam, phase template). This doc states only what is
**Malaysia-specific**; crawl philosophy and pipeline live in [`SOURCES.md`](../SOURCES.md),
[`PIPELINE.md`](../PIPELINE.md), [`EXTRACTION.md`](../EXTRACTION.md), and [`RAG.md`](../RAG.md).

## Decisions locked

- **Name / endpoint:** `laksa.danny.vn` — food-themed, parallel to banhmi = *bánh mì*.
- **Structure:** same repo, jurisdiction as a config dimension (the model now canonical in
  [`PLAYBOOK.md`](PLAYBOOK.md)); VN production untouched.
- **Scope:** same topical scope as VN (per the playbook) — Malaysian jurisdiction.
- **Language:** **English (BI)** — MY law is natively English, so English queries are native. AGC
  publishes EN + BM; per the one-language policy banhmi ingests **English only** (BM is not fetched).

## Sources (verified live 2026-06-21)

Malaysia needs **3 sources, not 4** — the federal gazette folds into the law DB.

```
VN                          →   Malaysia
──────────────────────────────────────────────────────────────
SBV portal (regulator)      →   BNM      bnm.gov.my       ◄ PRIMARY tech regs
VBPL (national law DB)       →   AGC LOM  lom.agc.gov.my   ◄ Acts + validity/relations
Công Báo (gazette signal)    →   AGC LOM  "What's New" + P.U.(A/B)   (same host)
2nd regulator                →   SC       sc.com.my        ◄ capital-mkt fintech (scoped)
```

⚠ `federalgazette.agc.gov.my` is **dead (NXDOMAIN)** — its gazette function now lives inside LOM.

| Source | Body text | Validity / relations | Crawl | Notes |
|---|---|---|---|---|
| **BNM** | born-digital **EN PDF** | **weak** — infer from newest-dated + "Revised…"/Highlights prose | static client-side DataTables (whole list in one page); **bot-hostile** → descriptive UA + headless | primary; PDF is canonical text |
| **AGC LOM** | born-digital PDF (**EN + BM**) | **strong** — per-Act timeline: commencement dates, amend/repeal, P.U. cites | listing + PDF links **JS-rendered** → headless or known URL pattern; `robots` host returns 500 (none) | structure is **PDF-only** (no HTML provision tree) |
| **SC** | born-digital PDF (`api/documentms/download.ashx?id=<GUID>`) | **good** — status + supersession + "Summary of Amendments" | clean server HTML; **permissive robots**; sitemap | scope to tech/cyber/digital-asset/outsourcing **only** (capital-markets, not banking) |

### BNM — bnm.gov.my (primary regulator)
- **Discovery:** poll the sector listing pages — `/banking-islamic-banking`, `/payment-systems`,
  `/money-services-business`, `/development-financial-institutions`, `/digital-currencies` — sort by
  date, diff against last-seen; `/pr` press feed is a secondary new-doc signal. No API/RSS.
- **Per-doc metadata:** `/-/<slug>` landing pages give structured **Issuance Date + Effective Date +
  Highlights/Applicability**; **no status field** → supersession inferred (the metadata weak spot).
- **Seed docs:** RMiT, Cloud (CTRAG, also in RMiT Appendix), Outsourcing, e-KYC, e-Money, BCM,
  Operational Resilience, Management of Customer Information, Licensing Framework for Digital Banks, FinTech
  Regulatory Sandbox, Open Finance. Files are English born-digital PDF (Range/206 supported).

### AGC LOM — lom.agc.gov.my (law DB + gazette, the VBPL analog)
- **Browse:** `principal.php?type=updated|repealed|revised`, `amendment.php`, `subsid.php?type=pua|pub`;
  detail `act-detail.php?act={N}&lang=BI|BM`; "What's New" dated feed = the **Công Báo analog**.
- **Validity/relations (strong):** the detail page is a parseable per-event **timeline** — publication /
  royal-assent / commencement dates, amendment + repeal events, each with a `P.U. (A/B)` citation + PDF.
  Relations are P.U. numbers / text references → need parsing+linking (not clean machine IDs).
- **Structure (the gap):** provision hierarchy (Part/Chapter/Section/Subsection/Paragraph) is **inside the
  PDF only** — no HTML tree like VBPL gave us. Modern reprints are born-digital (text extractable, no OCR).
- **Key Acts:** FSA 2013 (`act=758`), IFSA 2013 (`759`), CBMA 2009 (`701`), PDPA 2010 (`709`), AMLA 2001
  (`613`), Computer Crimes 1997 (`563`), Cyber Security Act 2024 (`854`), Electronic Commerce 2006.
- **Language:** EN (BI) + BM both published; per the one-language policy banhmi ingests **English (BI)
  only** — BM is not fetched.

#### agclom — verified fetch contract (2026-06-21; all plain HTTP, no headless)
- **Discover principal Acts:** `POST https://lom.agc.gov.my/json-updated-2024.php` (DataTables body
  `draw/start/length/search[value][value]`; `recordsTotal` ≈ 885). Each record: `lgt_act_id` (the act id),
  `lgt_act_no` (Act number), `title` (HTML: `act-detail.php?act=<id>&lang=BI` link + title + "As At <date>"),
  `doc2downloadgeneratepdf` (JSON array `{path, docName, icon}` per language — take the **BI** entry).
- **PDF file:** `https://lom.agc.gov.my/ilims` + `path` + `docName`
  (`…/ilims/upload/portal/akta/outputaktap/<id>_BI/<NAME>.pdf`) — plain GET, born-digital.
- **Act dates (own validity):** `GET act-detail.php?act=<id>&lang=BI` — HTML carries Publication Date,
  Royal Assent Date, Commencement Date, Commencement Remark (P.U. cites + per-section exceptions); parse it.
- **Relations / P.U. timeline (gazette analog):** `POST json-subsid-2024.php?act=<id>` (`recordsTotal` e.g.
  59 for FSA). Each record: `noPU` ("P.U. (A) 61/2025"), `titleBI`, `commencementDate`, `publicationDate`,
  `subsidiaryLegislationType` (pua/pub), `DOC2DOWNLOAD` (instrument PDF) → Relation edges.
- **Scope:** 885 Acts = ALL federal law → discovery **scope-filters by title** via MY config scope terms
  (finance/bank/payment/data/cyber/computer/electronic/digital), never by hardcoded act ids.
- **Number (số-ký-hiệu analog):** "Act <lgt_act_no>" (e.g. "Act 758").

### SC — sc.com.my (secondary, scoped)
- **In scope only:** Technology Risk Management (SC-GL/2-2023), Cyber, Digital Assets, Recognized-Markets
  digital, outsourcing-within-tech-risk. **Out of scope:** IPOs, unit trusts, market conduct.
- Clean HTML lists with current + dated archive; PDFs via stable `download.ashx?id=<GUID>`; good
  date/status/supersession metadata; permissive `robots`. Easiest of the three to crawl.

## Deltas from the VN build

| Area | VN | Malaysia | Work |
|---|---|---|---|
| Legal structure | VBPL HTML provision tree (free) | **PDF-only** | **NEW: born-digital PDF → Section/Subsection tree parser** (the main new build + biggest risk) |
| Citation model | Điều/Khoản/Điểm | Part/Chapter/Section/Subsection/Paragraph | generalize to a jurisdiction-pluggable provision path |
| Language | Vietnamese (native, binding) | English (native, binding) | one main language per country — index/serve/search in it only; **no translation** (user's responsibility); new scope vocab + dedup keys |
| Crawl | HTTP/JSON | BNM bot-hostile; LOM JS-rendered; SC clean | headless/real-UA fetch (Playwright already present); known PDF URL patterns |
| Reused unchanged | — | — | Medallion pipeline · go-fitz+OCR · Qwen3-Embedding + pgvector · MCP tools · deploy shape |

**Feasibility: high** — ~80% is config + new source packages on the existing core; the only genuinely new
code is the PDF-structure parser.

## Jurisdiction seam — MY-specific notes

The generic seam (share/customize split, safety invariants, deploy fan-out) is canonical in
[`PLAYBOOK.md`](PLAYBOOK.md); it was verified here first by a 3-part code audit (2026-06-21).
MY-specific residue:

- **Data boundary (decided 2026-06-21):** VN `banhmi` and MY `laksa` are **separate databases on the
  same RDS instance** (not a 2nd instance, not a `jurisdiction` column) — fully isolated, zero
  migration risk to live VN, one bill. Caveat: `db.t4g.micro` is small (~1 GB RAM, limited
  connections) — watch combined VN + MY load and split only if it contends.
- **DDL:** the only schema change the MY build needed was **relaxing the
  `silver.document_section.kind` CHECK** (migration 00005); gold untouched.

**VN improvements this unlocked (done alongside the MY build):**
- Centralize the **4 duplicated VN provision-label maps** → one config lookup (kills drift between the
  Markdown and VBPL-tree parsers).
- De-hardcode the `nhnn` scope signal → use the existing-but-unused `config.issuer_code.is_sbv`.
- Roll up `parentCitation`/`attachArticles` by **level depth**, not fragile Vietnamese substring matching.
- Build the MCP brief/guide **from config** (removes near-verbatim duplication across `mcp.go` + `corpus.go`);
  move Vietnamese jsonschema field descriptions to English (the agent-facing contract language).
- **Single-source the source list** (3 literals that must agree → 1 registry; remove dead `SourcesConfig.Enabled`).

## Spike — PDF-structure parser (PROVEN 2026-06-21)

Validated the one risky piece on **FSA 2013** (AGC LOM, 287 pp born-digital, fetched via plain HTTPS).
Deterministic text→tree works; **no OCR** for modern reprints.

**Result (pdftotext spike):** 17/17 Parts · **281/281 sections** · 557 subsections, 1109 paragraphs.
Note: this spike used pdftotext, not go-fitz. Go-fitz (the production extractor) has different line
geometry — the marginal-note fix above was needed to match these numbers in production.

**Recipe (deterministic, ~60 lines, validated):**
1. Strip page noise — bare page numbers, `Laws of Malaysia`, `ACT <n>` running headers.
2. Cut the front "Arrangement of Sections" TOC at the `ENACTED by …` enacting clause.
3. `PART <roman>` / `Division <n>` → title = following ALL-CAPS line(s); **join multi-line** titles.
4. Section = `^N.` in **two forms**: `N. (1) text` inline **or** `N.` alone on its own line.
5. **Monotonic filter** — accept a section only if its number is `last+1` (or `271A` after `271`). This
   drops the schedules' own `1. 2. 3.` renumbering and inline cross-refs. Stop sections at first `SCHEDULE n`.
6. Subsections `(n)`, paragraphs `(a)`.

**Residual (fixed 2026-07-15):** marginal notes on the same line as section numbers caused go-fitz to
merge them; the monotonic filter then rejected every later section. Fix: pre-split lines at
`2+ spaces + "N. "` in `myBodyLines` (`pkg/pipeline/malaysiaparse.go`). Validated on all 22 Act PDFs,
zero regressions (Acts 758/759: 0→281/291 sections). **Lesson: always validate parsers against the
production extractor (go-fitz), not pdftotext** — the original spike validated with pdftotext, which
has different line geometry.

**Fetch reality (proven live 2026-06-21):** AGC LOM = plain HTTPS GET (200, born-digital PDF). **BNM =
AWS WAF *Challenge* + Liferay, no open API** (headless-delivery 404/403, `/api/jsonws` 403; sector listing
is server-rendered HTML, no XHR feed). The listing serves an AWS WAF JS challenge (`challenge.js` from
`*.token.awswaf.com`, `gokuProps`) — **pure HTTP cannot mint the token**: plain `curl`, `requests`, and even
**`curl_cffi` Chrome-TLS impersonation all return the 202 challenge with no cookie set**. So JS execution is
mandatory. **Pattern (PoC-proven):** a headless browser loads the listing **once** → runs the challenge →
mints the `aws-waf-token` cookie → **reuse that cookie + matching UA in a plain HTTP client** for bulk
downloads. Python PoC downloaded **3/3 PDFs (RMiT 762 KB, e-KYC 648 KB, Outsourcing 391 KB)** with the
reused listing cookie. Re-mint on expiry/403. Go crawler: mint via chromedp/rod, reuse via `net/http`.
SC = permissive (stable `download.ashx?id=`).

## Phased plan

1. **Jurisdiction seam** — make jurisdiction a config dimension: generalize the citation/provision model
   (Điều/Khoản → pluggable), per-jurisdiction scope vocabularies, a per-jurisdiction source registry.
   ✅ **Shipped** — now the `pkg/base/jurisdiction` descriptor registry (see
   [PLAYBOOK](PLAYBOOK.md#seam-registry--shipped)).
2. **PDF-structure parser** — born-digital PDF → Part/Section/Subsection tree. ✅ **Spiked & proven on
   FSA 2013** (281/281 sections; recipe above); remaining work = layout-aware titles + OCR floor for the
   scanned-Act tail.
3. **Sources** — `pkg/ingest/agclom` (Acts + timeline validity/relations + P.U. gazette feed),
   `pkg/ingest/bnm` (sector listings + `/-/` metadata), `pkg/ingest/sc` (scoped).
4. **Validity/relations** — from the LOM timeline; infer BNM supersession from newest-dated + prose.
5. **Deploy** — a separate `laksa` database on the **same RDS instance** + ECS container on AWS →
   `laksa.danny.vn` (same image, `BANHMI_DATABASE_NAME=laksa`). Migrated to AWS CloudFront + ECS in v0.3.0.

**Status (2026-06-21):** Phases A–E done & validated on a local `laksa` DB. The **chunker is
jurisdiction-aware** (additive; VN bytes untouched): MY chunks at **Section**, walks
Section→Subsection→Paragraph, treats **Schedule** as the appendix-equivalent, adds **Part/Chapter**
context, renders native citations (`Section 5`, `(1)`, `(a)`), and labels long-leaf splits
`Đoạn`(VN)/`Paragraph`(MY). **52 docs · 7,182 chunks · 7,182 embeddings (100%)** via the Kaggle GPU
Qwen3-Embedding batch; pgvector search returns the right provisions (RMiT, Cyber Security Act 2024, e-KYC PD).

**Phase E (serve):** the MCP surface is jurisdiction-aware via a compiled `brief`
(`pkg/mcp/brief.go`: name/title/instructions/guide/tool-descriptions), selected by
`mcp.WithJurisdiction(cfg.Jurisdiction)`, with **VN as the default fallback**. laksa advertises an
English Malaysia brief (sources AGC LOM / BNM / SC; Part/Chapter/Section/Subsection/Paragraph citation;
English-only — never translates). The retrieval current-law pre-filter is **data-driven**: disabled
when a corpus has chunks but zero in-force/partial validity (MY's is all `unknown` until validity
derivation), so MY serves pure relevance with honest *"Validity unknown — verify against the official
source"* badges; VN (in-force rows present) is unchanged. Grew the MY scope seed
(RMiT/cloud/IT-outsourcing/e-KYC now strong). Validated by driving the real stdio MCP server against
laksa (identity `laksa`; "technology risk management" / "cloud outsourcing" / "cyber incident" →
abstain=false, in-domain, 8 hits with official source_urls; document "Act 854" → Cyber Security Act
2024). **Endpoints are fully separated** — one process = one DB pool, so the MY endpoint cannot reach
the VN database.

**Quality pass (2026-06-21):** a review-driven hardening of the MY corpus, all VN-safe:
- **Coverage** — agclom now downloads Acts that have no generated reprint (the viewer's LOM/EN or
  bare-`outputaktap` PDF), recovering the Electronic Commerce Act 2006, Payment Systems Act 2003, and
  MCMC Act 1998; the 7 older scanned Acts were OCR'd in **English** (`Config.OCRLanguages()` → `en`).
- **Validity** — each agclom Act is classified PRINCIPAL/REPEALED from its detail page and mapped via
  `config.validity_status`; MY unknown defaults to in_force (curated current corpus), so the data-driven
  filter **re-enables** and badges read *In force* / *Expired-repealed* (e.g. Payment Systems Act 2003 =
  expired). VN's strict unknown rule is untouched.
- **Relations (loopback)** — agclom is now a trusted structured source, so its **1000 P.U. links promote**
  to `document_relation` typed `subsidiary_legislation` (Act → its regulations); the document tool serves
  them (stubs, backfillable). VN promotion unchanged.
- **Parser** — case-insensitive headings cut the small-caps TOC (fixes the section-25 citation
  collisions → 0 duplicate paths); roman `(i)/(ii)` nest as subparagraphs; `(1992)` years are no longer
  subsections; a full-text fallback chunks structureless docs.
- **MCP schemas** — tool field descriptions are jurisdiction-neutral (no Vietnamese leaking into MY).

**Deployed 2026-06-22** → `laksa.danny.vn/mcp` (separate `laksa` DB on the shared RDS, same image;
migrated from Cloud Run to AWS ECS in v0.3.0). **Hybrid retrieval live since `v0.1.0-20260704`** (BM25 sparse + RRF; eval:
recall 95%, mrr 82.1%, current-law+abstention 100%; `bm25_score` per hit).

**Corpus now: 63 docs · 8,425 chunks · 100% embedded · 100% sparse · 62 in-force + 1 expired · 1000
relations.** Remaining: P.U. relation-target backfill (1,000 unresolved stubs); 8 `needs_review` agclom
Acts (null markdown from extraction — Acts 627, 623, 618, 613, 563, 545, 519, 459); layout-aware
Section titles.

## Open questions / risks

- **PDF-structure parser accuracy** — ✅ de-risked (spike above): numbering/hierarchy proven exact on FSA
  2013. Residual = layout-aware **marginal-note titles**; validate the recipe on more Acts before scaling.
- **EN vs BM authoritative text** — which to treat as binding per Act; record the prescribed version.
- **BNM supersession** — no status field; risk of presenting a superseded PD as current. Needs a reliable
  newest-version rule + change-list parsing.
- **DB layout** — ✅ decided 2026-06-21: **same RDS instance, separate `laksa` database** (not a 2nd
  instance, not a jurisdiction column). Watch the `db.t4g.micro` RAM/connection budget under combined
  VN + MY load; split out only if it contends.
