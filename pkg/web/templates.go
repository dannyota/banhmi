package web

const pageTemplates = `
{{define "head"}}<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} | banhmi</title>
  <meta name="description" content="Search source-faithful Vietnamese banking and technology-law evidence.">
  <link rel="stylesheet" href="/assets/site.css">
</head>{{end}}

{{define "public-nav"}}<header class="site-header"><a class="wordmark" href="/">banhmi</a><nav aria-label="Primary"><a href="/connect">Connect MCP</a><a class="button button-small" href="/login?next=%2Fapp">Try evidence</a></nav></header>{{end}}

{{define "account-nav"}}<header class="site-header"><a class="wordmark" href="/app">banhmi</a><nav aria-label="Primary"><a href="/app">Evidence</a><a href="/connect">Connect MCP</a><form action="/logout" method="post"><input type="hidden" name="csrf_token" value="{{.CSRFToken}}"><button class="text-button" type="submit">Sign out</button></form></nav></header>{{end}}

{{define "landing"}}{{template "head" .}}<body>
{{template "public-nav" .}}
<main>
  <section class="hero">
    <p class="eyebrow">Vietnam corpus</p>
    <h1>Vietnamese regulatory evidence, with the source still attached.</h1>
    <p class="hero-copy">Search exact legal text with native citations, validity, relations, provenance, and known gaps. You decide what the evidence means.</p>
    <div class="actions"><a class="button" href="/login?next=%2Fapp">Try evidence</a><a class="quiet-link" href="/login?next=%2Fconnect">Connect MCP <span aria-hidden="true">→</span></a></div>
  </section>
  <section class="principles" aria-label="What the service provides">
    <article><span>01</span><h2>Verbatim evidence</h2><p>Read the matched provision in Vietnamese. No generated answer sits between you and the source.</p></article>
    <article><span>02</span><h2>Legal context</h2><p>See validity, amendments, confirmed relations, and warnings beside the text.</p></article>
    <article><span>03</span><h2>Visible limits</h2><p>Coverage gaps and non-binding text stay visible so weak evidence never looks certain.</p></article>
  </section>
</main>
<footer><p>Evidence for research, not legal advice.</p><nav aria-label="Legal"><a href="/privacy">Privacy</a><a href="/terms">Terms</a></nav></footer>
</body></html>{{end}}

{{define "login"}}{{template "head" .}}<body class="account-bg">
{{template "public-nav" .}}
<main class="centered">
  <section class="auth-card">
    <p class="eyebrow">One account</p>
    <h1>Continue to banhmi</h1>
    <p>Use Google to open the evidence workspace or connect an MCP client.</p>
    <a class="button button-wide" href="{{.LoginURL}}">Continue with Google</a>
    <p class="fine-print">By continuing, you agree to the <a href="/terms">Terms</a> and acknowledge the <a href="/privacy">Privacy notice</a>.</p>
  </section>
</main>
</body></html>{{end}}

{{define "app"}}{{template "head" .}}<body>
<meta name="csrf-token" content="{{.CSRFToken}}">
{{template "account-nav" .}}
<main class="workspace">
  <section class="workspace-intro">
    <div><p class="eyebrow">Vietnam evidence</p><h1>Find the provision that matters.</h1></div>
    <div class="account-chip"><span>{{if .DisplayName}}{{.DisplayName}}{{else}}Signed in{{end}}</span>{{if .Email}}<small>{{.Email}}</small>{{end}}</div>
  </section>
  <section class="search-shell" aria-labelledby="search-heading">
    <h2 id="search-heading" class="sr-only">Search evidence</h2>
    <form id="search-form" class="search-form">
      <label for="query">Tìm kiếm văn bản, điều khoản hoặc nghĩa vụ</label>
      <div class="search-row"><textarea id="query" name="query" rows="2" required placeholder="Ví dụ: yêu cầu bảo đảm an toàn hệ thống thông tin"></textarea><button class="button" type="submit">Search evidence</button></div>
    </form>
    <div class="notices"><p><strong>Evidence only.</strong> banhmi retrieves source material and does not generate an answer.</p><p><strong>Vietnamese query.</strong> Search in the corpus language for the strongest ranking.</p></div>
  </section>
  <p id="search-status" class="status" role="status" aria-live="polite"></p>
  <div class="evidence-layout">
    <section id="results" class="results" aria-label="Search results"></section>
    <aside id="document-panel" class="document-panel" aria-label="Document detail" tabindex="-1" hidden></aside>
  </div>
</main>
<script src="/assets/app.js" defer></script>
</body></html>{{end}}

{{define "connect"}}{{template "head" .}}<body>
{{template "account-nav" .}}
<main class="connect-page">
  <section class="connect-copy">
    <p class="eyebrow">Model Context Protocol</p>
    <h1>Bring the evidence into your own model.</h1>
    <p>Configure a supported MCP client with the canonical resource below. The client opens OAuth in your browser, where you continue with the same Google account.</p>
  </section>
  <section class="endpoint-card" aria-labelledby="endpoint-heading">
    <p id="endpoint-heading" class="label">Canonical MCP resource</p>
    <code>{{.MCPURL}}</code>
    <ol><li>Add a remote MCP server in your client.</li><li>Enter the resource URL exactly as shown.</li><li>Complete the browser authorization flow and return to the client.</li></ol>
    <p class="fine-print">Access stays bound to the account and client you approve. You can revoke access from the client.</p>
  </section>
</main>
</body></html>{{end}}
`

