package main

// The landing page is the human/browser face of one MCP deployment: GET / on
// banhmi.danny.vn or laksa.danny.vn. One embedded template renders per
// jurisdiction from landingFor — the same share-the-common / customize-the-copy
// seam as pkg/mcp's brief. SEO comes from the meta/OG/JSON-LD head plus
// /robots.txt and /sitemap.xml; GEO (generative-engine optimization) from
// /llms.txt and the semantic, agent-quotable page body. VN is the compiled
// fallback, matching the registry convention.

import (
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

//go:embed landing.html
var landingHTML string

type landingSource struct {
	Name     string
	Operator string
	URL      string
}

type landingFAQ struct {
	Q string
	A string
}

type landingData struct {
	Code        string // jurisdiction code ("vn")
	Name        string // product name ("banhmi")
	Flag        string // emoji flag
	Emoji       string // product emoji (favicon)
	Country     string // "Vietnam"
	Adjective   string // "Vietnamese"
	Language    string // corpus language, English name
	Domain      string // "banhmi.danny.vn"
	Tagline     string // one-line EN value proposition
	NativeIntro string // one short paragraph in the corpus language (local SEO)
	Description string // meta description (<160 chars)
	Keywords    string
	Citation    string // native citation vocabulary ("Điều/Khoản/Điểm")
	Sources     []landingSource
	Examples    []string // example questions in the corpus language
	FAQ         []landingFAQ
	Siblings    []landingData // other live jurisdictions (Name/Domain/Country/Flag only)
	Version     string
	Generated   string // build/render date for the footer
}

// landingVN is the Vietnam page — the compiled fallback.
var landingVN = landingData{
	Code:      "vn",
	Name:      "banhmi",
	Flag:      "🇻🇳",
	Emoji:     "🥖",
	Country:   "Vietnam",
	Adjective: "Vietnamese",
	Language:  "Vietnamese",
	Domain:    "banhmi.danny.vn",
	Tagline:   "Vietnamese banking & technology law as citable evidence for your AI agent — free remote MCP server, no signup.",
	NativeIntro: "banhmi là máy chủ MCP miễn phí cung cấp pháp luật ngân hàng và công nghệ Việt Nam dưới dạng bằng chứng trích dẫn được: " +
		"văn bản trích nguyên văn từ nguồn chính thức (VBPL, Công Báo, NHNN), kèm số ký hiệu, điều khoản chính xác, " +
		"hiệu lực và liên kết nguồn — để agent AI của bạn tự quyết định câu trả lời.",
	Description: "Free MCP server for Vietnamese banking & fintech regulation. Exact Điều/Khoản citations, validity status and official source links from VBPL, Công Báo and SBV — evidence for your AI agent, no signup.",
	Keywords:    "Vietnam banking law MCP, Vietnamese fintech regulation API, pháp luật ngân hàng Việt Nam, tra cứu văn bản pháp luật AI, Model Context Protocol legal server, VBPL, Thông tư NHNN",
	Citation:    "Điều / Khoản / Điểm",
	Sources: []landingSource{
		{"VBPL", "Ministry of Justice national legal database", "https://vbpl.vn"},
		{"Công Báo", "Official Gazette of the Government", "https://congbao.chinhphu.vn"},
		{"vanban.chinhphu.vn", "Government legal document portal", "https://vanban.chinhphu.vn"},
		{"SBV Hanoi", "State Bank of Vietnam portal", "https://sbv.hanoi.gov.vn"},
	},
	Examples: []string{
		"Ngân hàng phải bảo đảm an toàn hệ thống thông tin như thế nào?",
		"Điều kiện chuyển dữ liệu cá nhân ra nước ngoài là gì?",
		"Thông tư nào quy định về eKYC khi mở tài khoản thanh toán?",
		"Hạn mức giao dịch qua ví điện tử là bao nhiêu?",
	},
	FAQ: []landingFAQ{
		{"Does banhmi answer legal questions?",
			"No. banhmi is evidence-only: it returns verbatim provisions with exact citations, validity status and official source links. Your own AI agent (Claude, ChatGPT, Gemini, Grok …) reads that evidence and decides the answer."},
		{"Where does the legal text come from?",
			"Extracted verbatim from Vietnam's official government sources — VBPL (Ministry of Justice), Công Báo (Official Gazette) and the State Bank of Vietnam — never generated or paraphrased. Every result links its official source page."},
		{"Is it free? Do I need an API key?",
			"Yes, free — and no key or signup. Add the endpoint as a custom connector/MCP server in your agent and start asking."},
		{"What language should I ask in?",
			"You can chat with your agent in any language — but the agent MUST search in Vietnamese. The corpus is Vietnamese-only; English queries return degraded, misleading rankings. A good agent translates your question to Vietnamese, searches, then translates the evidence back."},
	},
	Version: "dev",
}

// landingMY is the Malaysia page.
var landingMY = landingData{
	Code:      "my",
	Name:      "laksa",
	Flag:      "🇲🇾",
	Emoji:     "🍜",
	Country:   "Malaysia",
	Adjective: "Malaysian",
	Language:  "English",
	Domain:    "laksa.danny.vn",
	Tagline:   "Malaysian banking & technology law as citable evidence for your AI agent — free remote MCP server, no signup.",
	NativeIntro: "laksa is a free MCP server exposing Malaysian banking and technology regulation as citable evidence: " +
		"text extracted verbatim from official sources (AGC Laws of Malaysia, Bank Negara Malaysia, Securities Commission), " +
		"with exact section citations, validity status and source links — your AI agent decides the answer.",
	Description: "Free MCP server for Malaysian banking & fintech regulation. Exact section citations, validity status and official source links from AGC, BNM and SC — evidence for your AI agent, no signup.",
	Keywords:    "Malaysia banking law MCP, Malaysian fintech regulation API, RMiT, BNM policy documents, Financial Services Act 2013, Model Context Protocol legal server, e-KYC Malaysia",
	Citation:    "Section / Subsection / Paragraph",
	Sources: []landingSource{
		{"AGC Laws of Malaysia", "Attorney General's Chambers — Federal Acts & P.U. gazette", "https://lom.agc.gov.my"},
		{"Bank Negara Malaysia", "Central bank policy documents & guidelines (RMiT, e-KYC, cloud …)", "https://www.bnm.gov.my"},
		{"Securities Commission Malaysia", "Capital-market technology guidelines", "https://www.sc.com.my"},
	},
	Examples: []string{
		"What are the technology risk management requirements for banks?",
		"What are the e-KYC requirements for customer onboarding?",
		"What are the licensing requirements for a digital bank?",
		"Which BNM policy governs cloud outsourcing for financial institutions?",
	},
	FAQ: []landingFAQ{
		{"Does laksa answer legal questions?",
			"No. laksa is evidence-only: it returns verbatim provisions with exact citations, validity status and official source links. Your own AI agent (Claude, ChatGPT, Gemini, Grok …) reads that evidence and decides the answer."},
		{"Where does the legal text come from?",
			"Extracted verbatim from Malaysia's official sources — the Attorney General's Chambers Laws of Malaysia, Bank Negara Malaysia and the Securities Commission — never generated or paraphrased. Every result links its official source page."},
		{"Is it free? Do I need an API key?",
			"Yes, free — and no key or signup. Add the endpoint as a custom connector/MCP server in your agent and start asking."},
		{"What language should I ask in?",
			"Your agent must search in English — Malaysia's binding legal language and the corpus language. Queries in other languages (including Bahasa Melayu) return degraded rankings; the agent should translate first."},
	},
	Version: "dev",
}

// landingID is the Indonesia page.
var landingID = landingData{
	Code:      "id",
	Name:      "rendang",
	Flag:      "🇮🇩",
	Emoji:     "🍛",
	Country:   "Indonesia",
	Adjective: "Indonesian",
	Language:  "Indonesian",
	Domain:    "rendang.danny.vn",
	Tagline:   "Indonesian banking & technology law as citable evidence for your AI agent — free remote MCP server, no signup.",
	NativeIntro: "rendang adalah server MCP gratis yang menyajikan peraturan perbankan dan teknologi Indonesia sebagai bukti yang dapat dikutip: " +
		"teks diambil kata demi kata dari sumber resmi (JDIH OJK, Bank Indonesia, JDIH BPK), dengan nomor peraturan, pasal dan ayat yang tepat, " +
		"status berlaku, dan tautan sumber — agent AI Anda yang menentukan jawabannya.",
	Description: "Free MCP server for Indonesian banking & fintech regulation. Exact Pasal/ayat citations, validity status and official source links from OJK, BI and BPK — evidence for your AI agent, no signup.",
	Keywords:    "Indonesia banking law MCP, POJK SEOJK API, peraturan OJK, PBI Bank Indonesia, UU PDP, pelindungan data pribadi, Model Context Protocol legal server, regulasi perbankan Indonesia",
	Citation:    "Pasal / ayat / huruf",
	Sources: []landingSource{
		{"JDIH OJK", "Otoritas Jasa Keuangan — POJK & SEOJK (authoritative origin)", "https://jdih.ojk.go.id"},
		{"Bank Indonesia JDIH", "Central bank — PBI & PADG (payment systems, monetary)", "https://jdih.bi.go.id"},
		{"JDIH BPK", "National legal database — UU, PP and cross-ministry regulations", "https://peraturan.bpk.go.id"},
	},
	Examples: []string{
		"Apa kewajiban penyelenggara sistem elektronik menurut peraturan di Indonesia?",
		"Bagaimana bank umum harus mengelola risiko teknologi informasi?",
		"Apa saja hak subjek data pribadi menurut UU Pelindungan Data Pribadi?",
		"Apakah tanda tangan elektronik memiliki kekuatan hukum di Indonesia?",
	},
	FAQ: []landingFAQ{
		{"Does rendang answer legal questions?",
			"No. rendang is evidence-only: it returns verbatim provisions with exact citations, validity status and official source links. Your own AI agent (Claude, ChatGPT, Gemini, Grok …) reads that evidence and decides the answer."},
		{"Where does the legal text come from?",
			"Extracted verbatim from Indonesia's official sources — OJK's JDIH, Bank Indonesia's JDIH and BPK's national legal database — never generated or paraphrased. Every result links its official source page."},
		{"Is it free? Do I need an API key?",
			"Yes, free — and no key or signup. Add the endpoint as a custom connector/MCP server in your agent and start asking."},
		{"What language should I ask in?",
			"You can chat with your agent in any language — but the agent MUST search in Indonesian (Bahasa Indonesia). The corpus is Indonesian-only; English queries return degraded, misleading rankings. A good agent translates your question, searches, then translates the evidence back."},
	},
	Version: "dev",
}

// landingSG is the Singapore page.
var landingSG = landingData{
	Code:      "sg",
	Name:      "kaya",
	Flag:      "🇸🇬",
	Emoji:     "🍞",
	Country:   "Singapore",
	Adjective: "Singapore",
	Language:  "English",
	Domain:    "kaya.danny.vn",
	Tagline:   "Singapore banking & technology law as citable evidence for your AI agent — free remote MCP server, no signup.",
	NativeIntro: "kaya is a free MCP server exposing Singapore banking and technology regulation as citable evidence: " +
		"text extracted verbatim from official sources (SSO, MAS, PDPC, CSA), " +
		"with exact section citations, validity status and source links — your AI agent decides the answer.",
	Description: "Free MCP server for Singapore banking & fintech regulation. Exact section citations, validity status and official source links from SSO, MAS, PDPC and CSA — evidence for your AI agent, no signup.",
	Keywords:    "Singapore banking law MCP, MAS Notice API, FSM-N05 TRM, Payment Services Act, PDPA Singapore, Cybersecurity Act, Model Context Protocol legal server",
	Citation:    "Section / Subsection / Paragraph",
	Sources: []landingSource{
		{"Singapore Statutes Online", "Attorney-General's Chambers — consolidated Acts & subsidiary legislation", "https://sso.agc.gov.sg"},
		{"Monetary Authority of Singapore", "MAS Notices & Guidelines (TRM, cyber hygiene, outsourcing, AML …)", "https://www.mas.gov.sg"},
		{"PDPC Singapore", "Personal Data Protection Commission — advisory guidelines", "https://www.pdpc.gov.sg"},
		{"CSA Singapore", "Cyber Security Agency — CII codes of practice & cybersecurity guidelines", "https://www.csa.gov.sg"},
	},
	Examples: []string{
		"What are the technology risk management requirements for banks in Singapore?",
		"What are the cyber hygiene requirements under MAS Notice FSM-N06?",
		"What are the licensing requirements under the Payment Services Act?",
		"What are a bank's obligations regarding outsourcing under MAS rules?",
	},
	FAQ: []landingFAQ{
		{"Does kaya answer legal questions?",
			"No. kaya is evidence-only: it returns verbatim provisions with exact citations, validity status and official source links. Your own AI agent (Claude, ChatGPT, Gemini, Grok …) reads that evidence and decides the answer."},
		{"Where does the legal text come from?",
			"Extracted verbatim from Singapore's official sources — SSO (consolidated Acts), MAS (Notices & Guidelines), PDPC (advisory guidelines) and CSA (cybersecurity documents) — never generated or paraphrased. Every result links its official source page."},
		{"Is it free? Do I need an API key?",
			"Yes, free — and no key or signup. Add the endpoint as a custom connector/MCP server in your agent and start asking."},
		{"What language should I ask in?",
			"English — Singapore's binding legal language and the corpus language. Your agent should search in English for the best results."},
	},
	Version: "dev",
}

// landingTH is the Thailand page.
var landingTH = landingData{
	Code:      "th",
	Name:      "tomyum",
	Flag:      "🇹🇭",
	Emoji:     "🍲",
	Country:   "Thailand",
	Adjective: "Thai",
	Language:  "Thai",
	Domain:    "tomyum.danny.vn",
	Tagline:   "Thai banking & technology law as citable evidence for your AI agent — free remote MCP server, no signup.",
	NativeIntro: "tomyum เป็นเซิร์ฟเวอร์ MCP ฟรีที่ให้บริการกฎหมายธนาคารและเทคโนโลยีของประเทศไทยเป็นหลักฐานที่อ้างอิงได้: " +
		"ข้อความที่คัดลอกจากแหล่งที่มาทางการ (สำนักงานคณะกรรมการกฤษฎีกา, ธนาคารแห่งประเทศไทย, สพธอ.) " +
		"พร้อมการอ้างอิงมาตรา/ข้อ สถานะการบังคับใช้ และลิงก์แหล่งที่มา — AI agent ของคุณเป็นผู้ตัดสินคำตอบ",
	Description: "Free MCP server for Thai banking & fintech regulation. Exact มาตรา/ข้อ citations, validity status and official source links from OCS, BOT and ETDA — evidence for your AI agent, no signup.",
	Keywords:    "Thailand banking law MCP, ธปท ประกาศ API, พ.ร.บ.สถาบันการเงิน, พ.ร.บ.คุ้มครองข้อมูลส่วนบุคคล, PDPA Thailand, Model Context Protocol legal server",
	Citation:    "มาตรา / วรรค / ข้อ",
	Sources: []landingSource{
		{"สำนักงานคณะกรรมการกฤษฎีกา (OCS)", "Consolidated Thai Acts — Financial Institutions, PDPA, Cybersecurity, Payment Systems, ETA", "https://www.ocs.go.th"},
		{"ธนาคารแห่งประเทศไทย (BOT)", "Central bank notifications & circulars (IT risk, digital fraud, KYC, responsible lending …)", "https://app.bot.or.th/FIPCS"},
		{"สพธอ. (ETDA)", "Electronic Transactions Development Agency — e-transactions, digital ID regulations", "https://www.etda.or.th"},
	},
	Examples: []string{
		"สถาบันการเงินต้องขอใบอนุญาตจาก ธปท. อย่างไร",
		"พ.ร.บ.คุ้มครองข้อมูลส่วนบุคคลกำหนดสิทธิของเจ้าของข้อมูลอะไรบ้าง",
		"ข้อกำหนด IT Risk ของ ธปท. สำหรับสถาบันการเงินมีอะไรบ้าง",
		"ระบบการชำระเงินที่มีความสำคัญต้องปฏิบัติตามหลักเกณฑ์อะไร",
	},
	FAQ: []landingFAQ{
		{"tomyum ตอบคำถามกฎหมายไหม?",
			"ไม่ tomyum เป็นระบบหลักฐานเท่านั้น: ส่งคืนบทบัญญัติต้นฉบับพร้อมการอ้างอิงที่แม่นยำ สถานะการบังคับใช้ และลิงก์แหล่งที่มาทางการ AI agent ของคุณ (Claude, ChatGPT, Gemini, Grok …) อ่านหลักฐานนั้นแล้วตัดสินคำตอบเอง"},
		{"ข้อมูลกฎหมายมาจากไหน?",
			"คัดลอกจากแหล่งที่มาทางการของประเทศไทย — สำนักงานคณะกรรมการกฤษฎีกา (พระราชบัญญัติ) และธนาคารแห่งประเทศไทย (ประกาศ/หนังสือเวียน) — ไม่มีการสร้างหรือถอดความ ทุกผลลัพธ์มีลิงก์ไปยังหน้าแหล่งที่มาทางการ"},
		{"ฟรีไหม? ต้องใช้ API key ไหม?",
			"ใช่ ฟรี — ไม่ต้องลงทะเบียน เพิ่ม endpoint เป็น MCP server ใน agent ของคุณแล้วเริ่มถามได้เลย"},
		{"ควรถามเป็นภาษาอะไร?",
			"Agent ของคุณต้องค้นหาเป็นภาษาไทย — ภาษากฎหมายที่มีผลผูกพันและภาษาของคลังข้อมูล คำค้นภาษาอื่นจะให้ผลลัพธ์ที่ไม่แม่นยำ"},
	},
	Version: "dev",
}

// landingKH is the Cambodia page.
var landingKH = landingData{
	Code:      "kh",
	Name:      "amok",
	Flag:      "🇰🇭",
	Emoji:     "🐟",
	Country:   "Cambodia",
	Adjective: "Cambodian",
	Language:  "English",
	Domain:    "amok.danny.vn",
	Tagline:   "Cambodian banking & technology law as citable evidence for your AI agent — free remote MCP server, no signup.",
	NativeIntro: "amok is a free MCP server exposing Cambodian banking and technology regulation as citable evidence: " +
		"text extracted verbatim from official sources (NBC, SERC, CDC), " +
		"with exact article citations, validity status and source links — your AI agent decides the answer.",
	Description: "Free MCP server for Cambodian banking & fintech regulation. Exact article citations, validity status and official source links from NBC, SERC and CDC — evidence for your AI agent, no signup.",
	Keywords:    "Cambodia banking law MCP, NBC Prakas API, Banking and Financial Institutions, Bakong payment system, Model Context Protocol legal server",
	Citation:    "Article / Clause",
	Sources: []landingSource{
		{"National Bank of Cambodia", "Banking laws, Prakas, circulars, IT risk guidelines", "https://www.nbc.gov.kh"},
		{"SERC", "Securities and Exchange Regulator — securities laws, Prakas, guidelines", "https://www.serc.gov.kh"},
		{"CDC Cambodia", "Council for the Development of Cambodia — foundational law translations", "https://cdc.gov.kh"},
	},
	Examples: []string{
		"What are the licensing requirements for banks in Cambodia?",
		"What are the NBC's technology risk management guidelines?",
		"What AML/CFT reporting obligations apply to financial institutions?",
		"How does the Bakong payment system work under NBC regulations?",
	},
	FAQ: []landingFAQ{
		{"Does amok answer legal questions?",
			"No. amok is evidence-only: it returns verbatim provisions with exact citations, validity status and official source links. Your own AI agent (Claude, ChatGPT, Gemini, Grok …) reads that evidence and decides the answer."},
		{"Where does the legal text come from?",
			"Extracted verbatim from Cambodia's official sources — the National Bank of Cambodia, SERC and CDC — never generated or paraphrased. Every result links its official source page."},
		{"Is it free? Do I need an API key?",
			"Yes, free — and no key or signup. Add the endpoint as a custom connector/MCP server in your agent and start asking."},
		{"What language should I ask in?",
			"English — the corpus contains English translations of Cambodian regulations. Your agent should search in English for the best results."},
	},
	Version: "dev",
}

// landingFor selects the page data for a jurisdiction, defaulting to VN (the
// compiled fallback, per the registry convention). Live jurisdictions cross-link
// each other in the "other countries" section.
func landingFor(jurisdiction, version string) landingData {
	sib := func(d landingData) landingData {
		return landingData{Name: d.Name, Domain: d.Domain, Country: d.Country, Flag: d.Flag}
	}
	var d landingData
	switch strings.ToLower(strings.TrimSpace(jurisdiction)) {
	case "my":
		d = landingMY
		d.Siblings = []landingData{sib(landingVN), sib(landingID), sib(landingSG), sib(landingTH), sib(landingKH)}
	case "id":
		d = landingID
		d.Siblings = []landingData{sib(landingVN), sib(landingMY), sib(landingSG), sib(landingTH), sib(landingKH)}
	case "sg":
		d = landingSG
		d.Siblings = []landingData{sib(landingVN), sib(landingMY), sib(landingID), sib(landingTH), sib(landingKH)}
	case "th":
		d = landingTH
		d.Siblings = []landingData{sib(landingVN), sib(landingMY), sib(landingID), sib(landingSG), sib(landingKH)}
	case "kh":
		d = landingKH
		d.Siblings = []landingData{sib(landingVN), sib(landingMY), sib(landingID), sib(landingSG), sib(landingTH)}
	default:
		d = landingVN
		d.Siblings = []landingData{sib(landingMY), sib(landingID), sib(landingSG), sib(landingTH), sib(landingKH)}
	}
	if version != "" {
		d.Version = version
	}
	d.Generated = time.Now().UTC().Format("2006-01-02")
	return d
}

// mountLanding renders the page once at startup and mounts the static surface:
// GET / (exact), /robots.txt, /llms.txt, /sitemap.xml. Everything is
// pre-rendered — request handling is a byte copy. Cache-Control lets CloudFront
// cache at the edge; a deploy invalidates by bouncing the service + CloudFront
// invalidation if needed.
func mountLanding(mux *http.ServeMux, jurisdiction, version string, log *slog.Logger) error {
	d := landingFor(jurisdiction, version)
	tpl, err := template.New("landing").Parse(landingHTML)
	if err != nil {
		return fmt.Errorf("parse landing template: %w", err)
	}
	var page strings.Builder
	if err := tpl.Execute(&page, d); err != nil {
		return fmt.Errorf("render landing page: %w", err)
	}

	serveStatic := func(body, contentType string, maxAge int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
			_, _ = w.Write([]byte(body))
		}
	}

	mux.HandleFunc("GET /{$}", serveStatic(page.String(), "text/html; charset=utf-8", 300))
	mux.HandleFunc("GET /robots.txt", serveStatic(robotsTxt(d), "text/plain; charset=utf-8", 3600))
	mux.HandleFunc("GET /llms.txt", serveStatic(llmsTxt(d), "text/plain; charset=utf-8", 3600))
	mux.HandleFunc("GET /sitemap.xml", serveStatic(sitemapXML(d), "application/xml; charset=utf-8", 3600))
	log.Info("landing page mounted", "jurisdiction", d.Code, "domain", d.Domain)
	return nil
}

