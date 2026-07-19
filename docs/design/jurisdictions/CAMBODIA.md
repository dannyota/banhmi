# Cambodia jurisdiction (amok) — design

**Status: v0.6.0 — next build. Sources live-verified 2026-07-17** (via SOCKS5 Cambodia proxy for
NBC; direct for SERC/ODC/CDC). Extends banhmi to **Cambodian banking & financial regulation and
technology law** per the shared [`PLAYBOOK.md`](PLAYBOOK.md). **English-first corpus** — NBC
publishes official English translations for ~75% of documents.

## Basics

- **Codename / endpoint:** `amok` / `amok.danny.vn`
- **Language:** **English** — NBC, SERC, CDC all publish English translations. English is the
  working language for international banking compliance in Cambodia. Khmer is the constitutionally
  binding language; English translations are explicitly "unofficial" but routinely published.
- **Scope:** the shared topical scope, Cambodian jurisdiction.

## Sources (live-verified 2026-07-17)

| Source | URL | What it provides | Bot protection | Crawl difficulty |
|---|---|---|---|---|
| **NBC** | www.nbc.gov.kh | Banking laws (10), Prakas & circulars (~214, ~163 EN), IT guidelines (2), Banking Code compilations (4) | **CloudFront geo-block** — needs SOCKS5 Cambodia residential proxy + browser UA | **3/5** |
| **SERC** | serc.gov.kh | Securities laws (4 EN), Prakas (30 EN), Guidelines (8 EN), Anukrets (6 EN) — ~49 English docs | Cloudflare UA check (trivial) | **2/5** |
| **CDC** | cdc.gov.kh | 177 English law PDFs (30 financial) — foundational laws pre-2011 | Cloudflare (trivial) | **1/5** |
| **ODC** | data.opendevelopmentcambodia.net | CKAN API: 3,024 laws, 193 banking-adjacent, bilingual PDFs | **None** (open API) | **1/5** |

### API / access contracts (verified 2026-07-17)

- **NBC:** Static PHP site. Prakas page uses jQuery UI accordion (13 categories). Prakas
  search via POST pagination (17 pages × 10). PDFs at predictable paths:
  - English Prakas: `/download_files/legislation/prakas_eng/*.pdf`
  - English Laws: `/download_files/legislation/laws_eng/*.pdf`
  - IT Guidelines: `/download_files/publication/itguideline_eng/*.pdf`
  - Banking Code: `/download_files/legislation/banking_code_*.pdf`
  JS redirect trap (delayed redirect to serc.gov.kh) — must block external navigation.
  **Proxy:** SOCKS5 residential Cambodia IP required (CloudFront blocks datacenter IPs too).
  Env var `BANHMI_NBC_PROXY_URL` — same pattern as OJK/VN proxy.
- **SERC:** Korean-style BBS at `/boards/index.php?bid={boardID}`. 10 board IDs (5 EN + 5 KH).
  PDFs at `/boards/data_dir/{boardID}/{fileID}.pdf`. No API, but regular HTML structure.
  Curl with Chrome UA works (no JS challenge).
- **CDC:** Single static HTML page at `/laws-and-regulations/`. 177 PDFs, all English. Also
  has WP REST API (`/wp-json/wp/v2/media`). Pre-2011 only (stale for modern fintech).
- **ODC:** CKAN REST API `GET /api/3/action/package_search?q=banking&rows=100`. Rich metadata
  (title, doc_number, date, status, taxonomy, bilingual PDFs). CC-BY-SA-4.0. Supplementary
  (thin on NBC prakas — only 2 found).

### Doc number formats

- **NBC Prakas:** No consistent numbering. Some have `B7.026.200` style, others descriptive.
  Date + title slug is the practical identifier.
- **SERC:** No doc numbers in HTML. Board ID + entry ID is the key.
- **CDC:** Filename-based (WordPress upload path).

## Citation model

Cambodian law uses **អនុច្ឆេទ** (Article) as the primary provision unit. English translations
render as `Article N`. Sub-articles use numbering `(1)`, `(2)` — same family as SG/MY
(Section/Subsection). NBC Prakas cite by **Article** or **Clause** (ข้อ/clause numbering).

## Hard parts

1. **NBC geo-block** — CloudFront blocks all non-Cambodia IPs including ASEAN cloud instances
   (AWS Singapore, Bangkok, GCE Jakarta all tested and blocked). Requires a SOCKS5 Cambodia
   residential proxy. Free public proxies are ephemeral — design for fallback.
2. **No consolidated legal database** — no JDIH-equivalent. Documents scattered across NBC,
   SERC, CDC, ODC. Multiple sources must be wired.
3. **Regulatory gaps** — Cybersecurity law and Personal Data Protection Law are still drafts
   (not enacted). The technology law portfolio is thinner than VN/MY/ID/TH/SG.
4. **NBC JS redirect trap** — delayed JavaScript redirects to serc.gov.kh on many pages.
5. **Stale CDC corpus** — only covers laws pre-2011. Modern fintech regulation not available.

## Deltas from the shared core

| Area | Cambodia | Work |
|---|---|---|
| Structure | NBC Prakas PDFs; SERC board PDFs; CDC law PDFs | Article-based parser (MY/SG family) |
| Validity/relations | NBC Banking Code compilations track amendments | parse compilation; no structured API |
| Scope vocab | new English seed (`scope_term_kh.csv`) | research + seed (banking, payment, AML, fintech) |
| Retrieval | English | MY/SG router profile reuses |
| Source access | NBC needs SOCKS5 proxy; SERC Cloudflare trivial; CDC/ODC open | NBC proxy via `BANHMI_NBC_PROXY_URL` |

## Recommended build order

1. **CDC** (foundational English laws) — trivial, single HTML page, 30 financial PDFs
2. **SERC** (securities) — 49 English docs, regular BBS structure
3. **ODC** (supplementary) — CKAN API, cross-reference metadata
4. **NBC** (primary banking corpus) — needs proxy, largest document set (~185 English)

## Phased plan

Playbook template. English-first corpus. Deploy = `amok` DB + ECS container + CloudFront
distribution + domain + `golden_kh.json` eval gate.

**Estimated effort:** 2-3 weeks. Smallest corpus (~300-400 docs) but most source diversity
(4 sources vs VN's 4 but with NBC geo-block complexity). NBC proxy is the infrastructure gate.
