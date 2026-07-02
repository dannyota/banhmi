# Indonesia jurisdiction (rendang) — design

**Status: PROPOSED (2026-07-02). Not built; sources are CANDIDATES (not yet live-verified).** Extends
banhmi to **Indonesian banking digital/technology regulation** per the shared
[`PLAYBOOK.md`](PLAYBOOK.md). Everything below needs the Phase-1 verification spike before build.

## Proposal

- **Codename / endpoint:** `rendang` / `rendang.danny.vn` — *pending maintainer sign-off*
  (alternatives: `satay` — shared with MY/SG; `gadogado` — long).
- **Language:** **Indonesian (Bahasa Indonesia)** — the binding legal language. OJK/BI publish some EN
  renditions; **non-binding → never indexed** (playbook policy).
- **Scope:** the shared topical scope, Indonesian jurisdiction. Note the regulator **split**: OJK
  supervises banks; **Bank Indonesia owns payment systems** — both are in scope.

## Candidate sources (unverified — spike first)

| VN analog | Candidate | What it provides |
|---|---|---|
| SBV portal (regulator) | **OJK** — `ojk.go.id` + `jdih.ojk.go.id` | **POJK/SEOJK** — bank IT & digital regs (e.g. POJK 11/POJK.03/2022 on IT by commercial banks + its SEOJK), digital banking, e-KYC. PRIMARY. |
| 2nd regulator | **Bank Indonesia** — `jdih.bi.go.id` | **PBI/PADG** — payment systems, QRIS, BI-FAST, SNAP standards, payment-service providers. |
| VBPL (national law DB) | **peraturan.go.id** (JDIHN, Kemenkumham) | **UU/PP/Perpres** with status metadata: UU 27/2022 (PDP), UU 11/2008 ITE (as amended 19/2016, 1/2024), PP 71/2019 (electronic systems), UU 4/2023 (P2SK omnibus). |
| Scoped extra | **Komdigi JDIH** (ex-Kominfo) | PSE registration + PDP implementing regs — scoped to tech-in-finance only. Optional 4th. |

**Verification spike must prove:** discovery listing + pagination per site, per-doc metadata (status:
*mengubah / diubah / dicabut* — JDIHN carries structured relations), file download (born-digital PDF
share vs scan), robots/ToS + bot protection, and the JDIH fragmentation risk (every agency runs its
own JDIH instance with a different engine).

## Citation model

`BAB (chapter) → Bagian → Paragraf → **Pasal** (article) → **ayat** (clause, "(1)") → **huruf**
(point, "a.") → angka` — structurally the **closest to VN** (Pasal/ayat/huruf ≈ Điều/Khoản/Điểm), so
the VN chunk-walk pattern (article-level chunks, clause/point descent, appendix = *Lampiran*) should
generalize cheaply. Native labels: `Pasal 5`, `ayat (1)`, `huruf a`.

## Deltas from the shared core

| Area | Indonesia | Work |
|---|---|---|
| Structure | mixed: JDIH born-digital PDFs + some HTML | Pasal parser (Markdown/PDF text → tree); expect VN-parser reuse with new label regexes |
| Validity/relations | JDIHN status fields (dicabut/diubah) + OJK/BI page metadata | map via `config.validity_status`; relations from status links |
| Scope vocab | new Indonesian seed (`scope_term_id.csv`): keamanan siber, pelindungan data pribadi, teknologi informasi, sistem elektronik, komputasi awan, alih daya, tanda tangan elektronik, perbankan digital, QRIS, … | research + seed (sub-agent task) |
| OCR | older regs are scans | EasyOCR `id` (supported) |
| Retrieval | Latin script, space-delimited | lexical arm works as-is; router profile like VN's |

## Risks / open questions

- **JDIH fragmentation** — three-plus separate portals with different engines; each needs its own
  source package and fetch contract.
- **PDF quality variance** — pre-2010 regs are often scans → OCR floor share unknown until spiked.
- **Bot protection unknown** — assume none until probed; BNM-style WAF mint is the fallback pattern.
- **Regulator overlap** — OJK vs BI issuance boundaries shifted over time (P2SK); scope vocabulary
  must carry both issuers.

## Phased plan

Follows the playbook template: 1 verify sources → 2 parser spike (suggest UU 27/2022 PDP as the
flagship) → 3 seam config + `scope_term_id.csv` → 4 `pkg/ingest/{ojk,bi,peraturan}` → 5 extract/
normalize → 6 index/serve + `golden_id.json` → 7 deploy `rendang` DB + Cloud Run + domain.
