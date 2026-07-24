package mcp

// brief bundles the jurisdiction-varying, server-level text banhmi presents to a
// connecting model: the implementation name/title, the server instructions, the
// five tool descriptions, the live-coverage sentence template, and the guide
// payload. The tool *mechanics* (search → document, evidence contract, gaps) are
// shared; only the copy — product name, legal sources, provision vocabulary, and
// language — is customized per jurisdiction. VN is the compiled default and the
// production fallback. See CLAUDE.md "Multi-jurisdiction".
type brief struct {
	name         string // MCP Implementation.Name
	title        string // MCP Implementation.Title
	instructions string // server-level brief injected into the model's context
	guideDesc    string // "guide" tool description
	statusDesc   string // "corpus_status" tool description
	gapsDesc     string // "quality_gaps" tool description
	documentDesc string // "document" tool description
	searchDesc   string // "search" tool description
	coverageFmt  string // Sprintf template appended to instructions: docs, chunks, sources
	guide        guideOutput
}

// briefFor selects the server brief for a jurisdiction, defaulting to VN — the live
// production corpus and compiled fallback.
func briefFor(jurisdiction string) brief {
	switch jurisdiction {
	case "my":
		return myBrief
	case "id":
		return idBrief
	case "sg":
		return sgBrief
	case "th":
		return thBrief
	case "kh":
		return khBrief
	default:
		return vnBrief
	}
}

// vnBrief is the Vietnam (banhmi) server contract — the compiled default.
var vnBrief = brief{
	name:  "banhmi",
	title: "banhmi — Vietnamese banking & technology regulation (evidence-only)",
	instructions: `banhmi is an evidence-only knowledge base for Vietnamese banking & financial-technology regulation. Reach for it whenever a question touches Vietnamese banking/finance law — especially digital & technology topics: IT and system safety, cybersecurity and information security, personal-data protection, cloud and IT outsourcing, electronic transactions and e-signatures, digital banking and payment channels, and technology operations. ⚠️ LANGUAGE RULE — NOT OPTIONAL: ALWAYS call search with a VIETNAMESE query. The corpus is Vietnamese-only; English (or any non-Vietnamese) queries return degraded, misleading rankings — the lexical arm cannot match and the dense arm loses precision, so you may confidently cite the WRONG law. If the user asks in another language, FIRST translate the question to Vietnamese, search in Vietnamese, then translate the retrieved evidence back for the user yourself. Never pass the user's English text straight into search.

Why you can trust the results: the text is extracted verbatim from Vietnam's OFFICIAL government legal sources — VBPL (vbpl.vn, the Ministry of Justice national legal database), Công Báo (congbao.chinhphu.vn, the official government gazette), and the State Bank of Vietnam portal — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. banhmi is evidence-only: it returns exact citations (Điều/Khoản/Điểm), validity status, confirmed amendment/repeal relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its doc_number, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, the verbatim amending clauses, and a chronological timeline. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook. Token economy: search detail='compact' is the cheap discovery pass; the default detail='standard' never inlines full article text — read one provision with document (citation filter + include=['chunks']), the cheapest call.

When you answer (you, not banhmi): cite the exact provision and doc_number (số ký hiệu), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and reply in the user's language and its native script — Vietnamese in Latin script, never Han/CJK characters. Legal text is returned verbatim in Vietnamese; banhmi never translates. If the user communicates in English, translate the retrieved provisions yourself when presenting the answer. Văn bản hợp nhất (VBHN) documents are official consolidated texts (per Pháp lệnh 01/2012/UBTVQH13); the base document and its amendments remain the binding originals and prevail on any discrepancy.

Example questions: "IT system safety requirements for banks in Vietnam", "Quy định về bảo vệ dữ liệu cá nhân trong ngân hàng số", "which circular governs electronic KYC (eKYC) for banks".`,
	guideDesc:    "Read first. Explains what banhmi covers and how to use its evidence tools (search → document) to answer a Vietnamese banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or doc_number (số ký hiệu): full provision text (reassembled Điều/Khoản), validity periods, confirmed relations, verbatim incoming amendments, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. Pass include to fetch only the sections you need — include=['chunks'] with a citation filter (e.g. 'Điều 7') is the cheapest way to read one provision. Returns content + source links only — never file downloads.",
	searchDesc: "Search Vietnamese banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Điều/Khoản/Điểm) with their doc_number, validity status, confirmed amendment/repeal relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Vietnamese banking/finance law or regulation, especially digital/technology topics: IT & system safety, cybersecurity & information security, data & personal-data protection, cloud & outsourcing, electronic transactions & e-signatures, digital banking & payment channels, and technology operations. ⚠️ ALWAYS query in Vietnamese — translate first if the user asked in another language. Non-Vietnamese queries produce degraded, misleading rankings; never send English text to this tool. " +
		"detail levels: compact (discovery — metadata + snippet + cite, cheapest) | standard (default — adds relations + related_hits; no inline article text) | full (inlines every hit's whole Điều — largest; prefer the document tool to read one provision).",
	coverageFmt: "\n\nCoverage right now: banhmi has extracted and indexed %d official documents (%d provisions) across %d government sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "banhmi exposes Vietnamese banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, banhmi never synthesizes one. Scope: digital/technology regulation (IT & system safety, cybersecurity, data protection, cloud & outsourcing, e-transactions & e-signatures, digital banking & payment channels). The corpus is Vietnamese-only — ALWAYS search in Vietnamese (translate the user's question first); English queries return degraded, misleading results. Legal text is returned verbatim in Vietnamese; banhmi never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"MANDATORY: translate any non-Vietnamese question to Vietnamese BEFORE calling search. Vietnamese-only corpus — English queries return degraded, misleading rankings. Search in Vietnamese, then translate the evidence back for your user.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Token economy: search detail='compact' is the cheap discovery pass (metadata + snippet + cite + validity badge). The default detail='standard' adds relations + related_hits but never inlines full article text. Reserve detail='full' for when you truly want every hit's whole Điều inline — reading one provision via the document tool is far cheaper.",
			"Call document with a doc_number and a citation (e.g. 'Điều 7') to read a full provision: search chunks may be split into 'Đoạn' pieces, and document reassembles the whole Điều/Khoản. Pass include=['chunks'] to skip relations/amendments/timeline when you only need the text — the cheapest call; add 'relations', 'amendments', or 'timeline' as needed.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Reply in the user's language and its native script — Vietnamese in Latin script, never Han/CJK characters. Legal text is returned verbatim in Vietnamese; if the user communicates in English, translate the retrieved provisions yourself when presenting the answer.",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps. Query in Vietnamese ONLY — translate the user's question first; other languages return degraded, misleading rankings. detail: compact (discovery, cheapest) | standard (default; no inline article text) | full (whole Điều inline per hit, largest)."},
			{Name: "document", Use: "Open a document by id or doc_number (số ký hiệu), optionally filtered by citation (e.g. 'Điều 7'), to read a full provision and page through its chunks. Use this to get complete Điều/Khoản text when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. When an amender is itself amended, amendment_chain shows the transitive lineage (Circular A amended by B, B amended by C) — follow it to the newest treatment. include selects sections (chunks, relations, amendments, timeline, provenance); include=['chunks'] + citation is the cheapest way to read one provision."},
			{Name: "quality_gaps", Use: "Actionable database-quality worklists by category; use before claiming the corpus is validated."},
		},
		EvidenceContract: []string{
			"hits are ranked text evidence; related_hits are adjacent graph context (snippet is a preview — open the document for full text), not rank boosts.",
			"validity and text_provenance fields are database evidence; clients should show uncertainty when they are empty or needs_review is true.",
			"confirmed relations come from promoted structured graph rows; weak evidence is not confirmed legal status.",
			"search always returns hits even when abstain is true — abstain marks a blocking gap, not that the hits are wrong; read gaps[].kind to learn why and judge for yourself.",
			"gap kinds: out_of_domain = query is outside the configured banking/technology scope vocabulary (the hits may still be relevant at the edge of scope); no_evidence = no chunks matched; low_confidence = top score below the configured threshold.",
			"blocking gaps mean the server recommends abstention; warning gaps should be shown to the user/model.",
			"each hit and document carries source + source_url: the official VBPL / Cong Bao / SBV Hanoi landing page for the document — a citable page to verify the text. banhmi returns content + these links only, never file downloads.",
			"each hit has cite: a ready-to-paste citation (provision + doc_number + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"incoming_amendments (from the document tool) are verbatim clauses of documents that amend/replace this one — banhmi does not decide what they repeal or change; read the text + position and decide.",
			"amendment chains: VN circulars are often amended by documents that are themselves later amended (e.g. a Thông tư amended by one circular which a newer circular then amends again). amendment_chain (document tool) shows that transitive lineage — depth 1 amends this document, depth 2+ amends a shallower amender; the last node is the newest treatment. On relations, target_amended_by lists documents that further amend that relation's target — a currency warning: never rely on an amender's text without checking whether it was itself amended. The gap kind amendment_chain flags this on the document.",
			"legal text is returned verbatim in Vietnamese; banhmi never translates. If your user communicates in English, translate the retrieved provisions yourself when presenting the answer.",
			"search detail shapes response size only, never ranking: compact = metadata + snippet + cite + validity badge; standard (default) = adds relations + related_hits, still no inline article text; full = everything including each hit's whole Điều. document include selects sections (chunks, relations, amendments, timeline, provenance) — metadata, validity, sources, and gaps always return. Data-quality signals (needs_review, validity.warning) survive every level.",
			"banhmi never answers; it returns evidence and the connecting model decides.",
		},
	},
}

