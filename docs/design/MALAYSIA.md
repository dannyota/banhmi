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

## Language policy (one main language per country)

Each country's regulatory corpus is in its **single main legal language** — **VN: Vietnamese; MY: English**
— and banhmi **indexes, serves, and searches in that language only**. The native text is the **binding
ground truth**; banhmi **never translates** legal text, because a translation could introduce error and
would not be authoritative. A user who needs another language translates the returned native evidence
themselves — **translation is the user's own responsibility**. So: no multilingual/translated index, no
in-corpus English/Chinese layer. (MY law is natively English, so English queries are native there; a future
Chinese-speaking jurisdiction would be served in Chinese, not as a translation of another corpus.)

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
| Reused unchanged | — | — | Medallion pipeline · MarkItDown+OCR · BGE-M3 + pgvector · MCP tools · deploy shape |

**Feasibility: high** — ~80% is config + new source packages on the existing core; the only genuinely new
code is the PDF-structure parser.

## Jurisdiction seam (share common · customize the rest)

Verified by a 3-part code audit (2026-06-21). Principle: **share only the common; customize what differs
behind interfaces** — the Go idiom the repo already uses for sources/extractors/embedders (interface at the
consumer + config-selected impl). There is **no jurisdiction concept today**; `source` id already acts as a
per-jurisdiction proxy (VN and MY source sets are disjoint). **VN is live; every switch defaults to VN.**

| Layer | Common (shared, unchanged) | Customized per jurisdiction |
|---|---|---|
| Sources | `ingest.Source` interface; Temporal fetch/drain | the source **set** (VN: vbpl/vanban/congbao/sbv_hanoi · MY: agclom/bnm/sc) |
| Structure parse | chunk-walker; MarkItDown/OCR mechanics | the **parser**: VN Markdown `ParseSections` vs MY PDF Section-tree parser — both emit the same `[]Section` |
| Citation/provision | `gold.chunk` storage; retrieval mechanics | provision **levels + labels** (VN Điều/Khoản… vs MY Part/Section…) → a `config` provision-level table + label lookup |
| Scope | matcher framework | scope vocab + the central-bank **signal** (VN `nhnn` vs MY `bnm`) |
| MCP | transport; the 5 tools; coverage assembly | **brief/guide/jsonschema text** + reply language + `pathToCitation` labels → config-driven |
| Deploy | one image; env-driven DB/embedder | `BANHMI_JURISDICTION` (default `vn`) selects sources+scope+config; **separate Postgres database (same RDS) + separate Cloud Run per jurisdiction** |

**Data boundary (decided 2026-06-21):** the Postgres **database** is the jurisdiction boundary — VN
`banhmi` and MY `laksa` as **separate databases on the same RDS instance** (not a 2nd instance, not a
`jurisdiction` column). Separate databases are fully isolated (own tables; no cross-DB queries) → **zero
migration/risk to live VN**, no `jurisdiction` column needed for correctness; co-located, one bill. Caveat:
`db.t4g.micro` is small (~1 GB RAM, limited connections) — watch combined load (VN + MY + Temporal) and
split MY to its own instance only if it contends. (One optional `config.scope_term.jurisdiction` column
still lets a single repo ship both seed sets.)

**VN-safety invariants (must hold):**
1. `gold.chunk.citation` bytes stay **byte-identical** → no re-chunk, no re-embed of the live corpus. Guard
   with a golden-citation regression test before flipping VN labels to config.
2. The only DDL is **relaxing the `silver.document_section.kind` CHECK** (silver is worker-re-derivable; gold
   untouched).
3. Default jurisdiction = `vn`; keep the VN brief/guide/labels as the **compiled fallback** so a missing
   config row or absent env can never change what `banhmi.danny.vn` advertises.

**VN improvements this unlocks (do alongside):**
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
   ✅ **Designed & VN-safe** (3-part audit) — see *Jurisdiction seam* above for the share/customize split
   and the VN-safety invariants.
2. **PDF-structure parser** — born-digital PDF → Part/Section/Subsection tree. ✅ **Spiked & proven on
   FSA 2013** (281/281 sections; recipe above); remaining work = layout-aware titles + OCR floor for the
   scanned-Act tail.
3. **Sources** — `pkg/ingest/agclom` (Acts + timeline validity/relations + P.U. gazette feed),
   `pkg/ingest/bnm` (sector listings + `/-/` metadata), `pkg/ingest/sc` (scoped).
4. **Validity/relations** — from the LOM timeline; infer BNM supersession from newest-dated + prose.
5. **Deploy** — a separate `laksa` database on the **same RDS instance** + a 2nd Cloud Run service →
   `laksa.danny.vn` via Firebase (same image, `BANHMI_DATABASE_NAME=laksa`).

**Status (2026-06-21):** Phases A–D done & validated on a local `laksa` DB. The **chunker is
jurisdiction-aware** (additive; VN bytes untouched): MY chunks at **Section**, walks
Section→Subsection→Paragraph, treats **Schedule** as the appendix-equivalent, adds **Part/Chapter**
context, renders native citations (`Section 5`, `(1)`, `(a)`), and labels long-leaf splits
`Đoạn`(VN)/`Paragraph`(MY). **52 docs · 7,182 chunks · 7,182 embeddings (100%)** on the local OVMS
BGE-M3; pgvector search returns the right provisions (RMiT, Cyber Security Act 2024, e-KYC PD).
Remaining: **E serve** (per-jurisdiction English MCP brief) + **F deploy**.

## Open questions / risks

- **PDF-structure parser accuracy** — ✅ de-risked (spike above): numbering/hierarchy proven exact on FSA
  2013. Residual = layout-aware **marginal-note titles**; validate the recipe on more Acts before scaling.
- **EN vs BM authoritative text** — which to treat as binding per Act; record the prescribed version.
- **BNM supersession** — no status field; risk of presenting a superseded PD as current. Needs a reliable
  newest-version rule + change-list parsing.
- **DB layout** — ✅ decided 2026-06-21: **same RDS instance, separate `laksa` database** (not a 2nd
  instance, not a jurisdiction column). Watch the `db.t4g.micro` RAM/connection budget under combined
  VN + MY + Temporal load; split out only if it contends.
