package http

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
	"danny.vn/banhmi/internal/corpus/source/api"
)

func testPlan(locator string) api.RequestPlan {
	return api.RequestPlan{Country: "vn", SourceID: "test", RequestKey: "request-test", WindowKey: "window-test", Purpose: model.PurposeListing, URL: locator, Method: "GET", PolicyHash: model.Digest(strings.Repeat("a", 64)), BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxItems: 10, MaxPages: 10, MaxInputBytes: 4096, MaxElapsed: time.Second, MaxConcurrent: 1}}
}

func testClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	c, err := New(Config{UserAgent: "capture-test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}, {Name: "other.test", AllowHTTP: true}, {Name: "127.0.0.1", AllowHTTP: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	// This seam exists only in this package's tests. Production still validates DNS.
	c.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return c
}

func TestCaptureReceiptAndNoCredentials(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		count++
		if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.UserAgent() != "capture-test" {
			t.Error("unexpected credentials or user agent")
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Set-Cookie", "secret=value")
		_, _ = io.WriteString(w, "hello")
	}))
	defer s.Close()
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	c := testClient(t, s)
	p := testPlan("http://source.test/file")
	p.Headers = []api.RequestHeader{{Name: "User-Agent", Value: "ignored-planner-hint"}}
	for range 2 {
		var body strings.Builder
		r, err := c.Fetch(context.Background(), p, []Host{{Name: "source.test", AllowHTTP: true}}, nil, &body)
		h := sha256.Sum256([]byte("hello"))
		if err != nil || r.Outcome != Success || r.Status != 200 || r.FinalURL != p.URL || r.ByteLength != 5 || r.SHA256 != hex.EncodeToString(h[:]) || r.MediaType != "text/plain" || r.Finished.Before(r.Started) || body.String() != "hello" {
			t.Fatalf("receipt=%+v err=%v", r, err)
		}
	}
	if count != 2 {
		t.Fatalf("requests=%d", count)
	}
}

func TestRedirectRestrictions(t *testing.T) {
	for _, target := range []string{"http://evil.test/file", "http://127.0.0.1/file", "http://source.test/file"} {
		t.Run(target, func(t *testing.T) {
			s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { stdhttp.Redirect(w, r, target, 302) }))
			defer s.Close()
			c := testClient(t, s)
			p := testPlan("http://source.test/start")
			if target == "http://source.test/file" {
				// Exercise scheme screening directly; TLS downgrade is also checked at each hop.
				if err := checkURL(target, []Host{{Name: "source.test", AllowHTTP: true}}, "https"); err == nil {
					t.Fatal("scheme downgrade admitted")
				}
				return
			}
			r, err := c.Fetch(context.Background(), p, []Host{{Name: "source.test", AllowHTTP: true}, {Name: "127.0.0.1", AllowHTTP: true}}, nil, io.Discard)
			if err == nil || r.Outcome != PolicyRejected {
				t.Fatalf("receipt=%+v error=%v", r, err)
			}
		})
	}
}

func TestDialTimeRejectsPrivateAndRebinding(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1", "::ffff:10.0.0.1", "224.0.0.1", "0.0.0.0", "100.100.100.200", "168.63.129.16", "fd00:ec2::254"} {
		if publicAddress(net.ParseIP(address)) {
			t.Errorf("admitted %s", address)
		}
	}
	c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	dialed := false
	c.connect = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("test forbids connection")
	}
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		calls++
		if calls == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if err := c.resolve(context.Background(), "source.test"); err != nil {
		t.Fatal(err)
	}
	_, err = c.safeDial(context.Background(), "tcp", "source.test:80")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Outcome != PolicyRejected || dialed {
		t.Fatal("dial admitted rebound address")
	}
}

func TestRetryAfterWithoutRetry(t *testing.T) {
	for _, status := range []int{429, 503, 404} {
		count := 0
		s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			count++
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(status)
		}))
		c := testClient(t, s)
		r, err := c.Fetch(context.Background(), testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
		s.Close()
		outcome := Retryable
		if status == 404 {
			outcome = Permanent
		}
		if err == nil || r.Outcome != outcome || r.RetryAfter != "120" || r.Status != status || count != 1 {
			t.Fatalf("receipt=%+v error=%v requests=%d", r, err, count)
		}
	}
}