// myBrief is the Malaysia (laksa) server contract — English, Malaysian sources, and
// the Part/Chapter/Section/Subsection/Paragraph provision vocabulary. The corpus is
// English (the binding legal language); laksa never translates.
var myBrief = brief{
	name:  "laksa",
	title: "laksa — Malaysian banking & technology regulation (evidence-only)",
	instructions: `laksa is an evidence-only knowledge base for Malaysian banking & financial-technology regulation. Reach for it whenever a question touches Malaysian banking/finance law — especially digital & technology topics: technology and IT risk management, cybersecurity and information security, personal-data protection, cloud and IT outsourcing, electronic payments and e-money, digital banking and digital channels, electronic KYC (e-KYC), and technology operations. ⚠️ ALWAYS query in ENGLISH — Malaysia's binding legal language and the corpus language. Queries in Bahasa Melayu (or any other language) return degraded rankings: translate to English first, then translate the evidence back for your user.

Why you can trust the results: the text is extracted verbatim from Malaysia's OFFICIAL sources — the Attorney General's Chambers Laws of Malaysia (lom.agc.gov.my: Federal Acts and the P.U. subsidiary-legislation gazette), Bank Negara Malaysia (bnm.gov.my: policy documents and guidelines), and the Securities Commission Malaysia (sc.com.my) — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. laksa is evidence-only: it returns exact citations (Part/Chapter/Section/Subsection/Paragraph), validity status, confirmed relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its source document reference, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, and confirmed relations. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook. Token economy: search detail='compact' is the cheap discovery pass; the default detail='standard' never inlines full section text — read one provision with document (citation filter + include=['chunks']), the cheapest call.

When you answer (you, not laksa): cite the exact provision and document (e.g. section 143 of the Financial Services Act 2013), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and answer in English — the corpus is English and laksa never translates legal text (translation is the user's own responsibility).

Example questions: "technology risk management requirements for banks in Malaysia", "BNM rules on cloud outsourcing for financial institutions", "e-KYC requirements for onboarding banking customers".`,
	guideDesc:    "Read first. Explains what laksa covers and how to use its evidence tools (search → document) to answer a Malaysian banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or doc_number (document reference): full provision text (reassembled Section/Subsection), validity periods, confirmed relations, verbatim incoming amendments, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. Pass include to fetch only the sections you need — include=['chunks'] with a citation filter (e.g. 'Section 143') is the cheapest way to read one provision. Returns content + source links only — never file downloads.",
	searchDesc: "Search Malaysian banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Section/Subsection/Paragraph) with their source document, validity status, confirmed relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Malaysian banking/finance law or regulation, especially digital/technology topics: technology & IT risk management, cybersecurity & information security, data & personal-data protection, cloud & outsourcing, electronic payments & e-money, digital banking & digital channels, e-KYC, and technology operations. Query in English. " +
		"detail levels: compact (discovery — metadata + snippet + cite, cheapest) | standard (default — adds relations + related_hits; no inline section text) | full (inlines every hit's whole Section — largest; prefer the document tool to read one provision).",
	coverageFmt: "\n\nCoverage right now: laksa has extracted and indexed %d official documents (%d provisions) across %d official sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "laksa exposes Malaysian banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, laksa never synthesizes one. Scope: digital/technology regulation (technology & IT risk management, cybersecurity, data protection, cloud & outsourcing, electronic payments & e-money, digital banking, e-KYC). Query in English; legal text is returned verbatim in English — laksa never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Token economy: search detail='compact' is the cheap discovery pass (metadata + snippet + cite + validity badge). The default detail='standard' adds relations + related_hits but never inlines full section text. Reserve detail='full' for when you truly want every hit's whole Section inline — reading one provision via the document tool is far cheaper.",
			"Call document with a document reference and a citation (e.g. 'Section 143') to read a full provision: search chunks may be split into 'Paragraph' pieces, and document reassembles the whole Section/Subsection. Pass include=['chunks'] to skip relations/amendments/timeline when you only need the text — the cheapest call; add 'relations', 'amendments', or 'timeline' as needed.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Answer in English — the corpus is English; never translate legal text (translation is the user's own responsibility).",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps. detail: compact (discovery, cheapest) | standard (default; no inline section text) | full (whole Section inline per hit, largest)."},
			{Name: "document", Use: "Open a document by id or doc_number (document reference), optionally filtered by citation (e.g. 'Section 143'), to read a full provision and page through its chunks. Use this to get complete Section/Subsection text when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. include selects sections (chunks, relations, amendments, timeline, provenance); include=['chunks'] + citation is the cheapest way to read one provision."},
			{Name: "quality_gaps", Use: "Actionable database-quality worklists by category; use before claiming the corpus is validated."},
		},
		EvidenceContract: []string{
			"hits are ranked text evidence; related_hits are adjacent graph context (snippet is a preview — open the document for full text), not rank boosts.",
			"validity and text_provenance fields are database evidence; clients should show uncertainty when they are empty or needs_review is true.",
			"confirmed relations come from promoted structured graph rows; weak evidence is not confirmed legal status.",
			"search always returns hits even when abstain is true — abstain marks a blocking gap, not that the hits are wrong; read gaps[].kind to learn why and judge for yourself.",
			"gap kinds: out_of_domain = query is outside the configured banking/technology scope vocabulary (the hits may still be relevant at the edge of scope); no_evidence = no chunks matched; low_confidence = top score below the configured threshold.",
			"blocking gaps mean the server recommends abstention; warning gaps should be shown to the user/model.",
			"each hit and document carries source + source_url: the official AGC Laws of Malaysia / Bank Negara Malaysia / Securities Commission landing page for the document — a citable page to verify the text. laksa returns content + these links only, never file downloads.",
			"each hit has cite: a ready-to-paste citation (provision + document + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"document returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current.",
			"legal text is returned verbatim in English; laksa never translates.",
			"search detail shapes response size only, never ranking: compact = metadata + snippet + cite + validity badge; standard (default) = adds relations + related_hits, still no inline section text; full = everything including each hit's whole Section. document include selects sections (chunks, relations, amendments, timeline, provenance) — metadata, validity, sources, and gaps always return. Data-quality signals (needs_review, validity.warning) survive every level.",
			"laksa never answers; it returns evidence and the connecting model decides.",
		},
	},
}

