// Package web serves the public site and the signed-in evidence workspace.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"danny.vn/banhmi/internal/evidence/api"
	"danny.vn/banhmi/internal/evidence/service"
	"danny.vn/banhmi/pkg/identity"
)

const (
	maxRequestBody  = 1 << 20
	evidenceTimeout = 15 * time.Second
	assetCSSPath    = "/assets/site.css"
	assetJSPath     = "/assets/app.js"
)

// Evidence is the shared evidence operation boundary used by the website.
type Evidence interface {
	Search(context.Context, api.SearchInput) (api.SearchOutput, error)
	Document(context.Context, api.DocumentInput) (api.DocumentOutput, error)
}

// Auth is the website session and CSRF boundary.
type Auth interface {
	Authenticate(*http.Request) (identity.Principal, error)
	CSRFToken(*http.Request) string
	ValidCSRF(*http.Request, string) bool
}

// Server serves the public pages and the session-protected evidence website.
type Server struct {
	evidence  Evidence
	auth      Auth
	origin    string
	mcpURL    string
	templates *template.Template
}

type pageData struct {
	Title       string
	LoginURL    string
	CSRFToken   string
	DisplayName string
	Email       string
	MCPURL      string
}

// New validates the local website configuration.
func New(evidence Evidence, auth Auth, origin *url.URL) (*Server, error) {
	if evidence == nil {
		return nil, errors.New("web: evidence is required")
	}
	if auth == nil {
		return nil, errors.New("web: auth is required")
	}
	if origin == nil || origin.Scheme != "https" || origin.Host == "" {
		return nil, errors.New("web: canonical origin must be an HTTPS origin")
	}
	if origin.User != nil || origin.Path != "" || origin.RawPath != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("web: canonical origin must not contain credentials, path, query, or fragment")
	}
	if origin.Hostname() == "" {
		return nil, errors.New("web: canonical origin host is required")
	}

	pages, err := template.New("pages").Parse(pageTemplates)
	if err != nil {
		return nil, fmt.Errorf("web: parse templates: %w", err)
	}
	canonical := origin.Scheme + "://" + origin.Host
	return &Server{
		evidence:  evidence,
		auth:      auth,
		origin:    canonical,
		mcpURL:    canonical + "/mcp",
		templates: pages,
	}, nil
}

// Register adds the website routes to mux. The identity manager separately owns
// the Google callback and logout routes.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/", s.landing)
	mux.HandleFunc("/login", s.login)
	mux.HandleFunc("/app", s.accountPage("/app", "app"))
	mux.HandleFunc("/connect", s.accountPage("/connect", "connect"))
	mux.HandleFunc("/api/v1/search", s.search)
	mux.HandleFunc("/api/v1/document", s.document)
	mux.HandleFunc(assetCSSPath, s.asset("text/css; charset=utf-8", siteCSS))
	mux.HandleFunc(assetJSPath, s.asset("text/javascript; charset=utf-8", appJS))
}

func (s *Server) landing(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.render(w, "landing", pageData{Title: "Vietnamese regulatory evidence"})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	next := allowedNext(r.URL.Query().Get("next"))
	_, authErr := s.auth.Authenticate(r)
	if authErr == nil {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	if !isAuthDenial(authErr) {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	s.render(w, "login", pageData{Title: "Sign in", LoginURL: "/auth/google/start?next=" + url.QueryEscape(next)})
}

func (s *Server) accountPage(path, name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setPrivateHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		principal, err := s.auth.Authenticate(r)
		if err != nil {
			if isAuthDenial(err) {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(path), http.StatusSeeOther)
				return
			}
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		data := pageData{
			Title:       "Evidence workspace",
			CSRFToken:   s.auth.CSRFToken(r),
			DisplayName: principal.DisplayName,
			Email:       principal.Email,
			MCPURL:      s.mcpURL,
		}
		s.render(w, name, data)
	}
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !s.admitAPI(w, r) {
		return
	}
	var in api.SearchInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Query) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), evidenceTimeout)
	defer cancel()
	out, err := s.evidence.Search(ctx, in)
	if err != nil {
		if service.IsInvalidRequest(err) {
			writeAPIError(w, http.StatusBadRequest, "invalid request")
			return
		}
		writeAPIError(w, http.StatusBadGateway, "evidence unavailable")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) document(w http.ResponseWriter, r *http.Request) {
	setPrivateHeaders(w.Header())
	if !s.admitAPI(w, r) {
		return
	}
	var in api.DocumentInput
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.DocumentID == 0 && strings.TrimSpace(in.DocNumber) == "" {
		writeAPIError(w, http.StatusBadRequest, "invalid request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), evidenceTimeout)
	defer cancel()
	out, err := s.evidence.Document(ctx, in)
	if err != nil {
		if service.IsInvalidRequest(err) {
			writeAPIError(w, http.StatusBadRequest, "invalid request")
			return
		}
		writeAPIError(w, http.StatusBadGateway, "evidence unavailable")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) admitAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	if _, err := s.auth.Authenticate(r); err != nil {
		if isAuthDenial(err) {
			writeAPIError(w, http.StatusUnauthorized, "authentication required")
			return false
		}
		writeAPIError(w, http.StatusServiceUnavailable, "service unavailable")
		return false
	}
	if r.Header.Get("Origin") != s.origin {
		writeAPIError(w, http.StatusForbidden, "request denied")
		return false
	}
	if !s.auth.ValidCSRF(r, r.Header.Get("X-CSRF-Token")) {
		writeAPIError(w, http.StatusForbidden, "request denied")
		return false
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	if mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "application/json required")
		return false
	}
	return true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if r.ContentLength > maxRequestBody {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "request too large")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request too large")
			return false
		}
		writeAPIError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "request too large")
			return false
		}
		writeAPIError(w, http.StatusBadRequest, "invalid request")
		return false
	}
	return true
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, name, data); err != nil {
		return
	}
}

func (s *Server) asset(contentType, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		setSecurityHeaders(w.Header())
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = io.WriteString(w, body)
	}
}

func allowedNext(next string) string {
	if next == "/connect" {
		return "/connect"
	}
	return "/app"
}

func isAuthDenial(err error) bool {
	return errors.Is(err, identity.ErrUnauthenticated) || errors.Is(err, identity.ErrBlocked)
}

func setSecurityHeaders(header http.Header) {
	header.Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; connect-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self'; script-src 'self'; style-src 'self'")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Permissions-Policy", "camera=(), geolocation=(), microphone=()")
}

func setPrivateHeaders(header http.Header) {
	setSecurityHeaders(header)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Robots-Tag", "noindex, nofollow")
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
