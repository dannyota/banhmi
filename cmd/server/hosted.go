package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	appserve "danny.vn/banhmi/internal/app/serve"
)

func hostedHandler(hosted *appserve.Hosted, origin *url.URL, log *slog.Logger) (http.Handler, func(), error) {
	if hosted == nil || origin == nil {
		return nil, nil, fmt.Errorf("hosted service and origin are required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	registerHostedRoutes(mux, hosted.Identity, hosted.OAuth, hosted.Web, hosted.Evidence.HTTPHandler(), log)
	if err := mountAuthenticatedPublic(mux, origin, log); err != nil {
		return nil, nil, err
	}
	handler, stop := secureHosted(recoverPanic(mux, log), origin.Host, log)
	return handler, stop, nil
}

type routeRegistrar interface {
	Register(*http.ServeMux)
}

type oauthRouteRegistrar interface {
	routeRegistrar
	Protect(http.Handler) http.Handler
}

func registerHostedRoutes(mux *http.ServeMux, identity routeRegistrar, oauth oauthRouteRegistrar, website routeRegistrar, evidence http.Handler, log *slog.Logger) {
	identity.Register(mux)
	oauth.Register(mux)
	website.Register(mux)
	mux.Handle("/mcp", crossOriginProtected(oauth.Protect(evidence), log))
}

func mountAuthenticatedPublic(mux *http.ServeMux, origin *url.URL, log *slog.Logger) error {
	d := landingFor("vn", version)
	d.Domain = origin.Host
	serveStatic := func(body, contentType string, maxAge int) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAge))
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("GET /robots.txt", serveStatic(robotsTxt(d), "text/plain; charset=utf-8", 3600))
	mux.HandleFunc("GET /llms.txt", serveStatic(llmsTxt(d), "text/plain; charset=utf-8", 3600))
	mux.HandleFunc("GET /sitemap.xml", serveStatic(sitemapXML(d), "application/xml; charset=utf-8", 3600))
	mux.HandleFunc("GET /privacy", serveStatic(privacyHTML(d), "text/html; charset=utf-8", 3600))
	mux.HandleFunc("GET /terms", serveStatic(termsHTML(d), "text/html; charset=utf-8", 3600))
	mux.HandleFunc("GET /support", serveStatic(supportHTML(d), "text/html; charset=utf-8", 3600))
	mux.HandleFunc("GET /.well-known/openai-apps-challenge", challengeHandler(d, log))
	return nil
}