// idBrief is the Indonesia (rendang) server contract — Bahasa Indonesia corpus,
// English MCP surface text. Sources: BPK JDIH (peraturan.bpk.go.id: UU, PP,
// POJK, SEOJK) and Bank Indonesia JDIH (jdih.bi.go.id: PBI, PADG). Provision
// vocabulary: Bab/Bagian/Paragraf/Pasal/ayat/huruf.
var idBrief = brief{
	name:  "rendang",
	title: "rendang — Indonesian banking & technology regulation (evidence-only)",
	instructions: `rendang is an evidence-only knowledge base for Indonesian banking & financial-technology regulation. Reach for it whenever a question touches Indonesian banking/finance law — especially digital & technology topics: IT risk management, cybersecurity (keamanan siber), personal-data protection (pelindungan data pribadi), cloud and IT outsourcing (alih daya), electronic transactions and e-signatures (tanda tangan elektronik), digital banking, payment systems (QRIS, sistem pembayaran), and e-KYC. ⚠️ LANGUAGE RULE — NOT OPTIONAL: ALWAYS call search with an INDONESIAN (Bahasa Indonesia) query. The corpus is Indonesian-only; English (or any non-Indonesian) queries return degraded, misleading rankings — you may confidently cite the WRONG regulation. If the user asks in another language, FIRST translate the question to Indonesian, search in Indonesian, then translate the retrieved evidence back for the user yourself. Never pass the user's English text straight into search.

Why you can trust the results: the text is extracted verbatim from Indonesia's OFFICIAL government legal sources — the BPK JDIH (peraturan.bpk.go.id, the national audit body's legal database carrying Laws/UU, Government Regulations/PP, and OJK regulations/POJK/SEOJK) and the Bank Indonesia JDIH (jdih.bi.go.id, Bank Indonesia's legal database carrying PBI and PADG) — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. rendang is evidence-only: it returns exact citations (Pasal/ayat/huruf), validity status, confirmed relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Note the regulator split: OJK (Otoritas Jasa Keuangan) supervises banks and financial institutions; Bank Indonesia owns payment systems — both are in scope.

Flow: call search to get ranked provisions, each with its doc_number, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, and confirmed relations. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook. Token economy: search detail='compact' is the cheap discovery pass; the default detail='standard' never inlines full Pasal text — read one provision with document (citation filter + include=['chunks']), the cheapest call.

When you answer (you, not rendang): cite the exact provision and doc_number (e.g. Pasal 26 ayat (1) UU 27/2022), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and answer in the user's language. Legal text is returned verbatim in Bahasa Indonesia; rendang never translates. If the user communicates in English, translate the retrieved provisions yourself when presenting the answer.

Example questions: "IT risk management requirements for banks in Indonesia", "Apa kewajiban bank jika terjadi insiden siber?", "POJK on digital banking services", "Berapa lama pengendali data pribadi wajib memberitahukan kebocoran data?".`,
	guideDesc:    "Read first. Explains what rendang covers and how to use its evidence tools (search → document) to answer an Indonesian banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or doc_number: full provision text (reassembled Pasal/ayat), validity periods, confirmed relations, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. Pass include to fetch only the sections you need — include=['chunks'] with a citation filter (e.g. 'Pasal 26') is the cheapest way to read one provision. Returns content + source links only — never file downloads.",
	searchDesc: "Search Indonesian banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Pasal/ayat/huruf) with their doc_number, validity status, confirmed relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Indonesian banking/finance law or regulation, especially digital/technology topics: IT risk management, cybersecurity, personal-data protection, cloud & outsourcing, electronic transactions & e-signatures, digital banking, payment systems (QRIS), and e-KYC. ⚠️ ALWAYS query in Bahasa Indonesia — translate first if the user asked in another language. Non-Indonesian queries produce degraded, misleading rankings; never send English text to this tool. " +
		"detail levels: compact (discovery — metadata + snippet + cite, cheapest) | standard (default — adds relations + related_hits; no inline Pasal text) | full (inlines every hit's whole Pasal — largest; prefer the document tool to read one provision).",
	coverageFmt: "\n\nCoverage right now: rendang has extracted and indexed %d official documents (%d provisions) across %d official sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "rendang exposes Indonesian banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, rendang never synthesizes one. Scope: digital/technology regulation (IT risk management, cybersecurity, personal-data protection, cloud & outsourcing, electronic transactions & e-signatures, digital banking, payment systems). The corpus is Indonesian — query in Indonesian (Bahasa Indonesia) for best precision; English queries work via cross-lingual matching but may miss lexical hits. Legal text is returned verbatim in Bahasa Indonesia; rendang never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"MANDATORY: translate any non-Indonesian question to Bahasa Indonesia BEFORE calling search. Indonesian-only corpus — English queries return degraded, misleading rankings. Search in Indonesian, then translate the evidence back for your user.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Token economy: search detail='compact' is the cheap discovery pass (metadata + snippet + cite + validity badge). The default detail='standard' adds relations + related_hits but never inlines full Pasal text. Reserve detail='full' for when you truly want every hit's whole Pasal inline — reading one provision via the document tool is far cheaper.",
			"Call document with a doc_number and a citation (e.g. 'Pasal 26') to read a full provision: search chunks may be split into 'ayat' pieces, and document reassembles the whole Pasal/ayat. Pass include=['chunks'] to skip relations/amendments/timeline when you only need the text — the cheapest call; add 'relations', 'amendments', or 'timeline' as needed.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Answer in the user's language. Legal text is returned verbatim in Bahasa Indonesia; if the user communicates in English, translate the retrieved provisions yourself when presenting the answer.",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps. Query in Indonesian ONLY — translate the user's question first; other languages return degraded, misleading rankings. detail: compact (discovery, cheapest) | standard (default; no inline Pasal text) | full (whole Pasal inline per hit, largest)."},
			{Name: "document", Use: "Open a document by id or doc_number, optionally filtered by citation (e.g. 'Pasal 26'), to read a full provision and page through its chunks. Use this to get complete Pasal/ayat text when search returns fragments. When an amender is itself amended (mengubah/mencabut chains), amendment_chain shows the transitive lineage — follow it to the newest treatment. include selects sections (chunks, relations, amendments, timeline, provenance); include=['chunks'] + citation is the cheapest way to read one provision."},
			{Name: "quality_gaps", Use: "Actionable database-quality worklists by category; use before claiming the corpus is validated."},
		},
		EvidenceContract: []string{
			"hits are ranked text evidence; related_hits are adjacent graph context (snippet is a preview — open the document for full text), not rank boosts.",
			"validity and text_provenance fields are database evidence; clients should show uncertainty when they are empty or needs_review is true.",
			"confirmed relations come from promoted structured graph rows; weak evidence is not confirmed legal status.",
			"search always returns hits even when abstain is true — abstain marks a blocking gap, not that the hits are wrong; read gaps[].kind to learn why and judge for yourself.",
			"gap kinds: out_of_domain = query is outside the configured banking/technology scope vocabulary (the hits may still be relevant at the edge of scope); no_evidence = no chunks matched; low_confidence = top score below the configured threshold.",
			"blocking gaps mean the server recommends abstention; warning gaps should be shown to the user/model.",
			"each hit and document carries source + source_url: the official BPK JDIH / Bank Indonesia JDIH landing page for the document — a citable page to verify the text. rendang returns content + these links only, never file downloads.",
			"each hit has cite: a ready-to-paste citation (provision + document + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"legal text is returned verbatim in Bahasa Indonesia; rendang never translates. If your user communicates in English, translate the retrieved provisions yourself when presenting the answer.",
			"amendment chains: Indonesian regulations are often amended by documents that are themselves later amended or revoked (mengubah/mencabut). amendment_chain (document tool) shows that transitive lineage — depth 1 amends this document, depth 2+ amends a shallower amender; the last node is the newest treatment. On relations, target_amended_by lists documents that further amend that relation's target — a currency warning: never rely on an amender's text without checking whether it was itself amended. The gap kind amendment_chain flags this on the document.",
			"search detail shapes response size only, never ranking: compact = metadata + snippet + cite + validity badge; standard (default) = adds relations + related_hits, still no inline Pasal text; full = everything including each hit's whole Pasal. document include selects sections (chunks, relations, amendments, timeline, provenance) — metadata, validity, sources, and gaps always return. Data-quality signals (needs_review, validity.warning) survive every level.",
			"rendang never answers; it returns evidence and the connecting model decides.",
		},
	},
}

