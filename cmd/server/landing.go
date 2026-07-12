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

// landingFor selects the page data for a jurisdiction, defaulting to VN (the
// compiled fallback, per the registry convention). Live jurisdictions cross-link
// each other in the "other countries" section.
func landingFor(jurisdiction, version string) landingData {
	var d landingData
	switch strings.ToLower(strings.TrimSpace(jurisdiction)) {
	case "my":
		d = landingMY
		d.Siblings = []landingData{{Name: landingVN.Name, Domain: landingVN.Domain, Country: landingVN.Country, Flag: landingVN.Flag}}
	default:
		d = landingVN
		d.Siblings = []landingData{{Name: landingMY.Name, Domain: landingMY.Domain, Country: landingMY.Country, Flag: landingMY.Flag}}
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
