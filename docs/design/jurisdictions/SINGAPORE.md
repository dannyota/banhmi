# Singapore jurisdiction (kaya) — design

**Status: PROPOSED (2026-07-02). Not built; sources are CANDIDATES (not yet live-verified).** Extends
banhmi to **Singapore banking & financial regulation and technology law** per the shared
[`PLAYBOOK.md`](PLAYBOOK.md). **Likely the cheapest build** — English corpus + the MY citation family
+ the best-structured statute source of the planned countries.

## Proposal

- **Codename / endpoint:** `kaya` / `kaya.danny.vn` — *pending maintainer sign-off*
  (alternatives: `chilicrab`, `bakkutteh` — longer).
- **Language:** **English** — the binding legal language (native, like MY).
- **Scope:** the shared topical scope, Singapore jurisdiction.

## Candidate sources (unverified — spike first)

| VN analog | Candidate | What it provides |
|---|---|---|
| SBV portal (regulator) | **MAS** — `mas.gov.sg` | **Notices (binding) + Guidelines**: TRM Guidelines (2021), Cyber Hygiene Notices (655 family / FSM-N under FSMA), outsourcing & third-party guidelines, BCM Guidelines, Payment Services notices (PSN…). Clean modern HTML + structured metadata + PDFs. PRIMARY. |
| VBPL (national law DB) | **SSO** — `sso.agc.gov.sg` (Singapore Statutes Online, AGC) | **Consolidated Acts + subsidiary legislation in HTML** — the only VBPL-grade HTML provision tree among the planned countries: Banking Act 1970, FSMA 2022, Payment Services Act 2019, PDPA 2012, Cybersecurity Act 2018, ETA 2010. |
| Scoped extras | **PDPC** — `pdpc.gov.sg` · **CSA** — `csa.gov.sg` | PDPA advisory guidelines; CII codes of practice — scoped to tech-in-finance. |

**Verification spike must prove:** SSO **bot protection + ToS** (known to be strict — the BNM
WAF-mint pattern may apply; compliance check is part of the spike), the SSO HTML tree shape, MAS
listing/metadata/download contract, and validity signals (SSO version dates; MAS "last revised" +
supersession notes — BNM-style weak status).

## Citation model

Acts: `Part → **Section** → **Subsection** "(1)" → **Paragraph** "(a)"` — the **same family as MY**,
so the MY parser/chunker path near-reuses. MAS Notices/Guidelines cite by **paragraph number**
(e.g. "para 4.2") → one new small parser for notice-style documents.

## Deltas from the shared core

| Area | Singapore | Work |
|---|---|---|
| Structure | SSO **HTML tree** (best case); MAS PDFs | SSO HTML→tree parser (VBPL-like, new but easy); MAS notice-paragraph parser |
| Validity/relations | SSO consolidation dates + revised editions; MAS supersession prose | map via `config.validity_status`; BNM-style inference for MAS |
| Scope vocab | reuse the MY English seed as the base (`scope_term_sg.csv`): + TRM, FSMA, MAS Notice, e-payments, digital bank licence, … | adapt + seed |
| OCR | modern corpus, born-digital | minimal; EasyOCR `en` floor exists |
| Retrieval | English | MY router profile reuses |

## Risks / open questions

- **SSO scraping posture** — bot protection and terms of use must be verified and respected before
  any crawl; this is the main go/no-go for the statute source.
- **MAS instrument taxonomy** — Notices (binding) vs Guidelines (non-binding but supervisory) vs
  Circulars: validity/quality badging must reflect the instrument class, not just dates.
- **FSMA migration** — instruments are moving from sector Acts to FSMA; supersession mapping needs
  care.

## Phased plan

Playbook template. Parser-spike flagship suggestion: Payment Services Act 2019 (SSO HTML) + the TRM
Guidelines PDF (MAS). Deploy = `kaya` DB + ECS container + CloudFront distribution + domain.
