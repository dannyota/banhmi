# Thailand jurisdiction (tomyum) — design

**Status: PROPOSED (2026-07-02). Not built; sources are CANDIDATES (not yet live-verified).** Extends
banhmi to **Thai banking digital/technology regulation** per the shared [`PLAYBOOK.md`](PLAYBOOK.md).
**Heaviest language work of the planned countries** — see *Hard parts* before scheduling.

## Proposal

- **Codename / endpoint:** `tomyum` / `tomyum.danny.vn` — *pending maintainer sign-off*
  (alternatives: `padthai`, `somtam`).
- **Language:** **Thai** — the binding legal language. Krisdika publishes EN translations that are
  explicitly **non-binding → never indexed** (playbook policy).
- **Scope:** the shared topical scope, Thai jurisdiction.

## Candidate sources (unverified — spike first)

| VN analog | Candidate | What it provides |
|---|---|---|
| SBV portal (regulator) | **BOT** — `bot.or.th` | Bank of Thailand **notifications/circulars** (ประกาศ ธปท.) — IT risk (e.g. SorNorSor 21/2562 line), cyber resilience, e-payment rules under the Payment Systems Act. PRIMARY. |
| VBPL (national law DB) | **Krisdika** — `krisdika.go.th` (Office of the Council of State) | **Consolidated Acts**: FIBA B.E. 2551 (2008), Payment Systems Act B.E. 2560 (2017), PDPA B.E. 2562 (2019), Cybersecurity Act B.E. 2562 (2019), Electronic Transactions Act B.E. 2544 (+amendments). |
| Công Báo (gazette) | **Royal Gazette** — `ratchakitcha.soc.go.th` | New-law/notification publication signal. |
| Scoped extras | **PDPC** (Office of the Personal Data Protection Committee) · **ETDA** `etda.or.th` · **SEC** `sec.or.th` | PDPA sub-regulations; e-transaction standards; securities-sector IT guidelines — all scoped to tech-in-finance. |

**Verification spike must prove:** BOT notification listing + PDF contract, Krisdika consolidated-text
format (HTML vs PDF), gazette feed shape, robots/ToS + bot protection, and the born-digital share
(older gazette pages are scans).

## Citation model

Acts: **มาตรา** (Section) → **วรรค** (paragraph) → **(๑)(๒)** items; BOT notifications number by
**ข้อ** (clause) — two label sets, like VN's Luật vs Thông tư. Native Thai labels render verbatim
(`มาตรา 5`, `วรรคสอง`, `ข้อ 3`).

## Hard parts (why TH is recommended last)

1. **No inter-word spaces in Thai script** → the BM25 sparse tokenizer (whitespace-based hashing)
   produces garbage terms. Options, decide at build time: a Go dictionary segmenter (newmm-style),
   ICU segmentation, or a **character-n-gram lexical arm**; interim fallback = router goes
   vector-primary for TH (dense BGE-M3 handles Thai natively).
2. **Buddhist Era dates** everywhere (B.E. = CE + 543) → validity normalization must convert; mixed
   B.E./C.E. in metadata is a data-quality trap.
3. **Thai numerals** (๐–๙) appear in gazette text and citations → normalize for numbers/labels.
4. OCR: EasyOCR supports `th`, but scanned-gazette accuracy on Thai script is unproven for us →
   spike on real scans.

## Deltas from the shared core

| Area | Thailand | Work |
|---|---|---|
| Structure | Krisdika consolidated text; BOT PDFs | มาตรา/ข้อ parser (new); two document families |
| Validity/relations | Krisdika consolidations carry amendment history; BOT supersession from prose | parse timelines; BNM-style inference for BOT |
| Scope vocab | new Thai seed (`scope_term_th.csv`) | research + seed (sub-agent task) |
| Retrieval | segmentation problem above | lexical-arm design decision + TH router profile |

## Phased plan

Playbook template with one extra gate: **the lexical/segmentation decision (Hard part 1) is settled
before Phase 6 (index/serve)**. Parser-spike flagship suggestion: PDPA B.E. 2562. Deploy = `tomyum`
DB + Cloud Run + domain.