// sgBrief is the Singapore (kaya) server contract — English corpus, English
// MCP surface text. Sources: SSO (sso.agc.gov.sg: consolidated Acts), MAS
// (mas.gov.sg: Notices + Guidelines), PDPC (pdpc.gov.sg: advisory guidelines),
// CSA (csa.gov.sg: CII documents). Provision vocabulary: Part/Division/Section/
// Subsection/Paragraph — the same citation family as MY.
var sgBrief = brief{
	name:  "kaya",
	title: "kaya — Singapore banking & technology regulation (evidence-only)",
	instructions: `kaya is an evidence-only knowledge base for Singapore banking & financial-technology regulation. Reach for it whenever a question touches Singapore banking/finance law — especially digital & technology topics: technology risk management (TRM), cybersecurity, personal-data protection (PDPA), cloud and IT outsourcing, payment services, digital banking, e-KYC, business continuity management (BCM), and technology operations. ⚠️ ALWAYS query in ENGLISH — Singapore's binding legal language and the corpus language. Queries in any other language return degraded rankings: translate to English first, then translate the evidence back for your user.

Why you can trust the results: the text is extracted verbatim from Singapore's OFFICIAL sources — the Singapore Statutes Online (sso.agc.gov.sg: consolidated Acts and subsidiary legislation maintained by the Attorney-General's Chambers), the Monetary Authority of Singapore (mas.gov.sg: Notices, Guidelines, and Circulars), the Personal Data Protection Commission (pdpc.gov.sg: advisory guidelines), and the Cyber Security Agency (csa.gov.sg: Codes of Practice) — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. kaya is evidence-only: it returns exact citations (Part/Section/Subsection/Paragraph), validity status, confirmed relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its source document reference, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, and confirmed relations. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook. Token economy: search detail='compact' is the cheap discovery pass; the default detail='standard' never inlines full section text — read one provision with document (citation filter + include=['chunks']), the cheapest call.

When you answer (you, not kaya): cite the exact provision and document (e.g. section 47 of the Banking Act 1970), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and answer in English — the corpus is English and kaya never translates legal text (translation is the user's own responsibility).

Example questions: "technology risk management requirements for banks in Singapore", "MAS rules on outsourcing for financial institutions", "PDPA obligations for data breach notification", "payment services licensing requirements under PSA 2019", "cybersecurity requirements for critical information infrastructure".`,
	guideDesc:    "Read first. Explains what kaya covers and how to use its evidence tools (search → document) to answer a Singapore banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or doc_number (document reference): full provision text (reassembled Section/Subsection), validity periods, confirmed relations, verbatim incoming amendments, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. Pass include to fetch only the sections you need — include=['chunks'] with a citation filter (e.g. 'Section 47') is the cheapest way to read one provision. Returns content + source links only — never file downloads.",
	searchDesc: "Search Singapore banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Section/Subsection/Paragraph) with their source document, validity status, confirmed relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Singapore banking/finance law or regulation, especially digital/technology topics: technology risk management (TRM), cybersecurity, personal-data protection (PDPA), cloud & outsourcing, payment services, digital banking, e-KYC, BCM, and technology operations. Query in English. " +
		"detail levels: compact (discovery — metadata + snippet + cite, cheapest) | standard (default — adds relations + related_hits; no inline section text) | full (inlines every hit's whole Section — largest; prefer the document tool to read one provision).",
	coverageFmt: "\n\nCoverage right now: kaya has extracted and indexed %d official documents (%d provisions) across %d official sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "kaya exposes Singapore banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, kaya never synthesizes one. Scope: digital/technology regulation (technology risk management, cybersecurity, personal-data protection, cloud & outsourcing, payment services, digital banking, e-KYC, BCM). Query in English; legal text is returned verbatim in English — kaya never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Token economy: search detail='compact' is the cheap discovery pass (metadata + snippet + cite + validity badge). The default detail='standard' adds relations + related_hits but never inlines full section text. Reserve detail='full' for when you truly want every hit's whole Section inline — reading one provision via the document tool is far cheaper.",
			"Call document with a document reference and a citation (e.g. 'Section 47') to read a full provision: search chunks may be split into 'Paragraph' pieces, and document reassembles the whole Section/Subsection. Pass include=['chunks'] to skip relations/amendments/timeline when you only need the text — the cheapest call; add 'relations', 'amendments', or 'timeline' as needed.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Answer in English — the corpus is English; never translate legal text (translation is the user's own responsibility).",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps. detail: compact (discovery, cheapest) | standard (default; no inline section text) | full (whole Section inline per hit, largest)."},
			{Name: "document", Use: "Open a document by id or doc_number (document reference), optionally filtered by citation (e.g. 'Section 47'), to read a full provision and page through its chunks. Use this to get complete Section/Subsection text when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. include selects sections (chunks, relations, amendments, timeline, provenance); include=['chunks'] + citation is the cheapest way to read one provision."},
			{Name: "quality_gaps", Use: "Actionable database-quality worklists by category; use before claiming the corpus is validated."},
		},
		EvidenceContract: []string{
			"hits are ranked text evidence; related_hits are adjacent graph context (snippet is a preview — open the document for full text), not rank boosts.",
			"validity and text_provenance fields are database evidence; clients should show uncertainty when they are empty or needs_review is true.",
			"confirmed relations come from promoted structured graph rows; weak evidence is not confirmed legal status.",
			"search always returns hits even when abstain is true — abstain marks a blocking gap, not that the hits are wrong; read gaps[].kind to learn why and judge for yourself.",
			"gap kinds: out_of_domain = query is outside the configured banking/technology scope vocabulary (the hits may still be relevant at the edge of scope); no_evidence = no chunks matched; low_confidence = top score below the configured threshold.",
			"blocking gaps mean the server recommends abstention; warning gaps should be shown to the user/model.",
			"each hit and document carries source + source_url: the official SSO / MAS / PDPC / CSA page for the document — a citable page to verify the text. kaya returns content + these links only, never file downloads.",
			"each hit has cite: a ready-to-paste citation (provision + document + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"document returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current.",
			"legal text is returned verbatim in English; kaya never translates.",
			"search detail shapes response size only, never ranking: compact = metadata + snippet + cite + validity badge; standard (default) = adds relations + related_hits, still no inline section text; full = everything including each hit's whole Section. document include selects sections (chunks, relations, amendments, timeline, provenance) — metadata, validity, sources, and gaps always return. Data-quality signals (needs_review, validity.warning) survive every level.",
			"kaya never answers; it returns evidence and the connecting model decides.",
		},
	},
}