const siteCSS = `
:root{--ink:#18231f;--muted:#5e6c66;--paper:#f7f4ec;--card:#fffdf8;--line:#d8ddd7;--green:#164f3b;--lime:#d6e7a6;--warn:#8a4f17;--warn-bg:#fff3df;--shadow:0 18px 60px rgba(26,45,37,.1);font-family:Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--ink);background:var(--paper);font-synthesis:none}
*{box-sizing:border-box}body{margin:0;min-height:100vh;background:radial-gradient(circle at 80% 8%,rgba(214,231,166,.42),transparent 31rem),var(--paper);line-height:1.55}a{color:inherit;text-underline-offset:.2em}.site-header{height:76px;max-width:1180px;margin:auto;padding:0 28px;display:flex;align-items:center;justify-content:space-between}.wordmark{text-decoration:none;font-family:Georgia,serif;font-size:1.55rem;font-weight:700;letter-spacing:-.03em}.site-header nav,.site-header form{display:flex;align-items:center;gap:24px}.site-header nav>a:not(.button),.text-button{font:inherit;font-size:.9rem;text-decoration:none;color:var(--muted)}.text-button{border:0;padding:0;background:none;cursor:pointer}.button{display:inline-flex;align-items:center;justify-content:center;border:0;border-radius:999px;background:var(--green);color:white;text-decoration:none;font:inherit;font-weight:700;padding:14px 22px;cursor:pointer;box-shadow:0 6px 18px rgba(22,79,59,.16)}.button:hover,.button:focus-visible{background:#0e3e2d}.button-small{padding:9px 16px;font-size:.9rem}.button-wide{width:100%;margin-top:18px}.hero{max-width:980px;margin:100px auto 90px;padding:0 28px}.eyebrow,.label{text-transform:uppercase;letter-spacing:.14em;font-size:.72rem;font-weight:800;color:var(--green)}h1,h2,p{margin-top:0}.hero h1,.workspace-intro h1,.connect-copy h1,.auth-card h1{font-family:Georgia,serif;font-weight:500;letter-spacing:-.045em;line-height:1.02}.hero h1{max-width:900px;font-size:clamp(3rem,8vw,6.5rem);margin:16px 0 28px}.hero-copy{max-width:680px;font-size:1.2rem;color:var(--muted)}.actions{display:flex;align-items:center;gap:28px;margin-top:34px}.quiet-link{font-weight:700;text-decoration:none}.principles{max-width:1180px;margin:0 auto 90px;padding:0 28px;display:grid;grid-template-columns:repeat(3,1fr);gap:1px;background:var(--line);border:1px solid var(--line)}.principles article{background:rgba(255,253,248,.85);padding:32px}.principles span{font-family:ui-monospace,monospace;color:var(--green);font-size:.75rem}.principles h2{font-family:Georgia,serif;font-size:1.35rem;margin:42px 0 10px}.principles p,footer{color:var(--muted);font-size:.92rem}footer{max-width:1180px;margin:auto;padding:28px;display:flex;justify-content:space-between;border-top:1px solid var(--line)}footer nav{display:flex;gap:20px}.account-bg{background:linear-gradient(145deg,#edf0df,var(--paper) 55%)}.centered{min-height:calc(100vh - 76px);display:grid;place-items:center;padding:30px}.auth-card{width:min(100%,430px);background:var(--card);padding:46px;border:1px solid var(--line);border-radius:24px;box-shadow:var(--shadow)}.auth-card h1{font-size:2.6rem;margin:12px 0 18px}.auth-card>p{color:var(--muted)}.fine-print{font-size:.8rem;margin:20px 0 0!important}.workspace,.connect-page{max-width:1180px;margin:54px auto 100px;padding:0 28px}.workspace-intro{display:flex;align-items:end;justify-content:space-between;gap:24px}.workspace-intro h1{font-size:clamp(2.5rem,5vw,4.6rem);margin:8px 0 36px}.account-chip{display:flex;flex-direction:column;text-align:right;color:var(--muted);margin-bottom:42px}.account-chip small{font-size:.75rem}.search-shell{background:var(--card);border:1px solid var(--line);border-radius:22px;padding:28px;box-shadow:var(--shadow)}.search-form label{display:block;font-weight:700;margin-bottom:12px}.search-row{display:flex;gap:12px;align-items:stretch}.search-row textarea{flex:1;resize:vertical;min-height:60px;border:1px solid #aab5ae;border-radius:14px;padding:15px 16px;font:inherit;color:var(--ink);background:white}.search-row textarea:focus{outline:3px solid rgba(22,79,59,.2);border-color:var(--green)}.notices{display:flex;gap:28px;margin-top:18px;color:var(--muted);font-size:.82rem}.notices p{margin:0}.status{min-height:26px;margin:22px 4px;color:var(--muted)}.status.error{color:#9d301e}.evidence-layout{display:grid;grid-template-columns:minmax(0,1fr);gap:22px}.evidence-layout.with-document{grid-template-columns:minmax(0,1.05fr) minmax(340px,.95fr)}.results{display:grid;gap:14px}.result-card,.document-panel,.summary-card{background:var(--card);border:1px solid var(--line);border-radius:18px;padding:22px}.evidence-alerts{display:grid;gap:8px;padding:16px 18px;border:1px solid #e8cda5;border-radius:16px;background:var(--warn-bg)}.evidence-alerts p,.result-warnings p{margin:0}.result-warnings{display:grid;gap:6px;margin-top:16px;padding:12px 14px;border-left:3px solid #d79d50;background:#fff9ee}.evidence-warning{color:var(--warn);font-size:.84rem;font-weight:650}.evidence-warning.blocking{font-weight:800}.relation-notice{color:var(--green);font-size:.82rem;font-weight:700}.result-top{display:flex;justify-content:space-between;gap:12px;align-items:start}.result-card h2{font-family:Georgia,serif;font-size:1.35rem;margin:4px 0 2px}.citation{font-size:.84rem;font-weight:800;color:var(--green)}.snippet{white-space:pre-wrap;margin:18px 0;color:#26332e}.badge{display:inline-flex;border-radius:999px;background:#e7eddf;padding:5px 9px;font-size:.72rem;font-weight:800}.badge.warning{background:#f7e4cb;color:var(--warn)}.meta-grid{display:flex;flex-wrap:wrap;gap:8px;color:var(--muted);font-size:.77rem}.card-actions{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-top:20px}.secondary-button{border:1px solid var(--green);border-radius:999px;background:transparent;color:var(--green);padding:9px 14px;font:inherit;font-weight:800;cursor:pointer}.structured{margin-top:15px;border-top:1px solid var(--line);padding-top:12px}.structured summary{cursor:pointer;color:var(--muted);font-size:.8rem}.data-tree{font-size:.78rem;margin-top:10px;overflow-wrap:anywhere}.data-row{display:grid;grid-template-columns:minmax(110px,.35fr) minmax(0,1fr);gap:10px;padding:6px 0;border-bottom:1px solid #eef0ec}.data-key{color:var(--muted);font-family:ui-monospace,monospace}.data-list{display:grid;gap:8px}.data-group{border-left:2px solid var(--line);padding-left:10px}.document-panel{position:sticky;top:18px;max-height:calc(100vh - 36px);overflow:auto;box-shadow:var(--shadow)}.document-head{display:flex;justify-content:space-between;gap:12px}.icon-button{border:0;background:transparent;font-size:1.4rem;cursor:pointer}.empty{padding:38px;text-align:center;color:var(--muted);border:1px dashed #abb6af;border-radius:18px}.connect-page{display:grid;grid-template-columns:1fr 1fr;gap:70px;align-items:center;min-height:65vh}.connect-copy h1{font-size:clamp(3rem,6vw,5.5rem);margin:14px 0 24px}.connect-copy>p:last-child{color:var(--muted);font-size:1.08rem}.endpoint-card{background:var(--card);border:1px solid var(--line);border-radius:24px;padding:36px;box-shadow:var(--shadow)}.endpoint-card code{display:block;padding:16px;border-radius:12px;background:#edf0e9;overflow-wrap:anywhere;color:var(--green);font-size:.9rem}.endpoint-card ol{padding-left:20px;margin:28px 0}.endpoint-card li{padding:6px 0}.sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
@media(max-width:800px){.site-header{padding:0 18px}.site-header nav{gap:14px}.site-header nav>a:not(.button){display:none}.hero{margin:65px auto}.principles{grid-template-columns:1fr}.principles h2{margin-top:18px}.workspace,.connect-page{padding:0 18px;margin-top:32px}.workspace-intro{display:block}.account-chip{display:none}.search-row{display:block}.search-row .button{width:100%;margin-top:10px}.notices{display:block}.notices p+ p{margin-top:8px}.evidence-layout.with-document{grid-template-columns:1fr}.document-panel{position:relative;max-height:none;top:0}.connect-page{grid-template-columns:1fr;gap:28px}.auth-card{padding:32px 24px}footer{margin:0 18px;padding:26px 0}.result-top{display:block}.badge{margin-top:8px}}
@media(prefers-reduced-motion:no-preference){.button,.result-card{transition:transform .18s ease,box-shadow .18s ease}.button:hover{transform:translateY(-1px)}.result-card:hover{box-shadow:0 12px 34px rgba(26,45,37,.07)}}
`

