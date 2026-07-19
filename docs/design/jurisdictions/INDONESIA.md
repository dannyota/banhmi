# Indonesia jurisdiction (rendang) — design

**Status: LIVE — revived 2026-07-12 (`rendang.danny.vn/mcp`)**, served as the third container on the
AWS read path. Was previously live 2026-07-06 → decommissioned 2026-07-11; the corpus is archived in
RDS snapshot `banhmi-pre-rendang-drop-20260711` (deleted 2026-07-13; corpus rebuilt from source).

**Source set: `bpk` + `bi` + `ojk` + `ojkweb`.** Only **jdih.ojk.go.id** geo-drops foreign IPs, so
the `ojk` (JDIH JSON API) source needs `BANHMI_OJK_PROXY_URL`. **www.ojk.go.id serves listings,
details, and PDFs directly** (verified 2026-07-16 from a Malaysian egress), so `ojkweb` (SharePoint
catalogue scraper) is **always enabled and runs direct**, using the proxy only when the env var is
set. `ojkweb` provides the full POJK/SEOJK catalogue including types that the JDIH API misses; its
short on-site numbers are canonicalized to BPK's short-code format so POJK/SEOJK doc_keys converge
with `bpk`/`ojk` observations (see `pkg/ingest/ojkweb` `canonicalNumber`). All four ID sources now
normalize to the same doc_type (lowercase short codes: `pojk`, `pbi`, `padg`, etc.) and doc_number
(e.g. "POJK 21/2023", "PBI 10/2025") so cross-source dedup merges correctly in silver. BPK provides
the sweep for non-OJK regulation types (UU/PP/Perpres/PMK/BSSN/LPS/PPATK/Kominfo/Komdigi).

> **BPK discovery is sweep-only** (keywords removed 2026-07-13 — they polluted the corpus).
> Scope is issuer-based for regulator types (POJK/SEOJK/BSSN/LPS/PPATK: in-scope by construction)
> and vocabulary-filtered for general national-law types (UU/PP/Perpres/PMK/Kominfo/Komdigi).

## Decisions locked

- **Codename / endpoint:** `rendang` / `rendang.danny.vn`.
- **Language:** **Indonesian (Bahasa Indonesia)** — the binding legal language. OJK/BI publish some EN
  renditions; **non-binding → never indexed** (playbook policy).
- **Scope:** the shared topical scope, Indonesian jurisdiction. Note the regulator **split**: OJK
  supervises banks; **Bank Indonesia owns payment systems** — both are in scope.

## Sources — verification spike (verified live 2026-07-04)

