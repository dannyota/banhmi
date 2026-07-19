package vbpl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"
)

// dateLayout is vbpl's issueDate format (no zone); parseDate consumes it.
const dateLayout = "2006-01-02T15:04:05"

// Agency sets the test Source is built with: SBV (sweep) and the cross-cutting
// central issuers (keyword search).
var (
	sbvIDs    = []string{"62", "908"}
	nonSbvIDs = []string{"55", "1", "3", "2"}
)

// fakeCorpus is a newest-first vbpl doc/all corpus for the test server. Doc at
// rank 0 is the newest; each later rank is issued one day earlier, so issueDate
// is strictly descending and a watermark can land between any two. The server
// serves it for any request (the real server's keyword/agency filtering isn't
// modeled — these tests pin the request banhmi sends and the paging/parsing).
type fakeCorpus struct {
	total int
	base  time.Time // issueDate of rank 0 (newest)
}

// issued returns the issueDate string for a given rank (0 = newest).
func (c fakeCorpus) issued(rank int) string {
	return c.base.AddDate(0, 0, -rank).Format(dateLayout)
}

// page returns the doc/all items for a 1-based page of the given size, newest-first.
func (c fakeCorpus) page(pageNumber, pageSize int) []docItem {
	start := (pageNumber - 1) * pageSize
	if pageSize <= 0 || start < 0 || start >= c.total {
		return nil
	}
	end := min(start+pageSize, c.total)
	items := make([]docItem, 0, end-start)
	for rank := start; rank < end; rank++ {
		items = append(items, docItem{
			ID:         fmt.Sprintf("%d", 100000+rank),
			DocNum:     fmt.Sprintf("%d/2025/TT-NHNN", rank),
			Title:      fmt.Sprintf("doc rank %d", rank),
			DocAbs:     fmt.Sprintf("body text for rank %d", rank),
			IssueDate:  c.issued(rank),
			EffFrom:    c.issued(rank + 1),
			EffStatus:  codeName{Code: "CHL", Name: "Còn hiệu lực"},
			DocType:    codeName{Code: "TT", Name: "Thông tư"},
			AgencyName: "Ngân hàng Nhà nước",
		})
	}
	return items
}

// captured records the doc/all requests the server received, for assertions.
type captured struct {
	mu   sync.Mutex
	reqs []docAllRequest
}

func (c *captured) add(r docAllRequest) {
	c.mu.Lock()
	c.reqs = append(c.reqs, r)
	c.mu.Unlock()
}

func (c *captured) all() []docAllRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]docAllRequest(nil), c.reqs...)
}