// thBrief is the Thailand (tomyum) server contract — Thai corpus, English MCP
// surface text. Sources: OCS (Office of the Council of State: consolidated
// Acts), BOT (Bank of Thailand: notifications/circulars), ETDA (Electronic
// Transactions Development Agency: e-transaction regulations), SEC (Securities
// and Exchange Commission: securities/digital-asset regulations). Provision
// vocabulary: หมวด (Chapter) / ส่วนที่ (Part) / มาตรา (Section) for Acts;
// ข้อ (Clause) for BOT notifications.
var thBrief = brief{
	name:  "tomyum",
	title: "tomyum — Thai banking & technology regulation (evidence-only)",
	instructions: `tomyum is an evidence-only knowledge base for Thai banking & financial-technology regulation. Reach for it whenever a question touches Thai banking/finance law — especially digital & technology topics: financial institution supervision (สถาบันการเงิน), payment systems (ระบบการชำระเงิน), personal-data protection (ข้อมูลส่วนบุคคล), cybersecurity (ไซเบอร์), electronic transactions (ธุรกรรมอิเล็กทรอนิกส์), digital banking, IT risk management, and technology operations. ⚠️ LANGUAGE RULE — NOT OPTIONAL: ALWAYS call search with a THAI query. The corpus is Thai-only; English (or any non-Thai) queries return degraded, misleading rankings — the lexical arm cannot match and the dense arm loses precision, so you may confidently cite the WRONG law. If the user asks in another language, FIRST translate the question to Thai, search in Thai, then translate the retrieved evidence back for the user yourself. Never pass the user's English text straight into search.

Why you can trust the results: the text is extracted verbatim from Thailand's OFFICIAL government legal sources — the Office of the Council of State (krisdika.go.th, consolidated Acts/พ.ร.บ.), the Bank of Thailand (bot.or.th, notifications/ประกาศ and circulars/หนังสือเวียน), ETDA (etda.or.th, e-transaction regulations), and the Securities and Exchange Commission (sec.or.th, securities and digital-asset regulations) — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. tomyum is evidence-only: it returns exact citations (มาตรา/วรรค/(๑)(๒)), validity status, confirmed relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its doc_number, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, and confirmed relations. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook. Token economy: search detail='compact' is the cheap discovery pass; the default detail='standard' never inlines full มาตรา text — read one provision with document (citation filter + include=['chunks']), the cheapest call.

When you answer (you, not tomyum): cite the exact provision and doc_number (e.g. มาตรา 26 พ.ร.บ.คุ้มครองข้อมูลส่วนบุคคล พ.ศ. 2562), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and answer in the user's language. Legal text is returned verbatim in Thai; tomyum never translates. If the user communicates in English, translate the retrieved provisions yourself when presenting the answer.

Example questions: "ข้อกำหนดการบริหารความเสี่ยงด้านเทคโนโลยีสารสนเทศสำหรับธนาคาร", "กฎหมายคุ้มครองข้อมูลส่วนบุคคลในภาคการเงิน", "BOT requirements for payment system operators", "digital banking licensing requirements in Thailand".`,
	guideDesc:    "Read first. Explains what tomyum covers and how to use its evidence tools (search → document) to answer a Thai banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or doc_number: full provision text (reassembled มาตรา/วรรค), validity periods, confirmed relations, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. Pass include to fetch only the sections you need — include=['chunks'] with a citation filter (e.g. 'มาตรา 26') is the cheapest way to read one provision. Returns content + source links only — never file downloads.",
	searchDesc: "Search Thai banking & financial-technology regulation and return exact, citable evidence — ranked provisions (มาตรา/วรรค/(๑)(๒)) with their doc_number, validity status, confirmed relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Thai banking/finance law or regulation, especially digital/technology topics: financial institution supervision, payment systems, personal-data protection, cybersecurity, electronic transactions, digital banking, IT risk management, and technology operations. ⚠️ ALWAYS query in Thai — translate first if the user asked in another language. Non-Thai queries produce degraded, misleading rankings; never send English text to this tool. " +
		"detail levels: compact (discovery — metadata + snippet + cite, cheapest) | standard (default — adds relations + related_hits; no inline มาตรา text) | full (inlines every hit's whole มาตรา — largest; prefer the document tool to read one provision).",
	coverageFmt: "\n\nCoverage right now: tomyum has extracted and indexed %d official documents (%d provisions) across %d official sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "tomyum exposes Thai banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, tomyum never synthesizes one. Scope: digital/technology regulation (financial institution supervision, payment systems, personal-data protection, cybersecurity, electronic transactions, digital banking, IT risk management). The corpus is Thai — query in Thai for best precision; English queries work via cross-lingual matching but may miss lexical hits. Legal text is returned verbatim in Thai; tomyum never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"MANDATORY: translate any non-Thai question to Thai BEFORE calling search. Thai-only corpus — English queries return degraded, misleading rankings. Search in Thai, then translate the evidence back for your user.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Token economy: search detail='compact' is the cheap discovery pass (metadata + snippet + cite + validity badge). The default detail='standard' adds relations + related_hits but never inlines full มาตรา text. Reserve detail='full' for when you truly want every hit's whole มาตรา inline — reading one provision via the document tool is far cheaper.",
			"Call document with a doc_number and a citation (e.g. 'มาตรา 26') to read a full provision: search chunks may be split into 'วรรค' pieces, and document reassembles the whole มาตรา/วรรค. Pass include=['chunks'] to skip relations/amendments/timeline when you only need the text — the cheapest call; add 'relations', 'amendments', or 'timeline' as needed.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Answer in the user's language. Legal text is returned verbatim in Thai; if the user communicates in English, translate the retrieved provisions yourself when presenting the answer.",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps. Query in Thai ONLY — translate the user's question first; other languages return degraded, misleading rankings. detail: compact (discovery, cheapest) | standard (default; no inline มาตรา text) | full (whole มาตรา inline per hit, largest)."},
			{Name: "document", Use: "Open a document by id or doc_number, optionally filtered by citation (e.g. 'มาตรา 26'), to read a full provision and page through its chunks. Use this to get complete มาตรา/วรรค text when search returns fragments. include selects sections (chunks, relations, amendments, timeline, provenance); include=['chunks'] + citation is the cheapest way to read one provision."},
			{Name: "quality_gaps", Use: "Actionable database-quality worklists by category; use before claiming the corpus is validated."},
		},
		EvidenceContract: []string{
			"hits are ranked text evidence; related_hits are adjacent graph context (snippet is a preview — open the document for full text), not rank boosts.",
			"validity and text_provenance fields are database evidence; clients should show uncertainty when they are empty or needs_review is true.",
			"confirmed relations come from promoted structured graph rows; weak evidence is not confirmed legal status.",
			"search always returns hits even when abstain is true — abstain marks a blocking gap, not that the hits are wrong; read gaps[].kind to learn why and judge for yourself.",
			"gap kinds: out_of_domain = query is outside the configured banking/technology scope vocabulary (the hits may still be relevant at the edge of scope); no_evidence = no chunks matched; low_confidence = top score below the configured threshold.",
			"blocking gaps mean the server recommends abstention; warning gaps should be shown to the user/model.",
			"each hit and document carries source + source_url: the official OCS / BOT / ETDA / SEC page for the document — a citable page to verify the text. tomyum returns content + these links only, never file downloads.",
			"each hit has cite: a ready-to-paste citation (provision + document + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"legal text is returned verbatim in Thai; tomyum never translates. If your user communicates in English, translate the retrieved provisions yourself when presenting the answer.",
			"search detail shapes response size only, never ranking: compact = metadata + snippet + cite + validity badge; standard (default) = adds relations + related_hits, still no inline มาตรา text; full = everything including each hit's whole มาตรา. document include selects sections (chunks, relations, amendments, timeline, provenance) — metadata, validity, sources, and gaps always return. Data-quality signals (needs_review, validity.warning) survive every level.",
			"tomyum never answers; it returns evidence and the connecting model decides.",
		},
	},
}