**Headline finding (2026-07-04): OJK and peraturan.go.id geo-fenced to Indonesian IPs** — their ASNs
dropped non-ID TCP from every egress tried; Google indexed zero pages of `jdih.ojk.go.id`.
**peraturan.bpk.go.id (BPK's JDIH) replaced both** — it carries UU/PP/Perpres **and**
POJK/SEOJK/PBI with status relations.
**Update 2026-07-09:** `jdih.ojk.go.id` and `jdih.komdigi.go.id` became **reachable** from outside
Indonesia (`peraturan.go.id` still blocked).
**Structure spike (2026-07-12, live-verified) — revival source set decided:**
- **ojk (NEW source — build):** `jdih.ojk.go.id` is a clean jQuery-DataTables JSON API
  (`POST /Web/ViewPeraturan/ListDataPeraturan`, `start`/`length` paging, `jenisPeraturan` 01=UU
  12 · 06=POJK 560 · 09=SEOJK 407; optional `sektor`, title filter via
  `ListDataPeraturanSeacrhByFilter`). Detail pages expose gazette refs (LN/TLN), sector/
  classification taxonomy, explicit relations (Mencabut / Dicabut oleh / Dasar Hukum, linked by
  UUID) and granular status — Berlaku / Tidak Berlaku / **Berlaku (Dicabut Sebagian)** (partial
  repeal maps onto our validity model). Direct ungated PDF at
  `/Web/ViewPeraturan/DownloadDokumen/{UUID}`, born-digital. robots.txt 404 (no restrictions),
  F5 BIG-IP LB only, no WAF challenge. **Authoritative origin for POJK/SEOJK — richer than the
  bpk mirror.** Sweep-all (regulator-specific source).
- **komdigi (REJECTED as a source):** `jdih.komdigi.go.id` hosts only ministerial products (~741 docs,
  ~50–80 tech-law relevant), none of the parent laws; `robots.txt` **disallows the download
  paths, blocks AI crawlers, and sets Crawl-delay 10** — we crawl politely, so no text files.
  **Resolved 2026-07-13:** its Permen now reach the corpus **via bpk**, which hosts them — the sweep
  covers jenis **106 (Kominfo)** and **278 (Komdigi)**, so Permenkominfo 5/2020 (PSE),
  Permenkomdigi 5/2025 etc. are discovered without crawling komdigi itself.
- **bpk keywords: REJECTED (2026-07-13) — bpk is sweep-only.** Keyword slices were built, shipped, and
  **removed**: they polluted the corpus (see the status note above and
  [`SOURCES.md`](../SOURCES.md#bpk-discovery-id--sweep-only)). `Discover` now returns an error on a
  non-empty keyword, and bpk has no `discovery_keyword.csv` rows.
- `peraturan.go.id` remains blocked (TCP timeout, re-verified 2026-07-12).

| Candidate | Verdict | Key facts |
|---|---|---|
| **jdih.ojk.go.id** (+ www.ojk.go.id) | **VERIFIED** (reachable 2026-07-09; jdih geo-fenced — needs Jakarta proxy; www reachable directly) | `ojk` (JDIH JSON API) needs `BANHMI_OJK_PROXY_URL`; `ojkweb` (SharePoint scraper) runs **direct** (proxy optional) |
| **peraturan.go.id** (JDIHN) | **BLOCKED — geo-fenced** | TCP timeout locally, ECONNREFUSED from US; no public JDIHN API found |
| **jdih.bi.go.id** | **VERIFIED** | clean JSON API, no bot protection, descriptive UA works everywhere |
| **peraturan.bpk.go.id** *(new — replacement)* | **VERIFIED-WITH-CAVEATS** | Cloudflare challenge on HTML (mint-and-reuse proven); PDFs plain HTTP; comprehensive incl. POJK+SEOJK |
| **jdih.komdigi.go.id** *(optional)* | **VERIFIED-WITH-CAVEATS** | no WAF; `Crawl-delay: 10`; full text inline HTML; weak asymmetric relations |

Evidence: `data/spike_id/{bi,peraturan,komdigi,ojk}/` (listings, detail HTML/JSON, born-digital PDFs).

### jdih.bi.go.id — verified fetch contract (PBI/PADG, payments)

- **Discovery:** `GET /Web/DaftarPeraturan` returns **all 1,523 regulation cards in one HTML** (4.3 MB);
  `POST` same URL with `ddjenisperaturan` filters server-side (1=PBI **623**, 2=PADG **259**,
  3=SE Ekstern **637**, 4=UU 4). Sitemap (2,077 URLs) + `GET /api/WebJDIH/GetDataStatistikProdukHukum`
  for incremental checks.
- **Per-doc metadata (JSON):** `GET /api/WebJDIH/GetDataWebPeraturan?PeraturanID={id}` → number, title,
  taxonomy, dates (penetapan/pengundangan/berlaku), gazette ref, `Status`, and relation fields
  `Mengubah`/`Mencabut`/`Diubah`/`Dicabut` + `PeraturanTerkait` (semicolon-delimited numbers).
  `GET /api/WebJDIH/GetIDByNoPublikasi?nopublikasi=` resolves number → PeraturanID.
- **Relations: strong forward, weak reverse.** Forward (`Mengubah`/`Mencabut` on the newer doc) is
  reliable; reverse often unpopulated → build the graph from forward edges, compute reverses.
- **Download:** `GET /api/WebJDIH/DownloadFilePeraturan/{id}` — born-digital proven (PBI 10/2025, 2.6 MB).
- **robots:** permissive (only `/api/Authenticate/`, `/api/secure/` blocked); ToS page empty.
- **Caveats:** repealed docs can still report `Status: "Berlaku"` in the API (listing badge is more
  accurate); number formats vary (`22/23/PBI/2020` vs `PBI No.10 Tahun 2025` vs `PBI Nomor 5 Tahun 2026`)
  — all normalized by `normalizeNumber` to BPK short form (e.g. "PBI 10/2025", "PADG 15/2024").

### peraturan.bpk.go.id — verified fetch contract (national DB + POJK/SEOJK backstop)

- **Bot gate:** Cloudflare Managed Challenge on HTML pages. **Mint-and-reuse proven locally
  (2026-07-04):** headless Chrome passes the challenge; replaying the **full cookie set**
  (`cf_clearance` + `__cf_bm` + `_cfuvid` + TS) with the matching UA in plain curl → 200 on listing +
  detail. `cf_clearance` alone → 403. `__cf_bm` lives ~30 min → **re-mint periodically** (the BNM
  chromedp pattern, different trigger). **PDF `/Download/` paths need no cookies at all.**
- **Discovery — sweep-only:** `GET /Search?jenis={code}&p={n}[&tahun={y}…]` — server-rendered listing
  (no XHR API) with detail links, PDF links, and inline status relations. The sweep walks **all 13
  jenis**; `tahun` (multi-value, server-side) windows the incremental crawl.

| Scope rule | Jenis codes |
|---|---|
| **In scope by issuer** | 80 POJK (503) · 212 SEOJK · 78 PBI (fallback; BI is primary) · 54 BSSN · 83 LPS · 81 + 221 PPATK |
| **Vocabulary-filtered** | 8 UU (1,926) · 10 PP (4,991) · 11 Perpres · 42 PMK · 106 Kominfo · 278 Komdigi |

- **⚠️ Never use BPK's search filter as a scope decision** (verified live 2026-07-13). Two reasons:
  1. **It silently ignores an unrecognized param.** The real fields are `keywords=` (full text) and
     `tentang=` (title) — **not** `keyword=`. `/Search?jenis=8&keyword=bank%20indonesia` returns
     **1,926** rows (the whole UU listing) where `&keywords=` returns 573. A wrong param name returns
     *everything* instead of erroring.
  2. **It OR-matches multi-word terms** — `bank indonesia` matches any title containing *indonesia*.

  Because the pipeline skips `scope.Match` for keyword slices, this admitted the entire listing as
  in-scope. Hence sweep-only; `Discover` rejects a keyword.
- **Per-doc metadata:** `/Details/{id}/{slug}` → type, title, T.E.U., dates ×3, gazette ref (`Sumber`),
  `Subjek`, judicial-review notes (Uji Materi), file list, and **STATUS PERATURAN** — typed, hyperlinked
  relations ("Dicabut sebagian dengan", "Diubah dengan") down to **Pasal granularity** (UU 11/2008 ITE
  is the showcase).
- **Relations caveat:** status blocks are incomplete on newer docs (UU 27/2022 = "Belum Tersedia";
  PP 71/2019 = absent) → treat as enrichment, not guaranteed; BI API covers PBI relations.
- **Download:** `/Download/{file_id}/{name}.pdf` (file_id ≠ detail id; parse from the detail page) —
  born-digital proven (UU 27/2022, 5.8 MB, text layer).
- **robots/ToS:** `Allow: /` for `*`; Content-Signal `search=yes, ai-train=no, use=reference`; named AI
  bots (ClaudeBot/GPTBot/…) blocked — the `banhmi/0.1` UA is not matched; homepage states free public
  access without conditions.

### jdih.komdigi.go.id — verified fetch contract (optional; PSE/PDP scope only)

- **Discovery:** `GET /produk_hukum/kategori/{id}[/{year}]` (Permen=7: 498 docs; PM KOMDIGI=22; SE=11;
  ~742 total) + `/produk_hukum/inventarisasi/{year}` (referenced UU/PP; PP 71/2019 confirmed) +
  8 curated `/telusur/` topics. No sitemap, no JSON API; server-rendered HTML.
- **Per-doc:** **full regulation text inline in the detail HTML** (canonical text without PDF
  extraction — a strength); dates; `STATUS` badge (BERLAKU/TIDAK BERLAKU); `KETERANGAN STATUS` carries
  "Dicabut: X" (linked) on the revoked side only; related-docs table has no relation type.
- **Download:** `/produk_hukum/unduh/` is **robots-disallowed**; `/pratinjau/id/{id}` serves the same
  PDF and is allowed — prefer the inline HTML body as text source anyway (policy: respect robots intent).
- **Crawl:** `Crawl-delay: 10` → full discovery ≈ 2.2 h; acceptable for an optional scoped source.

## Source-set decision — DECIDED (2026-07-04; OJK added 2026-07-13)

**Current: BPK + BI + OJK (`ojkweb` direct; `ojk` needs the proxy).**

| Source package | What it provides | Fetch client |
|---|---|---|
| **`bpk`** (`peraturan.bpk.go.id`) | **PBI (fallback) + BSSN + LPS + PPATK** (in scope by issuer) and **UU/PP/Perpres/PMK/Kominfo/Komdigi** (vocabulary-filtered) + status relations — 13 jenis, sweep-only | `pkg/fetch.Client` with `CloudflareMinter` (proven 2026-07-04: 3s mint, cookie reuse → 200) |
| **`bi`** (`jdih.bi.go.id`) | PBI (623) + PADG (259) + SE + relation fields | `pkg/fetch.Client` with no minter (plain Chrome UA + utls; proven 200 on API) |
| **`ojk`** (`jdih.ojk.go.id`) | POJK + SEOJK metadata + relation/status detail via JDIH JSON API | `pkg/fetch.Client` via `BANHMI_OJK_PROXY_URL` (host geo-drops foreign IPs; proxy required) |
| **`ojkweb`** (`ojk.go.id`) | Full **POJK + SEOJK** catalogue (SharePoint scraper, born-digital PDFs) | `pkg/fetch.Client` with `OJKMinter`, **direct** (www.ojk.go.id reachable from foreign IPs, verified 2026-07-16; proxy optional) |
| **`komdigi`** (`jdih.komdigi.go.id`) | **not needed** — bpk's Kominfo/Komdigi jenis cover the same Permen | plain HTTP, `Crawl-delay: 10` |

OJK sources are the authoritative origin for POJK/SEOJK. BPK still sweeps all 13 jenis for non-OJK
types. Only jdih.ojk.go.id geo-drops foreign IPs (www.ojk.go.id does not).
BPK's Cloudflare Turnstile blocks the proxy (residential IP required), so BPK ingest is local-only.

**Client: `pkg/fetch`** — shared browser-impersonating HTTP package (added 2026-07-04): utls Chrome
TLS fingerprint (h1/h2 auto) + chromedp cookie minting (CloudflareMinter for BPK, AWSWAFMinter for
BNM). Replaces BNM's inline chromedp and is reused by all WAF'd Indonesian sources.

## Citation model

`BAB (chapter) → Bagian → Paragraf → **Pasal** (article) → **ayat** (clause, "(1)") → **huruf**
(point, "a.") → angka` — structurally the **closest to VN** (Pasal/ayat/huruf ≈ Điều/Khoản/Điểm), so
the VN chunk-walk pattern (article-level chunks, clause/point descent, appendix = *Lampiran*) should
generalize cheaply. Native labels: `Pasal 5`, `ayat (1)`, `huruf a`.

## Deltas from the shared core

| Area | Indonesia | Work |
|---|---|---|
| Structure | born-digital PDFs (BPK/BI, proven) + Komdigi inline HTML | Pasal parser (Markdown/PDF text → tree); expect VN-parser reuse with new label regexes |
| Validity/relations | BPK STATUS PERATURAN (typed, linked, Pasal-level) + BI JSON relation fields | map via `config.validity_status`; forward-edge graph |
| Cloudflare mint | BPK HTML needs challenge cookies | reuse the BNM chromedp mint-and-reuse client (~30-min re-mint) |
| Scope vocab | Indonesian seed (`scope_term_id.csv`): topical terms (keamanan siber, pelindungan data pribadi, teknologi informasi, sistem elektronik, komputasi awan, alih daya, tanda tangan elektronik, perbankan digital, QRIS, …) **+ issuer terms** (below) | seeded |
| **Scope by issuer** | **Regulator-issued = in scope by construction.** OJK, BI, LPS, PPATK, BSSN are bodies whose *entire* mandate is banking-finance or cybersecurity, so `scope_term_id.csv` carries their codes as **strong** terms — `pojk`, `seojk`, `lps`, `ppatk`, `bssn`, `pbi nomor`, `padg nomor`, plus the spelled-out `peraturan otoritas jasa keuangan`, `surat edaran otoritas jasa keuangan`, `peraturan bank indonesia`, `peraturan anggota dewan gubernur`. The matcher admits them on the **document number alone** (e.g. `POJK 30/2024`); no topical term is needed. **Broad-mandate issuers** (UU, PP, Perpres, PMK, Kominfo, Komdigi — they also cover agriculture, customs, broadcast) stay **vocabulary-filtered**. | added 2026-07-13 |
| OCR | older regs are scans (share unknown; UU 27/2022 + PBI 10/2025 + PP 82/2012 all born-digital) | Vision OCR (default) / EasyOCR `id` (fallback) |
| Retrieval | Latin script, space-delimited | lexical arm works as-is; router profile like VN's |

## Risks / open questions

- **Geo-fence** — resolved: BPK for non-OJK types; `ojk` (JDIH) via the GCE Jakarta proxy; `ojkweb` runs direct (www.ojk.go.id reachable from foreign IPs).
  BPK's Cloudflare Turnstile blocks the proxy (residential IP required), so BPK runs from a local machine.
- **BPK freshness lag** vs OJK/BI publication — mitigated by the direct `ojkweb` source for POJK/SEOJK.
- **BPK relation completeness** — new docs often "Belum Tersedia"; relations are enrichment.
- **JDIH fragmentation confirmed:** three different engines (BI = SPA + JSON API; BPK = ASP.NET +
  Cloudflare; Komdigi = server HTML) → three fetch contracts, as designed (one source package each).
- **Scan share in older regs** — unknown until corpus-scale extraction; all spike PDFs were born-digital.

## Phased plan

1. ✅ **Verify sources (spike done 2026-07-04).** All candidates live-verified; source set decided.
2. ✅ **Pasal parser (coded + validated on UU 27/2022, 2026-07-04).** `ParseIndonesianUU` in
   `pkg/pipeline/indonesiaparse.go` mirrors the MY parser architecture. BPK PDFs are
   **scanned+OCR'd** (zero font metadata, systematic digit confusion); the parser implements:
   - Pasal heading fuzzy regex `^Pasa[l17]\s*([0-9OoIlT]+)` + OCR fixup (`O→0`, `I/l→1`, `T→7`,
     strip dots/spaces) + **monotonic filter** (accept only if `num == last+1`).
   - `BAB [IVXLCDM]+` (tolerates `BABVIII`), Bagian (spelled ordinals), Paragraf, `^(N)` ayat,
     `^[a-z].` huruf; PENJELASAN split (binding main text vs non-binding notes); LAMPIRAN; no-BAB docs;
     OCR noise strip (SK-No watermarks, PRESIDEN/REPUBLIK header fragments, page markers).
   - **Measured on UU 27/2022:** Pasal 1–76 (0 gaps/dupes), **16 BAB** (the earlier "13" here was
     wrong), 120 ayat, 140 huruf, 1 penjelasan. Integration test runs pdftotext on the spike PDF and
     skips cleanly when absent.
3. ✅ **Seam config (coded 2026-07-04).** Registry entry `id` (DB `rendang`, parser `id-uu`,
   ParagraphLabel `Alinea`, OCR `id`); `scope_term_id.csv` (120 terms, calibrated against real spike
   titles); validity (`BERLAKU`/`TIDAK BERLAKU` per-source) + Indonesian relation types seeded; silver
   `kind` CHECK extended (migration `00006_update.sql`).
4. ✅ **Sources (coded 2026-07-04; discovery reworked to sweep-only 2026-07-13).** `pkg/ingest/bpk`
   (Cloudflare via `pkg/fetch.CloudflareMinter`; listing + detail + STATUS PERATURAN relations + PDF)
   and `pkg/ingest/bi` (JSON API; PBI + PADG; **forward-edge relations only** — the API self-reports
   repealed docs as "Berlaku"). `komdigi` not needed (bpk covers its Permen).
   **BPK discovery is a sweep over all 13 jenis, tahun-windowed incremental** — no keyword slices
   (`Discover` rejects a keyword; BPK's search filter is untrustworthy, see the fetch contract above).
   Verified live: full scan 7,445 docs/830 pages ≈ 18.5 min; incremental ≈ 48 s. Probed and ruled out:
   page-size params (fixed 10/page), sitemap (404), sort params, hidden JSON API. `tahun=` filters
   server-side (multi-value); `tema=` (124-theme taxonomy) exists but is NOT used for scope — recall
   depends on BPK's manual tagging. Cards carry a year-granularity `PublishedAt` watermark; clear the
   discover cursor to force a full rescan (BPK backfills old years). **Coverage gap: jenis 212 (SEOJK)
   holds only 25 docs** — BPK's SEOJK coverage is thin; surface via `quality_gaps`.
5. ✅ **Extract → Normalize (coded 2026-07-04).** Parser wired by jurisdiction; validity from BPK/BI
   status via `config.validity_status`; forward-edge graph.
6. ✅ **Index + serve (coded 2026-07-04).** Chunker walks pasal/ayat/huruf with Indonesian citation
   labels ("Pasal 26, ayat (1), huruf a"); `rendang` MCP brief; `golden_id.json` (31 cases — doc
   numbers must be re-verified against real gold rows during local validation).
7. ✅ **Validated + deployed (2026-07-06; revived 2026-07-12).** Local `rendang` corpus run + eval +
   MCP smoke, then RDS restore → ECS (AWS) + `rendang.danny.vn` domain. LIVE.