func TestCaptureBounds(t *testing.T) {
	for _, kind := range []string{"byte", "header", "decompressed", "time"} {
		t.Run(kind, func(t *testing.T) {
			s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				switch kind {
				case "header":
					w.Header().Set("X-Large", strings.Repeat("x", 8192))
				case "time":
					<-r.Context().Done()
					return
				case "decompressed":
					w.Header().Set("Content-Encoding", "gzip")
					gz := gzip.NewWriter(w)
					_, _ = io.WriteString(gz, strings.Repeat("x", 8192))
					_ = gz.Close()
					return
				}
				_, _ = io.WriteString(w, strings.Repeat("x", 8192))
			}))
			defer s.Close()
			c := testClient(t, s)
			p := testPlan("http://source.test/file")
			if kind == "time" {
				p.Budget.MaxElapsed = time.Nanosecond
			}
			r, err := c.Fetch(context.Background(), p, []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
			if err == nil || !r.Truncated || r.Outcome != PolicyRejected {
				t.Fatalf("receipt=%+v error=%v", r, err)
			}
			if kind == "time" && r.Reason != "time_limit" {
				t.Fatalf("time limit misclassified: %+v", r)
			}
		})
	}
}

func TestProductionDialerNeverAllowsLoopback(t *testing.T) {
	c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	dialed := false
	c.connect = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("test forbids connection")
	}
	_, err = c.safeDial(context.Background(), "tcp", "source.test:443")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Outcome != PolicyRejected || dialed {
		t.Fatal("production dialer accepted loopback")
	}
}

func TestEveryRedirectExportIsAuthorized(t *testing.T) {
	count, admissions := 0, 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		count++
		if r.URL.Path == "/start" {
			stdhttp.Redirect(w, r, "http://other.test/final", 302)
			return
		}
		_, _ = io.WriteString(w, "done")
	}))
	defer s.Close()
	c := testClient(t, s)
	r, err := c.Fetch(context.Background(), testPlan("http://source.test/start"), []Host{{Name: "source.test", AllowHTTP: true}, {Name: "other.test", AllowHTTP: true}}, func(context.Context, *stdhttp.Request) error {
		admissions++
		if admissions == 2 {
			return &Failure{Outcome: PolicyRejected, Reason: "residency_rejected"}
		}
		return nil
	}, io.Discard)
	if err == nil || r.Outcome != PolicyRejected || count != 1 || admissions != 2 || len(r.Redirects) != 1 {
		t.Fatalf("receipt=%+v error=%v requests=%d admissions=%d", r, err, count, admissions)
	}
}

func TestAuthHeadersRejectedBeforeNetwork(t *testing.T) {
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Host", "X-Api-Key"} {
		count := 0
		s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { count++ }))
		c := testClient(t, s)
		p := testPlan("http://source.test/file")
		p.Headers = []api.RequestHeader{{Name: name, Value: "forbidden"}}
		r, err := c.Fetch(context.Background(), p, []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
		s.Close()
		if err == nil || r.Outcome != PolicyRejected || count != 0 {
			t.Fatalf("header %s reached network", name)
		}
	}
}

func TestRedirectLimitHasNoInnerRetry(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		count++
		stdhttp.Redirect(w, r, "/again", 302)
	}))
	defer s.Close()
	c := testClient(t, s)
	r, err := c.Fetch(context.Background(), testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
	if err == nil || r.Outcome != PolicyRejected || count != MaxRedirects+1 || len(r.Redirects) != MaxRedirects+1 {
		t.Fatalf("receipt=%+v requests=%d error=%v", r, count, err)
	}
}

func TestTLSRedirectCannotDowngrade(t *testing.T) {
	count := 0
	var host string
	s := httptest.NewTLSServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		count++
		stdhttp.Redirect(w, r, "http://"+host+"/final", 302)
	}))
	defer s.Close()
	host = s.Certificate().DNSNames[0]
	c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: host, AllowHTTP: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	c.roots = x509.NewCertPool()
	c.roots.AddCert(s.Certificate())
	c.dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, s.Listener.Addr().String())
	}
	r, err := c.Fetch(context.Background(), testPlan("https://"+host+"/start"), []Host{{Name: host, AllowHTTP: true}}, nil, io.Discard)
	if err == nil || r.Reason != "scheme_downgrade" || r.Outcome != PolicyRejected || count != 1 {
		t.Fatalf("receipt=%+v requests=%d error=%v", r, count, err)
	}
}

