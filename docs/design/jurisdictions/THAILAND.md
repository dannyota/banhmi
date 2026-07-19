# Thailand jurisdiction (tomyum) — design

**Status: v0.5.0 — after Singapore. Sources live-verified 2026-07-16.** Extends banhmi to **Thai
banking & financial regulation and technology law** per the shared [`PLAYBOOK.md`](PLAYBOOK.md).
**Heaviest language work of the planned countries** — see *Hard parts* before scheduling.

## Basics

- **Codename / endpoint:** `tomyum` / `tomyum.danny.vn`
- **Language:** **Thai** — the binding legal language. OCS publishes EN translations (147 Acts)
  that are explicitly **non-binding → never indexed** (playbook policy).
- **Scope:** the shared topical scope, Thai jurisdiction.

## Sources (live-verified 2026-07-16)

| Source | URL | What it provides | Bot protection | Crawl difficulty |
|---|---|---|---|---|
| **OCS** | www.ocs.go.th | **Consolidated Acts** via JSON API (`searchlaw/indexs/list_table_search`): 1,891 laws, amendment tracking, in-force status, subordinate legislation links. All 6 target Acts confirmed. | **None** (bad TLS cert only — needs InsecureSkipVerify). | **2/5** |
| **BOT** | app.bot.or.th/FIPCS | **Notifications/circulars** (ประกาศ ธปท.): ~800–1,200 in-scope of ~3,700 total. PDF-only (no HTML body). ASP.NET WebForms pagination (`__doPostBack`). | **None** (Akamai CDN, no WAF). | **3/5** |
| **ETDA** | www.etda.or.th | E-transactions / Digital ID / Platform services (~40–60 instruments). ASP.NET + PDF. | **None.** | **1/5** |
| **SEC** | capital.sec.or.th | Digital assets / fintech securities (~80–150). Search subdomain open; PDF download may need browser headers. | WAF on main site; search subdomain **open**. | **4/5** |
| **Royal Gazette** | ratchakitcha.soc.go.th | New-law publication signal. Cloudflare on HTML; PDFs open at `/documents/{id}.pdf`. | **Cloudflare** on HTML. | **4/5** — supplementary, defer to Phase 2. |
| OIC, NBTC | — | **Skip.** Low relevance (4–10 docs), high friction (SPA/blanket 403). | — | — |

**Key finding:** `krisdika.go.th` is **dead** (404, self-signed cert). The Office of the Council
of State migrated to **`www.ocs.go.th`** with a structured JSON API — far cleaner than the old
HTML scraping assumption. No source has geo-blocking.

### Doc number formats

- **BOT:** `ธปท. ว. 3725/2569` (circular), `ประกาศ ธปท. ที่ 26/2569` (notification). Pattern:
  `{dept-abbrev} {seq}/{B.E. year}`.
- **OCS:** lawCode `ธ0012-1B-0001` (internal); display: Act name + B.E. year.
- **ETDA/SEC:** notification numbers; descriptive.
- ~270 BOT docs (~7%) have English "Unofficial Translation" PDFs alongside Thai.

### API / access contracts (Playwright-verified 2026-07-16)

- **OCS — two APIs:**
  - *Discovery:* `GET www.ocs.go.th/searchlaw/indexs/list_table_search?page={N}` — 10/page, 1,884
    laws, 189 pages. Returns `lawCode`, `lawNameTh`, `lawNameEn`, `encTimelineID`, `year`,
    `publishDate`, `fileUUID` (JWT-signed PDF URL), `childrens` (subordinate legislation array),
    `state`. No server-side filtering — paginate all, filter client-side by lawCode type
    (`-1B-` = Acts). TLS: missing intermediate cert on `www.ocs.go.th` → `InsecureSkipVerify`.
  - *Full text:* `POST searchlaw.ocs.go.th/ocs-api/public/doc/getLawDoc` with
    `{reqHeader:{reqChannel:"WEB",serviceName:"getPublicLawDoc",...}, reqBody:{timelineId:"<encTimelineID>"}}`
    → structured JSON: `lawInfo` (metadata + dates + stateId) + `lawSections[]` (sectionTypeId,
    sectionNo, sectionContent HTML, sectionLabel) + `footnoteList[]` (amendment annotations) +
    `timelines[]` (version history). Section types: 4=มาตรา, 8=หมวด, 9=ส่วน. Valid TLS on this host.
  - *Target Acts found:* ธ0012 (FI Act), ร0058 (Payment Systems), ค0136 (PDPA), ก0189 (Cybersecurity),
    ว0063 (ETA), ห0015 (Securities). All state=01, with subordinate legislation links.
