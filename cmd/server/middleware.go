package main

import (
	"crypto/subtle"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// maxRequestBody caps a single request body. MCP JSON-RPC requests are small; this
// stops memory-exhaustion via oversized bodies.
const maxRequestBody = 1 << 20 // 1 MiB

// secureHosted applies transport defenses. Route handlers enforce website
// sessions or OAuth bearer tokens according to their route class.
func secureHosted(h http.Handler, authority string, log *slog.Logger) (http.Handler, func()) {
	trustProxy := envBool("BANHMI_TRUST_PROXY", false)
	global := newRateLimiter(envFloat("BANHMI_MCP_RATE_RPS", 50), envInt("BANHMI_MCP_RATE_BURST", 100), trustProxy)
	google := newRateLimiter(envFloat("BANHMI_GOOGLE_RATE_RPS", 2), envInt("BANHMI_GOOGLE_RATE_BURST", 10), trustProxy)
	oauth := newRateLimiter(envFloat("BANHMI_OAUTH_RATE_RPS", 10), envInt("BANHMI_OAUTH_RATE_BURST", 20), trustProxy)
	stops := []func(){
		global.startEvictor(10 * time.Minute),
		google.startEvictor(10 * time.Minute),
		oauth.startEvictor(10 * time.Minute),
	}
	stop := func() {
		for _, stopLimiter := range stops {
			stopLimiter()
		}
	}

	h = bodyLimit(h)
	h = protocolRateLimit(h, google, oauth)
	h = canonicalHost(h, authority)
	h = originVerify(h, splitKeys(os.Getenv("BANHMI_ORIGIN_VERIFY_SECRET")), log)
	h = global.middleware(h)
	h = securityHeaders(h)
	return h, stop
}

func canonicalHost(next http.Handler, authority string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && r.Host != authority {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func protocolRateLimit(next http.Handler, google, oauth *rateLimiter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasPrefix(path, "/auth/google/") || path == "/logout":
			google.middleware(next).ServeHTTP(w, r)
		case strings.HasPrefix(path, "/oauth/") || strings.HasPrefix(path, "/.well-known/oauth-"):
			oauth.middleware(next).ServeHTTP(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// originVerify enforces the CloudFront origin secret: when
// BANHMI_ORIGIN_VERIFY_SECRET is set, every request must carry the matching
// X-Origin-Verify header (injected by the CloudFront distribution config), so
// requests that bypass CloudFront and hit the origin EC2 directly are refused.
// Comma-separated secrets allow zero-downtime rotation (accept old + new while
// the distributions update). Empty env → disabled (local dev, Cloud Run — no
// fronting distribution). /healthz bypasses: the ECS health check and direct
// origin smoke tests probe it without CloudFront.
func originVerify(next http.Handler, secrets []string, log *slog.Logger) http.Handler {
	if len(secrets) == 0 {
		return next
	}
	log.Info("origin verification enabled — requests must arrive via CloudFront", "secrets", len(secrets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if !keyAllowed(strings.TrimSpace(r.Header.Get("X-Origin-Verify")), secrets) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
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

// recoverPanic contains a panic to the request that caused it. One process now
// serves every jurisdiction, so an unhandled panic would otherwise take down VN,
// MY and ID together — the isolation the container-per-jurisdiction layout used
// to give for free. net/http already recovers per-connection, but it drops the
// connection silently; this returns a 500 and logs the offender.
func recoverPanic(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic serving request — contained",
					"err", v, "path", r.URL.Path, "host", r.Host,
					"jurisdiction", r.Header.Get(jurisdictionHeader),
					"stack", string(debug.Stack()))
				http.Error(w, "internal error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
