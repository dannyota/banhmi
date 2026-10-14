package http

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	stdhttp "net/http"
	"strings"
	"sync/atomic"
	"time"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

// MaxRedirects bounds redirect hops in addition to the frozen page allowance.
const MaxRedirects = 3

// Outcome classifies one capture for caller-owned source and infrastructure ledgers.
type Outcome string

const (
	// Success means a complete 2xx response was staged.
	Success Outcome = "success"
	// Retryable means the source failed and the ledger may schedule another attempt.
	Retryable Outcome = "retryable_source_failure"
	// Permanent means the request or source response cannot be retried unchanged.
	Permanent Outcome = "permanent_failure"
	// PolicyRejected means a destination, credential or resource rule rejected work.
	PolicyRejected Outcome = "policy_rejection"
	// Infrastructure means local staging or admission infrastructure failed.
	Infrastructure Outcome = "infrastructure_failure"
)

// Failure carries only a reason and ledger key, never a locator or response body.
type Failure struct {
	Key string
	Outcome Outcome
	Reason string
	// Receipt is local outcome evidence; Error never serializes it.
	Receipt *Receipt
}

// Error formats the safe ledger outcome without transport error text.
func (e *Failure) Error() string { return fmt.Sprintf("capture %s: %s: %s", e.Key, e.Outcome, e.Reason) }

// Hop records one received redirect without storing headers or response content.
type Hop struct {
	From string
	To string
	Status int
}

// Receipt records transport evidence locally; it must not enter workflow payloads.
type Receipt struct {
	RequestKey string
	// AttemptKey is the caller-issued claim key attached by capture composition.
	AttemptKey string
	Status int
	FinalURL string
	Redirects []Hop
	MediaType string
	RetryAfter string
	ByteLength int64
	WireLength int64
	SHA256 string
	Started time.Time
	Finished time.Time
	Outcome Outcome
	Reason string
	Truncated bool
	Intermediates []Receipt
}

// Authorize screens each outbound request immediately before network export,
// including redirects and AIA requests. It never owns host slots or retries.
type Authorize func(context.Context, *stdhttp.Request) error

// Config supplies a fixed user agent and independently approved source and CA
// host sets. Each request intersects source hosts with its frozen plan host set.
type Config struct {
	UserAgent string
	Sources map[string][]Host
	CAHosts []Host
}

// Client captures without parsing bodies, pacing, cookie state or source retries.
// Every Fetch uses a new transport with environment proxies disabled.
type Client struct {
	config Config
	roots *x509.CertPool
	lookup func(context.Context, string) ([]net.IPAddr, error)
	dial func(context.Context, string, string) (net.Conn, error)
	connect func(context.Context, string, string) (net.Conn, error)
	now func() time.Time
}

// New validates configuration without networking and loads system trust roots.
func New(config Config) (*Client, error) {
	if config.UserAgent == "" || len(config.UserAgent) > 512 || strings.ContainsAny(config.UserAgent, "\r\n") || len(config.Sources) == 0 { return nil, fmt.Errorf("invalid capture configuration") }
	copyConfig := Config{UserAgent: config.UserAgent, Sources: make(map[string][]Host), CAHosts: append([]Host(nil), config.CAHosts...)}
	if err := validateHosts(config.CAHosts); err != nil { return nil, err }
	for source, hosts := range config.Sources {
		if source == "" || len(hosts) == 0 { return nil, fmt.Errorf("source hosts are required") }
		if err := validateHosts(hosts); err != nil { return nil, err }
		copyConfig.Sources[source] = append([]Host(nil), hosts...)
	}
	roots, err := x509.SystemCertPool()
	if err != nil { return nil, fmt.Errorf("system trust roots unavailable") }
	c := &Client{config: copyConfig, roots: roots, now: time.Now, lookup: net.DefaultResolver.LookupIPAddr, connect: (&net.Dialer{}).DialContext}
	c.dial = c.safeDial
	return c, nil
}