func TestPlanCannotWidenApprovedHosts(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { count++ }))
	defer s.Close()
	c := testClient(t, s)
	r, err := c.Fetch(context.Background(), testPlan("http://evil.test/file"), []Host{{Name: "evil.test", AllowHTTP: true}}, nil, io.Discard)
	if err == nil || r.Outcome != PolicyRejected || count != 0 {
		t.Fatalf("unapproved plan host reached network: %+v %v", r, err)
	}
}

func TestEveryResolvedAddressMustBePublic(t *testing.T) {
	c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test"}}}})
	if err != nil {
		t.Fatal(err)
	}
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("10.0.0.1")}}, nil
	}
	dialed := false
	c.connect = func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("test forbids connection")
	}
	_, err = c.safeDial(context.Background(), "tcp", "source.test:443")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Outcome != PolicyRejected || dialed {
		t.Fatal("mixed DNS results were admitted")
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic storage failure")
}

func TestStorageFailureDoesNotBecomeSourceRetry(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		count++
		_, _ = io.WriteString(w, "body")
	}))
	defer s.Close()
	c := testClient(t, s)
	r, err := c.Fetch(context.Background(), testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, failedWriter{})
	if err == nil || r.Outcome != Infrastructure || r.Reason != "staging_write_failed" || count != 1 {
		t.Fatalf("receipt=%+v error=%v requests=%d", r, err, count)
	}
}

func TestCancelledCaptureDoesNotReachNetwork(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) { count++ }))
	defer s.Close()
	c := testClient(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := c.Fetch(ctx, testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
	if err == nil || r.Outcome != Infrastructure || r.Reason != "cancelled" || count != 0 {
		t.Fatalf("receipt=%+v error=%v requests=%d", r, err, count)
	}
}

// phaseFailureConn waits for a request write before returning scripted bytes
// followed by a synchronous phase error. Close unblocks a pending initial read.
type phaseFailureConn struct {
	net.Conn
	response  *strings.Reader
	fail      func() error
	ready     chan struct{}
	readyOnce sync.Once
	closed    chan struct{}
	closeOnce sync.Once
}

func newPhaseFailureConn(response string, fail func() error) *phaseFailureConn {
	return &phaseFailureConn{
		response: strings.NewReader(response),
		fail:     fail,
		ready:    make(chan struct{}),
		closed:   make(chan struct{}),
	}
}

func (c *phaseFailureConn) Read(p []byte) (int, error) {
	<-c.ready
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	if c.response.Len() > 0 {
		return c.response.Read(p)
	}
	return 0, c.fail()
}

func (c *phaseFailureConn) Write(p []byte) (int, error) {
	select {
	case <-c.closed:
		return 0, net.ErrClosed
	default:
	}
	c.readyOnce.Do(func() { close(c.ready) })
	return len(p), nil
}

func (c *phaseFailureConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	c.readyOnce.Do(func() { close(c.ready) })
	return nil
}

func (*phaseFailureConn) LocalAddr() net.Addr {
	return &net.TCPAddr{}
}

func (*phaseFailureConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{}
}

func (*phaseFailureConn) SetDeadline(time.Time) error {
	return nil
}

func (*phaseFailureConn) SetReadDeadline(time.Time) error {
	return nil
}

func (*phaseFailureConn) SetWriteDeadline(time.Time) error {
	return nil
}

func TestCaptureBodyReadBeforeRequestWrite(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
		if err != nil {
			t.Fatal(err)
		}
		failures := 0
		conn := newPhaseFailureConn("HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n", func() error {
			failures++
			return context.DeadlineExceeded
		})
		c.dial = func(context.Context, string, string) (net.Conn, error) { return conn, nil }
		gotConn := false
		ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
			GotConn: func(httptrace.GotConnInfo) {
				gotConn = true
				// Hold request registration until readLoop has run its initial
				// Peek. The fake must block that read until the request is written.
				synctest.Wait()
			},
		})
		r, err := c.Fetch(ctx, testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
		var failure *Failure
		if !gotConn || failures != 1 || r.Status != 200 || r.WireLength != 39 || !errors.As(err, &failure) || failure.Outcome != PolicyRejected || failure.Reason != "time_limit" || r.Outcome != PolicyRejected || r.Reason != "time_limit" {
			t.Fatalf("receipt=%+v err=%v failures=%d gotConn=%t", r, err, failures, gotConn)
		}
	})
}

