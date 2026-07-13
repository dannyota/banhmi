package fetch

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"

	"google.golang.org/api/idtoken"
)

// ProxyTransport is an http.RoundTripper that forwards requests through a
// Cloud Run fetch-proxy endpoint. The proxy accepts POST /fetch with a JSON
// body {url, method, headers, body} and returns the upstream response verbatim.
// Authentication uses a GCP identity token for the proxy's audience (ADC or
// explicit SA key).
type ProxyTransport struct {
	// ProxyURL is the full proxy endpoint, e.g.
	// "https://ojk-fetch-proxy-....run.app/fetch".
	ProxyURL string

	// SAKey is the GCP service account JSON key. If nil, uses application
	// default credentials (gcloud auth or GOOGLE_APPLICATION_CREDENTIALS).
	SAKey []byte

	mu    sync.Mutex
	inner http.RoundTripper
}

type proxyRequest struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body,omitempty"`
}

func (t *ProxyTransport) transport(req *http.Request) (http.RoundTripper, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inner != nil {
		return t.inner, nil
	}

	var opts []idtoken.ClientOption
	if t.SAKey != nil {
		opts = append(opts, idtoken.WithCredentialsJSON(t.SAKey))
	}
	client, err := idtoken.NewClient(req.Context(), t.ProxyURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("proxy idtoken client: %w", err)
	}
	t.inner = client.Transport
	return t.inner, nil
}

func (t *ProxyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr, err := t.transport(req)
	if err != nil {
		return nil, err
	}

	pr := proxyRequest{
		URL:    req.URL.String(),
		Method: req.Method,
	}
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("proxy read body: %w", err)
		}
		pr.Body = string(b)
	}
	if len(req.Header) > 0 {
		pr.Headers = make(map[string]string, len(req.Header))
		for k := range req.Header {
			pr.Headers[k] = req.Header.Get(k)
		}
	}

	payload, err := json.Marshal(pr)
	if err != nil {
		return nil, fmt.Errorf("proxy marshal: %w", err)
	}

	proxyReq, err := http.NewRequestWithContext(req.Context(), http.MethodPost, t.ProxyURL, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("proxy request: %w", err)
	}
	proxyReq.Header.Set("Content-Type", "application/json")

	return tr.RoundTrip(proxyReq)
}