// newCorpusServer serves POST doc/all over the fake corpus, captures each request,
// and pins the invariants both discovery modes must satisfy (newest-first sort,
// ungrouped, title-scoped, agency-scoped).
func newCorpusServer(t *testing.T, c fakeCorpus, cap *captured) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/qtdc/public/doc/all", func(w http.ResponseWriter, r *http.Request) {
		var req docAllRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request body", http.StatusBadRequest)
			return
		}
		cap.add(req)
		if req.SortBy != "issueDate" || req.SortDirection != "desc" {
			http.Error(w, "expected newest-first sort", http.StatusBadRequest)
			return
		}
		if req.GroupVbpl || req.OptionDoc != "title" {
			http.Error(w, "expected groupVbpl=false and optionDoc=title", http.StatusBadRequest)
			return
		}
		if len(req.AgencyIds) == 0 {
			http.Error(w, "discovery must be agency-scoped", http.StatusBadRequest)
			return
		}
		resp := docAllResponse{Success: true}
		resp.Data.Total = c.total
		for _, it := range c.page(req.PageNumber, req.PageSize) {
			b, err := json.Marshal(it)
			if err != nil {
				t.Errorf("marshal item: %v", err)
				return
			}
			resp.Data.Items = append(resp.Data.Items, b)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("encode response: %v", err)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// rewriteTransport sends every request to the test server, preserving the
// request's path and body. Source.New already takes an *http.Client, so pointing
// that client's Transport at httptest exercises the real Discover request and
// postJSON retry/limiter path with no production URL change.
type rewriteTransport struct {
	target *url.URL // test server base (scheme+host)
	base   http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	req.Host = rt.target.Host
	return rt.base.RoundTrip(req)
}

func testSource(t *testing.T, srv *httptest.Server) *Source {
	t.Helper()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client := &http.Client{Transport: rewriteTransport{target: target, base: http.DefaultTransport}}
	return New(client, nil, sbvIDs, nonSbvIDs, nil)
}

// TestDiscover_SbvSweep proves the empty-keyword mode is a keyword-less sweep over
// the SBV agency ids that pages the whole corpus newest-first, carrying docAbs into
// DiscoveredDoc.Abstract for the downstream scope filter.
func TestDiscover_SbvSweep(t *testing.T) {
	corpus := fakeCorpus{total: 2044, base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var cap captured
	srv := newCorpusServer(t, corpus, &cap)
	s := testSource(t, srv)

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover SBV sweep: %v", err)
	}

	reqs := cap.all()
	wantPages := (corpus.total + sweepPageSize - 1) / sweepPageSize
	if len(reqs) != wantPages {
		t.Fatalf("SBV sweep made %d requests, want %d (one per page to data.total)", len(reqs), wantPages)
	}
	for i, r := range reqs {
		if r.Keyword != "" {
			t.Fatalf("req %d: sweep sent keyword %q, want empty", i, r.Keyword)
		}
		if !reflect.DeepEqual(r.AgencyIds, sbvIDs) {
			t.Fatalf("req %d: sweep agencyIds = %v, want %v", i, r.AgencyIds, sbvIDs)
		}
	}
	if len(docs) != corpus.total {
		t.Fatalf("SBV sweep returned %d docs, want all %d", len(docs), corpus.total)
	}
	for i, d := range docs {
		if d.SourceID != SourceID {
			t.Fatalf("doc %d: SourceID = %q, want %q", i, d.SourceID, SourceID)
		}
		wantNum := fmt.Sprintf("%d/2025/TT-NHNN", i)
		if d.Number != wantNum {
			t.Fatalf("doc %d: Number = %q, want %q (order/completeness broken)", i, d.Number, wantNum)
		}
		wantAbs := fmt.Sprintf("body text for rank %d", i)
		if d.Abstract != wantAbs {
			t.Fatalf("doc %d: Abstract = %q, want %q (docAbs not carried)", i, d.Abstract, wantAbs)
		}
		wantEff := parseDate(corpus.issued(i + 1))
		if !d.EffectiveAt.Equal(wantEff) {
			t.Fatalf("doc %d: EffectiveAt = %s, want %s (effFrom not carried)", i, d.EffectiveAt, wantEff)
		}
	}
	if !docs[0].IssuedAt.After(docs[len(docs)-1].IssuedAt) {
		t.Errorf("expected newest-first: docs[0]=%s not after docs[last]=%s", docs[0].IssuedAt, docs[len(docs)-1].IssuedAt)
	}
}

// TestDiscover_WatermarkFilters proves an incremental sweep keeps only documents
// strictly newer than the watermark and stops paging early.
func TestDiscover_WatermarkFilters(t *testing.T) {
	corpus := fakeCorpus{total: 1200, base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	// Watermark = issueDate of rank 600. "Strictly after" excludes rank 600 and
	// everything older, so the expected result is ranks 0..599 (600 docs).
	const watermarkRank = 600
	since, err := time.Parse(dateLayout, corpus.issued(watermarkRank))
	if err != nil {
		t.Fatalf("parse watermark: %v", err)
	}
	since = since.UTC()

	var cap captured
	srv := newCorpusServer(t, corpus, &cap)
	s := testSource(t, srv)

	docs, err := s.Discover(context.Background(), since, "")
	if err != nil {
		t.Fatalf("Discover incremental: %v", err)
	}
	hits := len(cap.all())
	// Rank 600 sits on page 2 (1-based) at pageSize 500, so we read at most 2
	// pages, never the whole corpus — the watermark short-circuits paging.
	wantStopPage := watermarkRank/sweepPageSize + 1
	if hits > wantStopPage {
		t.Errorf("incremental made %d requests, want <= %d (must stop at the watermark)", hits, wantStopPage)
	}
	if hits >= (corpus.total+sweepPageSize-1)/sweepPageSize {
		t.Errorf("incremental paged the entire corpus (%d requests); watermark did not short-circuit", hits)
	}
	if len(docs) != watermarkRank {
		t.Fatalf("incremental returned %d docs, want %d (strictly newer than watermark)", len(docs), watermarkRank)
	}
	for i, d := range docs {
		if !d.IssuedAt.After(since) {
			t.Fatalf("doc %d (%s) is not strictly after watermark %s — watermark leaked", i, d.IssuedAt, since)
		}
	}
}

// TestDiscover_NonSbvKeywordSearch proves a non-empty keyword switches to a title
// search over the non-SBV agency ids, sending the keyword on every request.
func TestDiscover_NonSbvKeywordSearch(t *testing.T) {
	corpus := fakeCorpus{total: 30, base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var cap captured
	srv := newCorpusServer(t, corpus, &cap)
	s := testSource(t, srv)

	docs, err := s.Discover(context.Background(), time.Time{}, "  an ninh mạng  ")
	if err != nil {
		t.Fatalf("Discover keyword search: %v", err)
	}

	reqs := cap.all()
	if len(reqs) != 1 {
		t.Fatalf("keyword search made %d requests, want 1 (30 docs < one page)", len(reqs))
	}
	if got := reqs[0].Keyword; got != "an ninh mạng" {
		t.Fatalf("keyword = %q, want %q (trimmed)", got, "an ninh mạng")
	}
	if !reflect.DeepEqual(reqs[0].AgencyIds, nonSbvIDs) {
		t.Fatalf("keyword search agencyIds = %v, want %v (non-SBV set)", reqs[0].AgencyIds, nonSbvIDs)
	}
	if len(docs) != corpus.total {
		t.Fatalf("keyword search returned %d docs, want %d", len(docs), corpus.total)
	}
}

// TestDiscover_KeywordSearchSkippedWithoutAgencies proves a keyword search with no
// non-SBV agency set is refused — it would otherwise query every central issuer
// with no downstream scope filter — rather than run unscoped.
func TestDiscover_KeywordSearchSkippedWithoutAgencies(t *testing.T) {
	corpus := fakeCorpus{total: 50, base: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var cap captured
	srv := newCorpusServer(t, corpus, &cap)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client := &http.Client{Transport: rewriteTransport{target: target, base: http.DefaultTransport}}
	s := New(client, nil, sbvIDs, nil, nil) // no non-SBV agencies configured

	docs, err := s.Discover(context.Background(), time.Time{}, "an ninh mạng")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("got %d docs, want 0 (keyword search must be skipped without agencies)", len(docs))
	}
	if n := len(cap.all()); n != 0 {
		t.Fatalf("made %d requests, want 0 (must not run an unscoped keyword search)", n)
	}
}