const appJS = `
(() => {
  "use strict";
  const form = document.getElementById("search-form");
  if (!form) return;
  const status = document.getElementById("search-status");
  const results = document.getElementById("results");
  const documentPanel = document.getElementById("document-panel");
  const layout = document.querySelector(".evidence-layout");
  const csrf = document.querySelector('meta[name="csrf-token"]').content;

  function element(tag, className, value) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (value !== undefined && value !== null) node.textContent = String(value);
    return node;
  }

  function clear(node) {
    while (node.firstChild) node.removeChild(node.firstChild);
  }

  function safeLink(value, label) {
    try {
      const parsed = new URL(value);
      if (parsed.protocol === "http:" || parsed.protocol === "https:") {
        const link = element("a", "source-link", label || value);
        link.href = parsed.href;
        link.target = "_blank";
        link.rel = "noopener noreferrer";
        return link;
      }
    } catch (_) {}
    return element("span", "data-value", value);
  }

  function renderValue(value, key) {
    if (value === null || value === undefined) return element("span", "data-value", "Unknown");
    if (Array.isArray(value)) {
      const list = element("div", "data-list");
      if (value.length === 0) list.append(element("span", "data-value", "None"));
      value.forEach((item) => list.append(renderValue(item, key)));
      return list;
    }
    if (typeof value === "object") {
      const group = element("div", "data-group");
      Object.entries(value).forEach(([childKey, childValue]) => {
        const row = element("div", "data-row");
        row.append(element("span", "data-key", childKey.replaceAll("_", " ")));
        row.append(renderValue(childValue, childKey));
        group.append(row);
      });
      return group;
    }
    if (typeof value === "string" && key && key.toLowerCase().includes("url")) return safeLink(value, "Open official source");
    return element("span", "data-value", value);
  }

  function structured(value, label) {
    const details = element("details", "structured");
    details.append(element("summary", "", label));
    const tree = element("div", "data-tree");
    tree.append(renderValue(value));
    details.append(tree);
    return details;
  }

  function setStatus(message, isError) {
    status.textContent = message;
    status.className = isError ? "status error" : "status";
  }

  async function post(path, payload) {
    const response = await fetch(path, {
      method: "POST",
      credentials: "same-origin",
      headers: {"Content-Type": "application/json", "X-CSRF-Token": csrf},
      body: JSON.stringify(payload)
    });
    const data = await response.json().catch(() => ({error: "The response could not be read."}));
    if (!response.ok) throw new Error(data.error || "The request failed.");
    return data;
  }

  function badge(text, warning) {
    return element("span", warning ? "badge warning" : "badge", text);
  }

  function resultCard(hit) {
    const card = element("article", "result-card");
    const top = element("div", "result-top");
    const heading = element("div");
    heading.append(element("p", "citation", hit.cite || [hit.location, hit.doc_number].filter(Boolean).join(" · ")));
    heading.append(element("h2", "", hit.title || hit.doc_number || "Untitled document"));
    top.append(heading);
    const validity = hit.validity || {};
    top.append(badge(validity.warning || validity.status_label || validity.status_class || "Validity unknown", Boolean(validity.warning)));
    card.append(top);
    card.append(element("p", "snippet", hit.snippet || "No snippet returned."));
    const meta = element("div", "meta-grid");
    [hit.doc_number, hit.location, hit.source, hit.text_provenance && hit.text_provenance.quality].filter(Boolean).forEach((item) => meta.append(element("span", "", item)));
    card.append(meta);
    const warnings = element("div", "result-warnings");
    const provenance = hit.text_provenance || {};
    if (provenance.has_nonbinding_text) warnings.append(element("p", "evidence-warning", "Non-binding text is present in this evidence."));
    if (provenance.needs_review) warnings.append(element("p", "evidence-warning", "Text needs review before reliance."));
    const relations = Array.isArray(hit.relations) ? hit.relations : [];
    if (relations.length > 0) warnings.append(element("p", "relation-notice", relations.length === 1 ? "1 confirmed relation. Review its evidence and target." : relations.length + " confirmed relations. Review their evidence and targets."));
    if (warnings.childElementCount > 0) card.append(warnings);
    const actions = element("div", "card-actions");
    if (hit.source_url) actions.append(safeLink(hit.source_url, "Official source ↗"));
    const open = element("button", "secondary-button", "Open document");
    open.type = "button";
    open.addEventListener("click", () => openDocument(hit.document_id, hit.parent_citation || hit.location));
    actions.append(open);
    card.append(actions);
    card.append(structured(hit, "All structured evidence"));
    return card;
  }

  function renderSearch(data) {
    clear(results);
    const evidenceNotices = element("section", "evidence-alerts");
    evidenceNotices.setAttribute("aria-label", "Evidence limitations");
    evidenceNotices.setAttribute("role", "status");
    if (data.abstain) evidenceNotices.append(element("p", "evidence-warning blocking", "Evidence is insufficient for a supported answer."));
    const gaps = Array.isArray(data.gaps) ? data.gaps : [];
    gaps.forEach((gap) => {
      const prefix = gap.blocks_answer ? "Blocking gap: " : "Evidence gap: ";
      evidenceNotices.append(element("p", gap.blocks_answer ? "evidence-warning blocking" : "evidence-warning", prefix + (gap.message || gap.kind || "Unspecified evidence gap")));
    });
    const relatedHits = Array.isArray(data.related_hits) ? data.related_hits : [];
    if (relatedHits.length > 0) evidenceNotices.append(element("p", "relation-notice", relatedHits.length === 1 ? "1 related evidence result is included in the evidence context." : relatedHits.length + " related evidence results are included in the evidence context."));
    if (evidenceNotices.childElementCount > 0) results.append(evidenceNotices);
    const hits = Array.isArray(data.hits) ? data.hits : [];
    if (hits.length === 0) results.append(element("p", "empty", "No matching evidence was returned. Review the gaps and try a more exact Vietnamese query."));
    hits.forEach((hit) => results.append(resultCard(hit)));
    const summary = element("section", "summary-card");
    summary.append(element("h2", "", "Evidence context"));
    summary.append(structured({abstain: data.abstain, gaps, scope: data.scope || {}, related_hits: relatedHits}, "Full gaps, scope, and related evidence"));
    results.append(summary);
  }

  async function openDocument(documentID, citation) {
    setStatus("Loading document…", false);
    try {
      const data = await post("/api/v1/document", {document_id: documentID, citation: citation || ""});
      clear(documentPanel);
      const head = element("div", "document-head");
      head.append(element("h2", "", data.found ? "Document evidence" : "Document not found"));
      const close = element("button", "icon-button", "×");
      close.type = "button";
      close.setAttribute("aria-label", "Close document");
      close.addEventListener("click", () => { documentPanel.hidden = true; layout.classList.remove("with-document"); });
      head.append(close);
      documentPanel.append(head);
      documentPanel.append(renderValue(data));
      documentPanel.hidden = false;
      layout.classList.add("with-document");
      setStatus(data.found ? "Document loaded." : "The corpus returned a not-found evidence payload.", false);
      documentPanel.focus({preventScroll: true});
    } catch (error) {
      setStatus(error.message, true);
    }
  }

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const query = form.elements.query.value.trim();
    if (!query) return;
    setStatus("Searching evidence…", false);
    const submit = form.querySelector('button[type="submit"]');
    submit.disabled = true;
    try {
      const data = await post("/api/v1/search", {query});
      renderSearch(data);
      const count = Array.isArray(data.hits) ? data.hits.length : 0;
      setStatus(count === 1 ? "1 evidence result." : count + " evidence results.", false);
    } catch (error) {
      clear(results);
      setStatus(error.message, true);
    } finally {
      submit.disabled = false;
    }
  });
})();
`
