package http

import (
	"bufio"
	"context"
	"io"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/blob"
)

func rawResponseClient(t *testing.T, response string) *Client {
	t.Helper()
	c, err := New(Config{UserAgent: "test", Sources: map[string][]Host{"test": {{Name: "source.test", AllowHTTP: true}}}})
	if err != nil { t.Fatal(err) }
	c.lookup = func(context.Context, string) ([]net.IPAddr, error) { return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil }
	c.connect = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			req, err := stdhttp.ReadRequest(bufio.NewReader(server))
			if err != nil { return }
			_, _ = io.Copy(io.Discard, req.Body)
			_ = req.Body.Close()
			_, _ = io.WriteString(server, response)
		}()
		return client, nil
	}
	return c
}

func TestChunkedWireLimitAndTruncatedStream(t *testing.T) {
	for _, tc := range []struct { name, response, reason string }{
		{"chunk-framing", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"+strings.Repeat("1\r\nx\r\n", 1000)+"0\r\n\r\n", "byte_limit"},
		{"trailers", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-Padding\r\n\r\n1\r\nx\r\n0\r\nX-Padding: "+strings.Repeat("p", 5000)+"\r\n\r\n", "byte_limit"},
		{"truncated", "HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nx", "body_stream_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := rawResponseClient(t, tc.response)
			r, err := c.Fetch(context.Background(), testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
			if err == nil || r.Reason != tc.reason || !r.Truncated { t.Fatalf("receipt=%+v err=%v", r, err) }
			if tc.reason == "byte_limit" && (r.Outcome != PolicyRejected || r.WireLength <= 4096) { t.Fatalf("wire limit not accounted: %+v", r) }
		})
	}
}

func TestChunkedWireIncludesFramingAndTrailers(t *testing.T) {
	response := "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: X-End\r\n\r\n1\r\nx\r\n0\r\nX-End: yes\r\n\r\n"
	c := rawResponseClient(t, response)
	r, err := c.Fetch(context.Background(), testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
	if err != nil || r.ByteLength != 1 || r.WireLength != int64(len(response)) { t.Fatalf("receipt=%+v err=%v", r, err) }
}

func TestObservedRedirectResponseAndProtocolRelative(t *testing.T) {
	count := 0
	s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
		count++
		if r.URL.Path == "/start" { stdhttp.Redirect(w, r, "//source.test/next", 302); return }
		stdhttp.Redirect(w, r, "//evil.test/file", 302)
	}))
	defer s.Close()
	c := testClient(t, s)
	r, err := c.Fetch(context.Background(), testPlan("http://source.test/start"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, io.Discard)
	if err == nil || r.Outcome != PolicyRejected || r.FinalURL != "http://source.test/next" || r.Status != 302 || count != 2 { t.Fatalf("receipt=%+v err=%v count=%d", r, err, count) }
}

func TestPostRedirectReplay(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(stdhttp.StatusText(status), func(t *testing.T) {
			count, admissions := 0, 0
			s := httptest.NewServer(stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
				count++
				content, _ := io.ReadAll(r.Body)
				wantMethod, wantBody := "POST", "synthetic=form"
				if r.URL.Path == "/next" && status != 307 && status != 308 { wantMethod, wantBody = "GET", "" }
				if r.Method != wantMethod || string(content) != wantBody { t.Errorf("method=%s body=%q", r.Method, content) }
				if r.URL.Path == "/start" { stdhttp.Redirect(w, r, "/next", status); return }
				_, _ = io.WriteString(w, "ok")
			}))
			defer s.Close()
			c := testClient(t, s)
			p := testPlan("http://source.test/start"); p.Method, p.Body = "POST", []byte("synthetic=form")
			r, err := c.Fetch(context.Background(), p, []Host{{Name: "source.test", AllowHTTP: true}}, func(context.Context, *stdhttp.Request) error { admissions++; return nil }, io.Discard)
			if err != nil || r.Outcome != Success || count != 2 || admissions != 2 { t.Fatalf("receipt=%+v err=%v count=%d admissions=%d", r, err, count, admissions) }
		})
	}
}

type wireStagingObjects struct {
	blob.Store
	blob.Catalog
	registrations int
	puts int
}

func (s *wireStagingObjects) Register(_ context.Context, m blob.Metadata) (blob.Metadata, error) {
	s.registrations++
	return m, nil
}

func (s *wireStagingObjects) Put(_ context.Context, m blob.Metadata, r io.Reader) (blob.Object, error) {
	s.puts++
	n, err := io.Copy(io.Discard, r)
	return blob.Object{ObjectRef: m.ObjectRef, SHA256: m.SHA256, ByteSize: n}, err
}

func TestWireOverrunAndTruncationNeverPublishBody(t *testing.T) {
	for _, response := range []string{
		"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n"+strings.Repeat("1\r\nx\r\n", 1000)+"0\r\n\r\n",
		"HTTP/1.1 200 OK\r\nContent-Length: 10\r\n\r\nx",
	} {
		c := rawResponseClient(t, response)
		objects := &wireStagingObjects{}
		stager := blob.Stager{Store: objects, Catalog: objects, TempDir: t.TempDir()}
		var receipt Receipt
		_, err := stager.Stage(context.Background(), blob.Metadata{CaptureRef: "capture-wire", CaptureSource: "test", CapturedAt: time.Now(), MediaType: "text/plain"}, 4096, func(w io.Writer) error {
			var err error
			receipt, err = c.Fetch(context.Background(), testPlan("http://source.test/file"), []Host{{Name: "source.test", AllowHTTP: true}}, nil, w)
			return err
		})
		if err == nil || receipt.Outcome == Success || objects.puts != 0 || objects.registrations != 0 { t.Fatalf("incomplete body published: %+v err=%v registrations=%d puts=%d", receipt, err, objects.registrations, objects.puts) }
	}
}