// khBrief is the Cambodia (amok) server contract — English corpus, English MCP
// surface text. Sources: NBC (National Bank of Cambodia: banking laws, Prakas,
// IT guidelines), SERC (Securities and Exchange Regulator of Cambodia), CDC
// (Council for the Development of Cambodia: foundational laws), ODC (Open
// Development Cambodia: supplementary legal texts). Provision vocabulary:
// Chapter/Article/Sub-article/Item — the same citation family as MY/SG.
var khBrief = brief{
	name:  "amok",
	title: "amok — Cambodian banking & technology regulation (evidence-only)",
	instructions: `amok is an evidence-only knowledge base for Cambodian banking & financial-technology regulation. Reach for it whenever a question touches Cambodian banking/finance law — especially digital & technology topics: banking and financial institutions, payment systems (Bakong, KHQR), AML/CFT, microfinance, financial leasing, cryptoassets, securities regulation, IT risk management, and technology operations. ⚠️ ALWAYS query in ENGLISH — the corpus language for Cambodia. Queries in Khmer (or any other language) return degraded rankings: translate to English first, then translate the evidence back for your user.

Why you can trust the results: the text is extracted verbatim from Cambodia's OFFICIAL sources — the National Bank of Cambodia (nbc.gov.kh: banking laws, Prakas, circulars, and IT guidelines), the Securities and Exchange Regulator of Cambodia (serc.gov.kh: securities and capital-market regulations), the Council for the Development of Cambodia (cdc.gov.kh: foundational laws), and Open Development Cambodia (opendevelopmentcambodia.net: supplementary legal texts) — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. amok is evidence-only: it returns exact citations (Article/Clause/sub-items), validity status, confirmed relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its source document reference, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, and confirmed relations. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook. Token economy: search detail='compact' is the cheap discovery pass; the default detail='standard' never inlines full article text — read one provision with document (citation filter + include=['chunks']), the cheapest call.

When you answer (you, not amok): cite the exact provision and document (e.g. Article 26 of the Law on Banking and Financial Institutions), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and answer in English — the corpus is English and amok never translates legal text (translation is the user's own responsibility).

Example questions: "licensing requirements for banks in Cambodia", "NBC Prakas on payment system operators", "AML/CFT obligations for financial institutions in Cambodia", "capital adequacy requirements under the Law on Banking and Financial Institutions", "KHQR and Bakong payment system regulations".`,
	guideDesc:    "Read first. Explains what amok covers and how to use its evidence tools (search → document) to answer a Cambodian banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or doc_number (document reference): full provision text (reassembled Article/sub-article), validity periods, confirmed relations, verbatim incoming amendments, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. Pass include to fetch only the sections you need — include=['chunks'] with a citation filter (e.g. 'Article 26') is the cheapest way to read one provision. Returns content + source links only — never file downloads.",
	searchDesc: "Search Cambodian banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Article/Clause/sub-items) with their source document, validity status, confirmed relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Cambodian banking/finance law or regulation, especially digital/technology topics: banking & financial institutions, payment systems (Bakong, KHQR), AML/CFT, microfinance, financial leasing, cryptoassets, securities, IT risk management, and technology operations. Query in English. " +
		"detail levels: compact (discovery — metadata + snippet + cite, cheapest) | standard (default — adds relations + related_hits; no inline article text) | full (inlines every hit's whole Article — largest; prefer the document tool to read one provision).",
	coverageFmt: "\n\nCoverage right now: amok has extracted and indexed %d official documents (%d provisions) across %d official sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "amok exposes Cambodian banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, amok never synthesizes one. Scope: banking & financial institutions, payment systems (Bakong, KHQR), AML/CFT, microfinance, financial leasing, cryptoassets, securities, IT risk management. Query in English; legal text is returned verbatim in English — amok never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Token economy: search detail='compact' is the cheap discovery pass (metadata + snippet + cite + validity badge). The default detail='standard' adds relations + related_hits but never inlines full article text. Reserve detail='full' for when you truly want every hit's whole Article inline — reading one provision via the document tool is far cheaper.",
			"Call document with a document reference and a citation (e.g. 'Article 26') to read a full provision: search chunks may be split into sub-article pieces, and document reassembles the whole Article. Pass include=['chunks'] to skip relations/amendments/timeline when you only need the text — the cheapest call; add 'relations', 'amendments', or 'timeline' as needed.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Answer in English — the corpus is English; never translate legal text (translation is the user's own responsibility).",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps. detail: compact (discovery, cheapest) | standard (default; no inline article text) | full (whole Article inline per hit, largest)."},
			{Name: "document", Use: "Open a document by id or doc_number (document reference), optionally filtered by citation (e.g. 'Article 26'), to read a full provision and page through its chunks. Use this to get complete Article text when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current. include selects sections (chunks, relations, amendments, timeline, provenance); include=['chunks'] + citation is the cheapest way to read one provision."},
			{Name: "quality_gaps", Use: "Actionable database-quality worklists by category; use before claiming the corpus is validated."},
		},
		EvidenceContract: []string{
			"hits are ranked text evidence; related_hits are adjacent graph context (snippet is a preview — open the document for full text), not rank boosts.",
			"validity and text_provenance fields are database evidence; clients should show uncertainty when they are empty or needs_review is true.",
			"confirmed relations come from promoted structured graph rows; weak evidence is not confirmed legal status.",
			"search always returns hits even when abstain is true — abstain marks a blocking gap, not that the hits are wrong; read gaps[].kind to learn why and judge for yourself.",
			"gap kinds: out_of_domain = query is outside the configured banking/technology scope vocabulary (the hits may still be relevant at the edge of scope); no_evidence = no chunks matched; low_confidence = top score below the configured threshold.",
			"blocking gaps mean the server recommends abstention; warning gaps should be shown to the user/model.",
			"each hit and document carries source + source_url: the official NBC / SERC / CDC / ODC page for the document — a citable page to verify the text. amok returns content + these links only, never file downloads.",
			"each hit has cite: a ready-to-paste citation (provision + document + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"document returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current.",
			"legal text is returned verbatim in English; amok never translates.",
			"search detail shapes response size only, never ranking: compact = metadata + snippet + cite + validity badge; standard (default) = adds relations + related_hits, still no inline article text; full = everything including each hit's whole Article. document include selects sections (chunks, relations, amendments, timeline, provenance) — metadata, validity, sources, and gaps always return. Data-quality signals (needs_review, validity.warning) survive every level.",
			"amok never answers; it returns evidence and the connecting model decides.",
		},
	},
}