- **BOT FIPCS:** `app.bot.or.th/FIPCS/Thai/PFIPCS_list.aspx` — ASP.NET WebForms, 30/page, 374 pages
  (~11,220 total; ~1,560 active in-scope with DocGroup 1+3). ViewState pagination via
  `__doPostBack('ctl00$...$dgDocument$ctl33$ddlPageSelector', pageNum)`. Session-bound: must reuse
  `ASP.NET_SessionId` + ViewState from initial GET. 8 dropdown filters (DocGroup, Year, DocType,
  Status, etc.) + 3 text searches. Summary page at `PFIPCS_summary.aspx?packId={PACKID}` has
  dates, purpose, substance. PDF: `www.bot.or.th/content/dam/bot/fipcs/documents/{GROUP}/{YEAR_BE}/ThaiPDF/{PACKID}.pdf`
  (direct, no auth, born-digital). packId format: `YYYYNNNN` (B.E. year + sequence).
- **ETDA:** 5 listing pages (DPS, Digital ID, ETC, Digital Law, Recommendations). Server-rendered
  ASP.NET HTML. PDFs at `getattachment/{GUID}/{filename}.aspx` (direct, no auth). ~100-120 in-scope
  instruments. No API — HTML scrape. Intermittent connectivity but no hard geo-block.
- **SEC:** `capital.sec.or.th/webapp/nrs/nrs_main_search.php` — PHP POST form, no pagination (all
  results inline). 15 document types, hierarchical category filter. ~101 digital-asset + ~36 IT docs.
  PDFs on `publish.sec.or.th/nrs/{NRS_ID}{suffix}` — **F5 BIG-IP geo-blocks non-TH IPs** (flat 403,
  not JS challenge). TIS-620/CP874 charset. **Needs Thai IP proxy** — AWS `ap-southeast-7`
  (Bangkok) confirmed: t4g.micro + tinyproxy, on-demand (~$0.005/hr). Phase 2.

## Citation model

Acts: **มาตรา** (Section) → **วรรค** (paragraph, ordinal words: วรรคหนึ่ง, วรรคสอง, วรรคสาม) →
**(๑)(๒)** items; BOT notifications number by **ข้อ** (clause). Two label families (like VN's
Luật vs Thông tư). Amendment suffixes use Pali/Sanskrit (ทวิ, ตรี, จัตวา). Native Thai labels
render verbatim.

## Hard parts (why TH is recommended last)

1. **Thai word segmentation (BLOCKER)** — no inter-word spaces; whitespace BM25 produces garbage.
   No viable Go-native tokenizer (mapkha is archived). Options at build time:
   - **nlpo3** (Rust, fast, thread-safe, ~89% accuracy) via FFI/subprocess
   - **TCC-gram** (Thai Character Clusters — deterministic, regex-based, no dictionary,
     comparable IR recall per research; pure Go portable)
   - Interim fallback: router goes vector-primary for TH (dense Qwen3 handles Thai natively)
   - The TextNormalizer seam (shipped 2026-07-15) is the integration point: TH normalizer
     must NOT NFD-strip (Thai combining marks are integral to the script)
2. **Buddhist Era dates** (B.E. = CE + 543) — custom Go parser (~50 lines). Formal sources use
   Thai numerals + พ.ศ.; portals mix Arabic numerals. Pre-1941 edge: offset is 542 for Jan-Mar.
3. **Thai numerals** (๐–๙) — NFKD/NFKC do NOT convert Thai digits; explicit mapping required.
   Normalize on ingest, index both forms for BM25.
4. **Mixed born-digital/scanned PDFs at BOT** — scanned share unknown. Vision OCR + `th` language
   hint (already integrated); Tesseract NOT viable for Thai (CER >75%); EasyOCR usable as
   fallback (8.6% CER on born-digital).
5. **OCS TLS cert chain incomplete** — needs InsecureSkipVerify or manual cert pinning.

## Deltas from the shared core

| Area | Thailand | Work |
|---|---|---|
| Structure | OCS JSON API (Acts); BOT PDFs | มาตรา/ข้อ/วรรค parser (new; วรรค uses ordinal words not numbers) |
| Validity/relations | OCS: `state` field + `childrens` subordinate links; BOT: supersession prose | OCS state maps to config; BOT BNM-style inference |
| Scope vocab | new Thai seed (`scope_term_th.csv`) | research + seed |
| Retrieval | segmentation problem above | lexical-arm design decision + TH router profile |
| Source access | OCS JSON API; BOT ASP.NET WebForms | OCS client (easy); BOT `__doPostBack` session handler |

## Recommended build order

1. **OCS** (Acts) first — JSON API, no protection, all target Acts confirmed, easiest.
2. **BOT** (notifications) — primary regulatory source, ASP.NET scraping but no WAF.
3. **ETDA** — zero crawl friction, high relevance (Digital ID, e-transactions).
4. **SEC** — search subdomain open, PDF download needs browser headers.
5. **Royal Gazette** — defer to Phase 2 (Cloudflare on HTML; PDFs open by sequential ID).

## Phased plan

Playbook template with one extra gate: **the lexical/segmentation decision (Hard part 1) is
settled before Phase 6 (index/serve)**. Parser-spike flagship: PDPA B.E. 2562 (OCS JSON API).
Deploy = `tomyum` DB + ECS container + CloudFront distribution + domain.

**External resource:** PyThaiNLP/thai-law on HuggingFace has 42,755 laws scraped from old
krisdika.go.th (Parquet) — potential bootstrap/validation source.