func TestCapturePhaseContextClassification(t *testing.T) {
	for _, phase := range []string{"authorize", "dns", "dial", "tls", "headers", "body", "redirect"} {
		for _, cancelled := range []bool{false, true} {
			name := phase + "-deadline"
			if cancelled {
				name = phase + "-cancel"
			}
			t.Run(name, func(t *testing.T) {
				c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				failures := 0
				fail := func() error {
					failures++
					if cancelled {
						cancel()
						// Even a phase-specific error must yield caller cancellation.
						return &Failure{Outcome: Permanent, Reason: "synthetic_phase_failure"}
					}
					// Model a deadline error arriving before ctx.Err becomes set.
					return context.DeadlineExceeded
				}
				c.lookup = func(context.Context, string) ([]net.IPAddr, error) {
					if phase == "dns" {
						return nil, fail()
					}
					return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
				}
				dials := 0
				c.connect = func(context.Context, string, string) (net.Conn, error) {
					dials++
					if phase == "dial" || (phase == "redirect" && dials == 2) {
						return nil, fail()
					}
					response := ""
					connectionFailure := fail
					if phase == "body" {
						response = "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\n"
					}
					if phase == "redirect" {
						response = "HTTP/1.1 302 Found\r\nLocation: /next\r\nContent-Length: 0\r\n\r\n"
						connectionFailure = func() error { return io.EOF }
					}
					return newPhaseFailureConn(response, connectionFailure), nil
				}
				locator := "http://source.test/file"
				if phase == "tls" {
					locator = "https://source.test/file"
				}
				var authorize Authorize
				if phase == "authorize" {
					authorize = func(context.Context, *stdhttp.Request) error {
						return fail()
					}
				}
				r, err := c.Fetch(ctx, testPlan(locator), []Host{{Name: "source.test", AllowHTTP: true}}, authorize, io.Discard)
				want, reason := PolicyRejected, "time_limit"
				if cancelled {
					want, reason = Infrastructure, "cancelled"
				}
				var failure *Failure
				if !errors.As(err, &failure) || failure.Outcome != want || failure.Reason != reason || failure.Key != "request-test" || r.Outcome != want || r.Reason != reason || failures == 0 {
					t.Fatalf("phase=%s receipt=%+v err=%v failures=%d", phase, r, err, failures)
				}
				if phase == "redirect" && (len(r.Redirects) != 1 || dials != 2) {
					t.Fatalf("redirect phase not reached: %+v dials=%d", r, dials)
				}
			})
		}
	}
}

// overdueContext models the interval before a context timer publishes Err.
type overdueContext struct{ context.Context }

func (overdueContext) Deadline() (time.Time, bool) { return time.Unix(1, 0), true }

func TestElapsedDeadlinePrecedesTimerNotification(t *testing.T) {
	ctx := overdueContext{Context: context.Background()}
	for _, phaseError := range []error{
		errors.New("socket timeout"),
		&Failure{Outcome: Retryable, Reason: "connect_failed"},
		&Failure{Outcome: Permanent, Reason: "certificate_format"},
	} {
		var failure *Failure
		if !errors.As(classify(ctx, phaseError), &failure) || failure.Outcome != PolicyRejected || failure.Reason != "time_limit" {
			t.Fatalf("expired deadline misclassified: %+v", failure)
		}
	}
}

func TestCallerCancellationOverridesNestedDeadline(t *testing.T) {
	caller, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(caller, callerContextKey{}, caller)
	nested, stop := context.WithDeadline(ctx, time.Unix(1, 0))
	defer stop()
	cancel()
	var failure *Failure
	if !errors.As(classify(nested, context.DeadlineExceeded), &failure) || failure.Outcome != Infrastructure || failure.Reason != "cancelled" {
		t.Fatalf("caller cancellation lost behind nested deadline: %+v", failure)
	}
}

type cancellingWriter struct {
	cancel context.CancelFunc
}

func (w cancellingWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func TestCallerCancellationOverridesCompleteRead(t *testing.T) {
	c := rawResponseClient(t, "HTTP/1.1 200 OK\r\nContent-Length: 4\r\n\r\ndone")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := c.Fetch(ctx, testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, cancellingWriter{cancel: cancel})
	if err == nil || r.Outcome != Infrastructure || r.Reason != "cancelled" || r.ByteLength != 4 {
		t.Fatalf("completed read hid caller cancellation: %+v err=%v", r, err)
	}
}