// Fetch streams one complete response to dst under the immutable plan bounds.
// The caller holds all host claims, including approved redirect and CA hosts.
// It must reserve those hosts before calling; capture never acquires extra slots.
func (c *Client) Fetch(ctx context.Context, plan api.RequestPlan, planHosts []Host, authorize Authorize, dst io.Writer) (Receipt, error) {
	r := Receipt{RequestKey: plan.RequestKey, Started: time.Now().UTC(), FinalURL: plan.URL}
	if model.ValidateKey(plan.RequestKey) != nil {
		r.RequestKey = ""
		return finish(r, &Failure{Outcome: PolicyRejected, Reason: "invalid_request_key"})
	}
	if dst == nil { return finish(r, &Failure{Outcome: Infrastructure, Reason: "missing_staging_sink"}) }
	if err := plan.Validate(); err != nil { return finish(r, &Failure{Outcome: PolicyRejected, Reason: "invalid_plan"}) }
	if err := validateHosts(planHosts); err != nil { return finish(r, &Failure{Outcome: PolicyRejected, Reason: "invalid_plan_hosts"}) }
	hosts := intersection(c.config.Sources[plan.SourceID], planHosts)
	ctx, cancel := context.WithTimeout(ctx, plan.Budget.MaxElapsed)
	defer cancel()
	if err := checkURL(plan.URL, hosts, ""); err != nil { return finish(r, err) }
	headers := stdhttp.Header{}
	for _, header := range plan.Headers {
		name := stdhttp.CanonicalHeaderKey(header.Name)
		switch name {
		case "User-Agent":
			// Frozen planners can retain a legacy user agent hint. The configured
			// capture identity always wins and is the only value exported.
			continue
		case "Accept", "Content-Type", "If-None-Match", "If-Modified-Since":
		case "Referer", "Origin":
			if err := checkURL(header.Value, hosts, ""); err != nil { return finish(r, err) }
		default:
			return finish(r, &Failure{Outcome: PolicyRejected, Reason: "unapproved_header"})
		}
		if strings.ContainsAny(header.Value, "\r\n") { return finish(r, &Failure{Outcome: PolicyRejected, Reason: "invalid_header"}) }
		headers.Set(name, header.Value)
	}
	resp, err := c.request(ctx, plan, hosts, authorize, headers, &r, true, nil)
	if err != nil { return finish(r, classify(ctx, err)) }
	defer func() { _ = resp.Body.Close() }()
	r.Status = resp.StatusCode
	r.FinalURL = resp.Request.URL.String()
	r.MediaType = resp.Header.Get("Content-Type")
	if r.MediaType == "" { r.MediaType = "application/octet-stream" }
	r.RetryAfter = resp.Header.Get("Retry-After")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		outcome := Permanent
		if resp.StatusCode == 429 || resp.StatusCode == 403 || resp.StatusCode == 408 || resp.StatusCode >= 500 { outcome = Retryable }
		return finish(r, &Failure{Outcome: outcome, Reason: "source_status"})
	}
	if resp.ContentLength > plan.Budget.MaxInputBytes { return finish(r, &Failure{Outcome: PolicyRejected, Reason: "byte_limit"}) }
	wire := &boundedReader{reader: resp.Body, remaining: plan.Budget.MaxInputBytes}
	var reader io.Reader = wire
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(wire)
		if err != nil { return finish(r, classify(ctx, err)) }
		defer func() { _ = gz.Close() }()
		reader = gz
	default:
		return finish(r, &Failure{Outcome: PolicyRejected, Reason: "unsupported_content_encoding"})
	}
	body := &boundedReader{reader: reader, remaining: plan.Budget.MaxInputBytes}
	hash := sha256.New()
	writer := &captureWriter{dst: dst, digest: hash}
	r.ByteLength, err = io.Copy(writer, body)
	// WireLength is counted on the connection before HTTP transfer decoding.
	r.SHA256 = hex.EncodeToString(hash.Sum(nil))
	if err != nil {
		if errors.Is(err, blob.ErrByteLimit) { return finish(r, &Failure{Outcome: PolicyRejected, Reason: "byte_limit"}) }
		if ctx.Err() != nil { return finish(r, classify(ctx, err)) }
		if writer.failure != nil { return finish(r, &Failure{Outcome: Infrastructure, Reason: "staging_write_failed"}) }
		return finish(r, &Failure{Outcome: Retryable, Reason: "body_stream_failed"})
	}
	return finish(r, nil)
}

