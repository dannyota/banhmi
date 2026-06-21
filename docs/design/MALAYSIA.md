# Malaysia jurisdiction (laksa) — design

**Status: PROPOSED (2026-06-21). Not built.** Captures the decision + verified source research for
extending banhmi to **Malaysian banking digital/technology regulation**. The shared crawl philosophy and
pipeline live in [`SOURCES.md`](SOURCES.md), [`PIPELINE.md`](PIPELINE.md), [`EXTRACTION.md`](EXTRACTION.md),
and [`RAG.md`](RAG.md) — this doc states only what is **Malaysia-specific**.

## Decisions locked

- **Name / endpoint:** `laksa.danny.vn` — food-themed, parallel to banhmi = *bánh mì*.
- **Structure:** **same repo, jurisdiction as a config dimension** — not a branch, not a fork. VN
  production is untouched (MY sources simply aren't enabled in the VN config). One shared core = one place
  to fix bugs.
- **Scope:** same topical scope as VN — banking **digital/technology** regulation (IT & system risk,
  cybersecurity, data protection, cloud, outsourcing, e-transactions/e-signature, digital banking &
  payments, e-KYC, technology operations) — Malaysian jurisdiction.

## Why same-repo, not a branch

A long-lived branch never merges back; every core fix (extract/RAG/MCP) would diverge across two heads
forever. banhmi is already pluggable (sources under `pkg/ingest/`, scope in the DB-seeded `config`
schema), so a jurisdiction = config + new source packages + one new parser.

```
                 one codebase (master)
                         │
          ┌──────────────┴───────────────┐
       VN config                      MY config
   ingest: vbpl, vanban,…       ingest: bnm, agclom, sc
   scope: VN terms              scope: EN/Malay terms
   cite: Điều/Khoản             cite: Part/Chapter/Section/Subsection
          │                              │
   RDS (VN corpus)                RDS (MY corpus)
   CloudRun → banhmi.danny.vn    CloudRun → laksa.danny.vn
          └─ shared core: pipeline · extract · BGE-M3 · pgvector · MCP ─┘
```

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
- **Discovery:** poll the sector listing pages — `/banking-islamic-banking`, `/payment-systems` — sort by
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
- **Language:** EN + BM both published; **BM is often the prescribed authoritative text**. Plan: ingest
  **English as primary**, keep BM as the parallel/authoritative companion, record which is prescribed.

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
| Language | Vietnamese | English-first (+ BM parallel) | new scope vocab + dedup keys; BGE-M3 already multilingual (**no model change**) |
| Crawl | HTTP/JSON | BNM bot-hostile; LOM JS-rendered; SC clean | headless/real-UA fetch (Playwright already present); known PDF URL patterns |
| Reused unchanged | — | — | Medallion pipeline · MarkItDown+OCR · BGE-M3 + pgvector · MCP tools · deploy shape |

**Feasibility: high** — ~80% is config + new source packages on the existing core; the only genuinely new
code is the PDF-structure parser.

## Spike — PDF-structure parser (PROVEN 2026-06-21)

Validated the one risky piece on **FSA 2013** (AGC LOM, 287 pp born-digital, fetched via plain HTTPS).
Deterministic text→tree works; **no OCR** for modern reprints.

**Result:** 17/17 Parts (full titles, in order) · **281/281 sections, range 1..281, 0 gaps / 0 dupes** ·
correct part assignment (s.129 → Part VIII, s.271 → Part XVII) · 557 subsections, 1109 paragraphs.

**Recipe (deterministic, ~60 lines, validated):**
1. Strip page noise — bare page numbers, `Laws of Malaysia`, `ACT <n>` running headers.
2. Cut the front "Arrangement of Sections" TOC at the `ENACTED by …` enacting clause.
3. `PART <roman>` / `Division <n>` → title = following ALL-CAPS line(s); **join multi-line** titles.
4. Section = `^N.` in **two forms**: `N. (1) text` inline **or** `N.` alone on its own line.
5. **Monotonic filter** — accept a section only if its number is `last+1` (or `271A` after `271`). This
   drops the schedules' own `1. 2. 3.` renumbering and inline cross-refs. Stop sections at first `SCHEDULE n`.
6. Subsections `(n)`, paragraphs `(a)`.

**Residual (tractable, not a blocker):** marginal-note **titles** mis-associate on a few sections (pdftotext
flattens margin geometry) → use **layout-aware extraction** (pdfplumber / `pdftotext -layout` x-coords) to
pick the margin note by position, not line order. Numbering/hierarchy/part-mapping is unaffected.

**Fetch reality (confirmed live):** AGC LOM = plain HTTPS GET (200, born-digital PDF). **BNM = bot challenge**
(HTTP 202 JS interstitial even with browser headers) → needs the **headless-browser fetch** (Playwright,
already in the stack). SC = permissive (stable `download.ashx?id=`).

## Phased plan

1. **Jurisdiction seam** — make jurisdiction a config dimension: generalize the citation/provision model
   (Điều/Khoản → pluggable), per-jurisdiction scope vocabularies, a per-jurisdiction source registry.
2. **PDF-structure parser** — born-digital PDF → Part/Section/Subsection tree. ✅ **Spiked & proven on
   FSA 2013** (281/281 sections; recipe above); remaining work = layout-aware titles + OCR floor for the
   scanned-Act tail.
3. **Sources** — `pkg/ingest/agclom` (Acts + timeline validity/relations + P.U. gazette feed),
   `pkg/ingest/bnm` (sector listings + `/-/` metadata), `pkg/ingest/sc` (scoped).
4. **Validity/relations** — from the LOM timeline; infer BNM supersession from newest-dated + prose.
5. **Deploy** — 2nd RDS corpus + 2nd Cloud Run service → `laksa.danny.vn` via Firebase (same shape as VN).

## Open questions / risks

- **PDF-structure parser accuracy** — ✅ de-risked (spike above): numbering/hierarchy proven exact on FSA
  2013. Residual = layout-aware **marginal-note titles**; validate the recipe on more Acts before scaling.
- **EN vs BM authoritative text** — which to treat as binding per Act; record the prescribed version.
- **BNM supersession** — no status field; risk of presenting a superseded PD as current. Needs a reliable
  newest-version rule + change-list parsing.
- **DB layout** — one RDS instance with a MY schema/database vs a separate instance. Decide at deploy time.
