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
	default:
		return vnBrief
	}
}

// vnBrief is the Vietnam (banhmi) server contract — the compiled default.
var vnBrief = brief{
	name:  "banhmi",
	title: "banhmi — Vietnamese banking & technology regulation (evidence-only)",
	instructions: `banhmi is an evidence-only knowledge base for Vietnamese banking & financial-technology regulation. Reach for it whenever a question touches Vietnamese banking/finance law — especially digital & technology topics: IT and system safety, cybersecurity and information security, personal-data protection, cloud and IT outsourcing, electronic transactions and e-signatures, digital banking and payment channels, and technology operations. Ask in English or Vietnamese.

Why you can trust the results: the text is extracted verbatim from Vietnam's OFFICIAL government legal sources — VBPL (vbpl.vn, the Ministry of Justice national legal database), Công Báo (congbao.chinhphu.vn, the official government gazette), and the State Bank of Vietnam portal — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. banhmi is evidence-only: it returns exact citations (Điều/Khoản/Điểm), validity status, confirmed amendment/repeal relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its số ký hiệu, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, the verbatim amending clauses, and a chronological timeline. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook.

When you answer (you, not banhmi): cite the exact provision and số ký hiệu, link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and reply in the user's language and its native script — Vietnamese in Latin script, never Han/CJK characters.

Example questions: "IT system safety requirements for banks in Vietnam", "Quy định về bảo vệ dữ liệu cá nhân trong ngân hàng số", "which circular governs electronic KYC (eKYC) for banks".`,
	guideDesc:    "Read first. Explains what banhmi covers and how to use its evidence tools (search → document) to answer a Vietnamese banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or số ký hiệu: full provision text (reassembled Điều/Khoản), validity periods, confirmed relations, verbatim incoming amendments, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. Returns content + source links only — never file downloads.",
	searchDesc: "Search Vietnamese banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Điều/Khoản/Điểm) with their số ký hiệu, validity status, confirmed amendment/repeal relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Vietnamese banking/finance law or regulation, especially digital/technology topics: IT & system safety, cybersecurity & information security, data & personal-data protection, cloud & outsourcing, electronic transactions & e-signatures, digital banking & payment channels, and technology operations. You may query in English or Vietnamese — the index is multilingual.",
	coverageFmt: "\n\nCoverage right now: banhmi has extracted and indexed %d official documents (%d provisions) across %d government sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "banhmi exposes Vietnamese banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, banhmi never synthesizes one. Scope: digital/technology regulation (IT & system safety, cybersecurity, data protection, cloud & outsourcing, e-transactions & e-signatures, digital banking & payment channels). Query in English or Vietnamese (the index is multilingual); legal text is returned verbatim in Vietnamese.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Call document with a số ký hiệu and a citation (e.g. 'Điều 7') to read a full provision: search chunks may be split into 'Đoạn' pieces, and document reassembles the whole Điều/Khoản.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Reply in the user's language and its native script — Vietnamese in Latin script, never Han/CJK characters.",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps."},
			{Name: "document", Use: "Open a document by id or số ký hiệu, optionally filtered by citation (e.g. 'Điều 7'), to read a full provision and page through its chunks. Use this to get complete Điều/Khoản text when search returns fragments. It also returns incoming_amendments: verbatim clauses from documents that amend/replace this one (text + position) — read these to judge which provisions are still current."},
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
			"each hit has cite: a ready-to-paste citation (provision + số ký hiệu + validity + source link). validity.status_label is a plain-English currency badge (In force / Partially in force / Expired-repealed / Not yet effective / Suspended).",
			"MCP returns structured citations and provenance so clients do not need local repo prompts or files.",
			"incoming_amendments (from the document tool) are verbatim clauses of documents that amend/replace this one — banhmi does not decide what they repeal or change; read the text + position and decide.",
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
	instructions: `laksa is an evidence-only knowledge base for Malaysian banking & financial-technology regulation. Reach for it whenever a question touches Malaysian banking/finance law — especially digital & technology topics: technology and IT risk management, cybersecurity and information security, personal-data protection, cloud and IT outsourcing, electronic payments and e-money, digital banking and digital channels, electronic KYC (e-KYC), and technology operations. Ask in English — Malaysia's binding legal language.

Why you can trust the results: the text is extracted verbatim from Malaysia's OFFICIAL sources — the Attorney General's Chambers Laws of Malaysia (lom.agc.gov.my: Federal Acts and the P.U. subsidiary-legislation gazette), Bank Negara Malaysia (bnm.gov.my: policy documents and guidelines), and the Securities Commission Malaysia (sc.com.my) — never generated or paraphrased. Every hit and document includes source_url, the official source page, so you and the user can verify the exact wording against the authoritative origin. laksa is evidence-only: it returns exact citations (Part/Chapter/Section/Subsection/Paragraph), validity status, confirmed relations, provenance, and explicit gaps — it does NOT synthesize an answer and never hides weak data behind confident prose.

Flow: call search to get ranked provisions, each with its source document reference, a plain-English validity badge, the official source link, and a ready-to-paste cite. Call document for a full provision, all official source links, and confirmed relations. Call corpus_status for live coverage, quality_gaps for what is missing, and guide for the full playbook.

When you answer (you, not laksa): cite the exact provision and document (e.g. section 143 of the Financial Services Act 2013), link the source_url so the user can verify, respect validity (never present repealed, superseded, or not-yet-effective text as current law), surface gaps (gaps[], abstain, needs_review) instead of guessing, and answer in English — the corpus is English and laksa never translates legal text (translation is the user's own responsibility).

Example questions: "technology risk management requirements for banks in Malaysia", "BNM rules on cloud outsourcing for financial institutions", "e-KYC requirements for onboarding banking customers".`,
	guideDesc:    "Read first. Explains what laksa covers and how to use its evidence tools (search → document) to answer a Malaysian banking/technology regulation question with exact citations — no local files or extra prompts needed.",
	statusDesc:   "Live corpus coverage: document/chunk/embedding counts, relation coverage, and known data gaps. Call this to gauge how complete the evidence is before relying on it.",
	gapsDesc:     "Exact database rows behind corpus-quality gaps (incomplete fetches, non-binding-only text, unresolved relations, etc.) so an agent can see what is missing. Evidence about completeness, not legal content.",
	documentDesc: "Open one legal document by id or document reference: full provision text (reassembled Section/Subsection), validity periods, confirmed relations, the official source link(s), and data gaps. Use it to read complete provisions when search returns fragments. Returns content + source links only — never file downloads.",
	searchDesc: "Search Malaysian banking & financial-technology regulation and return exact, citable evidence — ranked provisions (Section/Subsection/Paragraph) with their source document, validity status, confirmed relations, the official source link, and explicit gaps. No LLM synthesis: you get the source evidence and decide the answer. " +
		"Use this whenever the question touches Malaysian banking/finance law or regulation, especially digital/technology topics: technology & IT risk management, cybersecurity & information security, data & personal-data protection, cloud & outsourcing, electronic payments & e-money, digital banking & digital channels, e-KYC, and technology operations. Query in English.",
	coverageFmt: "\n\nCoverage right now: laksa has extracted and indexed %d official documents (%d provisions) across %d official sources — call corpus_status for the live, detailed breakdown.",
	guide: guideOutput{
		Purpose: "laksa exposes Malaysian banking & financial-technology regulation as citable database evidence for a user-owned agent/model — you decide the answer, laksa never synthesizes one. Scope: digital/technology regulation (technology & IT risk management, cybersecurity, data protection, cloud & outsourcing, electronic payments & e-money, digital banking, e-KYC). Query in English; legal text is returned verbatim in English — laksa never translates.",
		RecommendedFlow: []string{
			"Call corpus_status first to understand coverage and known gaps.",
			"Call search for a legal question; inspect scope, gaps, hits, relations, and related_hits.",
			"Call document with a document reference and a citation (e.g. 'Section 143') to read a full provision: search chunks may be split into 'Paragraph' pieces, and document reassembles the whole Section/Subsection.",
			"Call quality_gaps for exact database rows behind corpus-quality issues.",
			"Answer only from returned evidence; treat gaps, unresolved targets, and needs_review text as uncertainty.",
			"Answer in English — the corpus is English; never translate legal text (translation is the user's own responsibility).",
		},
		Tools: []guideTool{
			{Name: "corpus_status", Use: "Live corpus counts, embedding coverage, relation coverage, and data gaps."},
			{Name: "search", Use: "The entry point for a legal question: ranked chunks plus confirmed one-hop relations, related-doc previews, scope, and gaps."},
			{Name: "document", Use: "Open a document by id or document reference, optionally filtered by citation (e.g. 'Section 143'), to read a full provision and page through its chunks. Use this to get complete Section/Subsection text when search returns fragments."},
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
			"laksa never answers; it returns evidence and the connecting model decides.",
		},
	},
}