func finish(r Receipt, err error) (Receipt, error) {
	r.Finished = time.Now().UTC()
	r.Outcome = Success
	if err != nil {
		var failure *Failure
		if !errors.As(err, &failure) { failure = &Failure{Outcome: Infrastructure, Reason: "capture_failed"} }
		failure = &Failure{Key: r.RequestKey, Outcome: failure.Outcome, Reason: failure.Reason}
		r.Outcome, r.Reason = failure.Outcome, failure.Reason
		r.Truncated = failure.Reason == "byte_limit" || failure.Reason == "time_limit" || failure.Reason == "header_limit" || failure.Reason == "body_stream_failed"
		err = failure
		copyReceipt := r
		failure.Receipt = &copyReceipt
	}
	return r, err
}

func classify(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) { return &Failure{Outcome: PolicyRejected, Reason: "time_limit"} }
	if ctx.Err() != nil { return &Failure{Outcome: Infrastructure, Reason: "cancelled"} }
	var failure *Failure
	if errors.As(err, &failure) { return failure }
	if errors.Is(err, blob.ErrByteLimit) { return &Failure{Outcome: PolicyRejected, Reason: "byte_limit"} }
	// net/http has no exported sentinel for oversized response headers.
	if strings.Contains(err.Error(), "response headers exceeded") { return &Failure{Outcome: PolicyRejected, Reason: "header_limit"} }
	var certErr *tls.CertificateVerificationError
	if errors.As(err, &certErr) { return &Failure{Outcome: Permanent, Reason: "tls_verification_failed"} }
	return &Failure{Outcome: Retryable, Reason: "transport_failed"}
}

func (c *Client) request(ctx context.Context, plan api.RequestPlan, hosts []Host, authorize Authorize, headers stdhttp.Header, receipt *Receipt, allowAIA bool, visited map[string]bool) (*stdhttp.Response, error) {
	locator, method, body := plan.URL, plan.Method, plan.Body
	headers = headers.Clone()
	headers.Set("User-Agent", c.config.UserAgent)
	headers.Set("Accept-Encoding", "gzip")
	if !allowAIA { headers.Set("Accept-Encoding", "identity") }
	for {
		if visited != nil {
			if visited[locator] { return nil, &Failure{Outcome: Permanent, Reason: "tls_chain_cycle"} }
			visited[locator] = true
		}
		if err := checkURL(locator, hosts, ""); err != nil { return nil, err }
		req, err := stdhttp.NewRequestWithContext(ctx, method, locator, bytes.NewReader(body))
		if err != nil { return nil, &Failure{Outcome: PolicyRejected, Reason: "invalid_request"} }
		req.GetBody = nil
		req.Header = headers.Clone()
		if authorize != nil {
			if err := authorize(ctx, req); err != nil {
				var failure *Failure
				if errors.As(err, &failure) { return nil, failure }
				if ctx.Err() != nil { return nil, classify(ctx, err) }
				return nil, &Failure{Outcome: Infrastructure, Reason: "export_authorization_failed"}
			}
		}
		port := req.URL.Port()
		if port == "" { if req.URL.Scheme == "https" { port = "443" } else { port = "80" } }
		address := net.JoinHostPort(req.URL.Hostname(), port)
		// Complete TLS synchronously. Transport dial goroutines never mutate
		// receipts after cancellation, and each hop has exactly one connection.
		var conn net.Conn
		if req.URL.Scheme == "https" { conn, err = c.dialTLS(ctx, "tcp", address, plan, authorize, receipt, allowAIA) } else { conn, err = c.dial(ctx, "tcp", address) }
		if err != nil { return nil, err }
		wireBase := receipt.WireLength
		wireConn := &responseWireConn{Conn: conn, remaining: max(int64(0), plan.Budget.MaxInputBytes - wireBase)}
		conn = wireConn
		var used atomic.Bool
		dial := func(context.Context, string, string) (net.Conn, error) {
			if used.Swap(true) { return nil, &Failure{Outcome: Infrastructure, Reason: "unexpected_transport_redial"} }
			return conn, nil
		}
		tr := &stdhttp.Transport{Proxy: nil, DialContext: dial, DialTLSContext: dial, DisableCompression: true, DisableKeepAlives: true, MaxResponseHeaderBytes: plan.Budget.MaxInputBytes, ResponseHeaderTimeout: plan.Budget.MaxElapsed}
		resp, err := tr.RoundTrip(req)
		tr.CloseIdleConnections()
		receipt.WireLength = wireBase + wireConn.length.Load()
		if err != nil {
			_ = conn.Close()
			if wireConn.exceeded.Load() { return nil, &Failure{Outcome: PolicyRejected, Reason: "byte_limit"} }
			return nil, err
		}
		resp.Body = &responseWireBody{ReadCloser: resp.Body, wire: wireConn, receipt: receipt, base: wireBase}
		receipt.Status, receipt.FinalURL = resp.StatusCode, req.URL.String()
		if len(resp.Header.Get("Content-Type")) > MaxReceiptFieldBytes || len(resp.Header.Get("Retry-After")) > MaxReceiptFieldBytes {
			_ = resp.Body.Close()
			return nil, &Failure{Outcome: PolicyRejected, Reason: "header_limit"}
		}
		receipt.MediaType, receipt.RetryAfter = resp.Header.Get("Content-Type"), resp.Header.Get("Retry-After")
		status := resp.StatusCode
		location := resp.Header.Get("Location")
		if location == "" || (status != 301 && status != 302 && status != 303 && status != 307 && status != 308) { return resp, nil }
		_ = resp.Body.Close()
		target, err := req.URL.Parse(location)
		if err != nil { return nil, &Failure{Outcome: PolicyRejected, Reason: "invalid_redirect"} }
		if len(target.String()) > MaxURLBytes { return nil, &Failure{Outcome: PolicyRejected, Reason: "url_limit"} }
		receipt.Redirects = append(receipt.Redirects, Hop{From: locator, To: target.String(), Status: status})
		if len(receipt.Redirects) > MaxRedirects || pageCount(*receipt) > plan.Budget.MaxPages { return nil, &Failure{Outcome: PolicyRejected, Reason: "redirect_limit"} }
		if err := checkURL(target.String(), hosts, req.URL.Scheme); err != nil { return nil, err }
		if target.Host != req.URL.Host { headers.Del("Origin"); headers.Del("Referer") }
		if status == 303 || ((status == 301 || status == 302) && method == "POST") { method, body = "GET", nil; headers.Del("Content-Type") }
		locator = target.String()
	}
}

