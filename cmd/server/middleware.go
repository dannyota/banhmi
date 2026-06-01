package main

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// The MCP server is the only public-facing component (the DB is firewalled to it).
// It is PUBLIC by default — any agent may connect, no key required. Abuse is bounded
// by per-IP rate limiting + request body caps here, plus Cloud Run --max-instances
// (cost ceiling), TLS, and optionally Cloud Armor at the edge. API-key auth is
// OPT-IN (set BANHMI_MCP_API_KEY) only if you later want to restrict access.

// maxRequestBody caps a single request body. MCP JSON-RPC requests are small; this
// stops memory-exhaustion via oversized bodies.
const maxRequestBody = 1 << 20 // 1 MiB

// secure wraps h with the public-facing defenses, outermost first: rate limit →
// auth → body cap → handler. /healthz is exempt from auth (Cloud Run probes it)
// but is still rate limited. The returned cleanup stops the limiter's evictor.
func secure(h http.Handler, log *slog.Logger) (http.Handler, func()) {
	keys := splitKeys(os.Getenv("BANHMI_MCP_API_KEY"))
	// Generous per-IP defaults: hosted agents (Claude.ai/ChatGPT/Gemini/Grok) call from
	// SHARED egress IPs, so one bucket may serve many users; an agent Q&A is ~10-15 tool
	// calls. This is a flood backstop, not the primary throttle — Cloud Run --max-instances
	// is the real cost ceiling, and the CPU embedder caps practical throughput per instance.
	// Tune via BANHMI_MCP_RATE_RPS / _BURST from real traffic.
	rl := newRateLimiter(envFloat("BANHMI_MCP_RATE_RPS", 50), envInt("BANHMI_MCP_RATE_BURST", 100), envBool("BANHMI_TRUST_PROXY", false))
	stop := rl.startEvictor(10 * time.Minute)

	h = bodyLimit(h)
	h = apiKeyAuth(h, keys, log)
	h = rl.middleware(h)
	h = securityHeaders(h)
	return h, stop
}

// securityHeaders sets minimal hardening headers on every response (incl. 401/429
// and the cheap 404s scanners get). nosniff is the one that matters for a JSON/SSE
// API; CSP/HSTS are browser-app concerns (HSTS is handled by Cloud Run's TLS edge).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// crossOriginProtected wraps the MCP handler with Go's stdlib cross-origin
// protection — the MCP spec's Origin-validation requirement (CSRF / DNS-rebinding
// defense). It rejects browser requests from untrusted origins (Sec-Fetch-Site:
// cross-site) while letting server-to-server agent calls (no Sec-Fetch-Site/Origin)
// through, so hosted agents are unaffected. Allowlist legit browser origins via
// BANHMI_MCP_ALLOWED_ORIGINS (comma-separated). The SDK's localhost DNS-rebinding
// protection (non-localhost Host on a localhost address → 403) is on by default.
func crossOriginProtected(h http.Handler, log *slog.Logger) http.Handler {
	cop := http.NewCrossOriginProtection()
	for _, o := range splitKeys(os.Getenv("BANHMI_MCP_ALLOWED_ORIGINS")) {
		if err := cop.AddTrustedOrigin(o); err != nil {
			log.Warn("ignoring invalid BANHMI_MCP_ALLOWED_ORIGINS entry", "origin", o, "err", err)
		}
	}
	return cop.Handler(h)
}

func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		next.ServeHTTP(w, r)
	})
}

// apiKeyAuth optionally enforces a static token from BANHMI_MCP_API_KEY
// (comma-separated for rotation / multiple clients), via `Authorization: Bearer
// <key>` or `X-API-Key`. Empty key set → PUBLIC mode (the default): everyone may
// connect, abuse is bounded by rate limiting, not a key. /healthz always bypasses
// auth.
func apiKeyAuth(next http.Handler, keys []string, log *slog.Logger) http.Handler {
	if len(keys) == 0 {
		log.Info("MCP auth disabled — public mode (any agent may connect); abuse bounded by per-IP rate limiting + Cloud Run max-instances. Set BANHMI_MCP_API_KEY to restrict.")
		return next
	}
	log.Info("MCP API-key auth enabled", "keys", len(keys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if !keyAllowed(presentedKey(r), keys) {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func presentedKey(r *http.Request) string {
	if h := r.Header.Get("Authorization"); len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(r.Header.Get("X-API-Key"))
}

// keyAllowed compares in constant time and checks every key (no early exit) so the
// response time does not leak which/whether a key matched.
func keyAllowed(got string, keys []string) bool {
	if got == "" {
		return false
	}
	ok := false
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(got), []byte(k)) == 1 {
			ok = true
		}
	}
	return ok
}

func splitKeys(s string) []string {
	var out []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// rateLimiter is a per-client-IP token-bucket limiter with idle eviction so a
// single source cannot flood the server (and the firewalled DB behind it).
type rateLimiter struct {
	mu         sync.Mutex
	clients    map[string]*clientLimiter
	rps        rate.Limit
	burst      int
	trustProxy bool
}

type clientLimiter struct {
	lim  *rate.Limiter
	seen time.Time
}

func newRateLimiter(rps float64, burst int, trustProxy bool) *rateLimiter {
	return &rateLimiter{clients: make(map[string]*clientLimiter), rps: rate.Limit(rps), burst: burst, trustProxy: trustProxy}
}

func (rl *rateLimiter) limiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	c, ok := rl.clients[ip]
	if !ok {
		c = &clientLimiter{lim: rate.NewLimiter(rl.rps, rl.burst)}
		rl.clients[ip] = c
	}
	c.seen = time.Now()
	return c.lim
}

func (rl *rateLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.limiter(clientIP(r, rl.trustProxy)).Allow() {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// startEvictor drops limiters idle longer than ttl, bounding memory under churn or
// a spray of one-off IPs. Returns a stop func.
func (rl *rateLimiter) startEvictor(ttl time.Duration) func() {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(ttl)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				cutoff := time.Now().Add(-ttl)
				rl.mu.Lock()
				for ip, c := range rl.clients {
					if c.seen.Before(cutoff) {
						delete(rl.clients, ip)
					}
				}
				rl.mu.Unlock()
			}
		}
	}()
	return func() { close(done) }
}

// clientIP returns the rate-limit key for r. X-Forwarded-For is client-controlled,
// so it is honored ONLY behind a known proxy (BANHMI_TRUST_PROXY=true). Cloud Run
// APPENDS the real client IP as the LAST XFF entry — earlier entries are
// attacker-supplied — so we take the last entry, never the first. Without a trusted
// proxy we use RemoteAddr (the real TCP peer), which a client cannot spoof; on
// Cloud Run that collapses to one shared bucket, a fail-safe (over-restrictive),
// not a bypass. (A fronting HTTP load balancer that appends its own IP would need
// the second-to-last entry — revisit if one is added.)
func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
				return ip
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func envBool(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