func robotsTxt(d landingData) string {
	return fmt.Sprintf(`User-agent: *
Allow: /

Sitemap: https://%s/sitemap.xml
`, d.Domain)
}

// llmsTxt is the llms.txt (llmstxt.org) GEO surface: a concise, plain-text brief
// an AI crawler or agent can ingest to learn what this endpoint is and how to
// call it — the same facts as the page, without markup.
func llmsTxt(d landingData) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s banking & technology regulation, evidence-only MCP server\n\n", d.Name, d.Adjective)
	fmt.Fprintf(&b, "> %s\n\n", d.Tagline)
	fmt.Fprintf(&b, "%s serves %s banking/financial regulation and cross-cutting technology law (cybersecurity, data protection, cloud, e-transactions, payments, digital banking) as citable database evidence over the Model Context Protocol. It does NOT answer questions: the connecting agent retrieves verbatim provisions with exact %s citations, validity badges, amendment relations, official source links and explicit coverage gaps, and decides the answer itself.\n\n", d.Name, d.Adjective, d.Citation)
	fmt.Fprintf(&b, "## Connect\n\n- MCP endpoint (Streamable HTTP, no auth): https://%s/mcp\n- Works with Claude (custom connector), ChatGPT (developer-mode connector), Gemini CLI (mcpServers httpUrl), Grok, and any MCP client.\n\n", d.Domain)
	b.WriteString("## Tools\n\n- guide: how to use the service (read first)\n- corpus_status: live coverage counts and gaps\n- search: ranked provisions with citations, validity, source links\n- document: full provision text, relations, amendment timeline\n- quality_gaps: exact rows behind known data gaps\n\n")
	fmt.Fprintf(&b, "## Sources (official, verbatim)\n\n")
	for _, s := range d.Sources {
		fmt.Fprintf(&b, "- %s — %s (%s)\n", s.Name, s.Operator, s.URL)
	}
	fmt.Fprintf(&b, "\n## IMPORTANT — query language\n\n- ALWAYS search in %s. The corpus is %s-only: queries in any other language return degraded, misleading rankings. Translate the user's question first, then translate the evidence back yourself.\n\n## Notes\n\n- Legal text is returned verbatim and never translated; repealed or not-yet-effective law is badged, never presented as current.\n- Free, public, rate-limited. No signup.\n", d.Language, d.Language)
	return b.String()
}

func sitemapXML(d landingData) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://%s/</loc><changefreq>weekly</changefreq><priority>1.0</priority></url>
</urlset>
`, d.Domain)
}