func pageCount(r Receipt) int {
	count := 1 + len(r.Redirects)
	for _, intermediate := range r.Intermediates { count += pageCount(intermediate) }
	return count
}

type boundedReader struct {
	reader io.Reader
	remaining int64
	length int64
	failure error
}

type captureWriter struct {
	dst io.Writer
	digest hash.Hash
	failure error
}

func (w *captureWriter) Write(content []byte) (int, error) {
	n, err := w.dst.Write(content)
	_, _ = w.digest.Write(content[:n])
	if err == nil && n != len(content) { err = io.ErrShortWrite }
	w.failure = err
	return n, err
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if len(p) == 0 { return 0, nil }
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 { r.failure = blob.ErrByteLimit; return 0, r.failure }
		r.failure = err
		return 0, err
	}
	if int64(len(p)) > r.remaining { p = p[:r.remaining] }
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	r.length += int64(n)
	r.failure = err
	return n, err
}

// ExportBytes returns bounded canonical request bytes for in-country residency
// screening. It restores the body without enabling transport retries. URLs and
// headers are part of the screened bytes, including on redirect and AIA hops.
func ExportBytes(req *stdhttp.Request, limit int64) ([]byte, error) {
	if limit <= 0 { return nil, &Failure{Outcome: PolicyRejected, Reason: "export_limit"} }
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(&boundedReader{reader: req.Body, remaining: limit})
		_ = req.Body.Close()
		if err != nil { return nil, &Failure{Outcome: PolicyRejected, Reason: "export_limit"} }
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	copyRequest := req.Clone(req.Context())
	copyRequest.Body = io.NopCloser(bytes.NewReader(body))
	var out bytes.Buffer
	w := &exportWriter{dst: &out, remaining: limit}
	if err := copyRequest.Write(w); err != nil { return nil, &Failure{Outcome: PolicyRejected, Reason: "export_limit"} }
	return out.Bytes(), nil
}

type exportWriter struct { dst io.Writer; remaining int64 }

func (w *exportWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining { return 0, blob.ErrByteLimit }
	n, err := w.dst.Write(p)
	w.remaining -= int64(n)
	return n, err
}

