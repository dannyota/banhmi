package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		remote     string
		xff        string
		trustProxy bool
		want       string
	}{
		{"no trust uses RemoteAddr", "10.1.2.3:5555", "1.2.3.4", false, "10.1.2.3"},
		{"no trust ignores spoofed XFF", "10.1.2.3:5555", "8.8.8.8, evil", false, "10.1.2.3"},
		{"trust uses LAST xff entry (proxy-appended)", "10.0.0.1:80", "evil-spoof, 203.0.113.9", true, "203.0.113.9"},
		{"trust single xff entry", "10.0.0.1:80", "203.0.113.9", true, "203.0.113.9"},
		{"trust but no xff falls back to RemoteAddr", "10.0.0.1:80", "", true, "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/mcp", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := clientIP(r, tt.trustProxy); got != tt.want {
				t.Errorf("clientIP = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKeyAllowed(t *testing.T) {
	keys := []string{"alpha", "beta"}
	cases := map[string]bool{"alpha": true, "beta": true, "gamma": false, "": false, "ALPHA": false}
	for got, want := range cases {
		if keyAllowed(got, keys) != want {
			t.Errorf("keyAllowed(%q) = %v, want %v", got, !want, want)
		}
	}
	if keyAllowed("alpha", nil) {
		t.Error("keyAllowed with no configured keys should be false")
	}
}

func TestPresentedKey(t *testing.T) {
	tests := []struct {
		name, auth, apiKey, want string
	}{
		{"bearer", "Bearer secret123", "", "secret123"},
		{"bearer case-insensitive scheme", "bearer secret123", "", "secret123"},
		{"x-api-key", "", "secret123", "secret123"},
		{"authorization wins over x-api-key", "Bearer fromauth", "fromheader", "fromauth"},
		{"none", "", "", ""},
		{"non-bearer authorization ignored", "Basic abc", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/mcp", nil)
			if tt.auth != "" {
				r.Header.Set("Authorization", tt.auth)
			}
			if tt.apiKey != "" {
				r.Header.Set("X-API-Key", tt.apiKey)
			}
			if got := presentedKey(r); got != tt.want {
				t.Errorf("presentedKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitKeys(t *testing.T) {
	got := splitKeys("  a , b ,, c  ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("splitKeys len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("splitKeys[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if len(splitKeys("")) != 0 {
		t.Error("splitKeys(\"\") should be empty")
	}
}

// TestAuthMiddleware verifies /healthz bypasses auth, missing/wrong keys are
// rejected, and a valid key passes — when a key is configured.
func TestAuthMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := apiKeyAuth(next, []string{"secret"}, testLogger())

	check := func(path, auth string, want int) {
		r := httptest.NewRequest("POST", path, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("%s auth=%q: got %d, want %d", path, auth, w.Code, want)
		}
	}
	check("/healthz", "", http.StatusOK)       // exempt
	check("/mcp", "", http.StatusUnauthorized) // no key
	check("/mcp", "Bearer nope", http.StatusUnauthorized)
	check("/mcp", "Bearer secret", http.StatusOK) // valid

	// No keys configured → public (passes through).
	hp := apiKeyAuth(next, nil, testLogger())
	r := httptest.NewRequest("POST", "/mcp", nil)
	w := httptest.NewRecorder()
	hp.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("public mode: got %d, want 200", w.Code)
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(1, 3, false) // 1 rps, burst 3, RemoteAddr keying
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := rl.middleware(next)
	ok, limited := 0, 0
	for i := 0; i < 8; i++ {
		r := httptest.NewRequest("GET", "/healthz", nil)
		r.RemoteAddr = "10.0.0.5:1234"
		// spoofed XFF must NOT create new buckets (trustProxy=false)
		r.Header.Set("X-Forwarded-For", "9.9.9."+string(rune('0'+i)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		switch w.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok != 3 || limited != 5 {
		t.Errorf("burst=3: got ok=%d limited=%d, want ok=3 limited=5 (spoofed XFF must not bypass)", ok, limited)
	}
}
