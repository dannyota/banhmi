package congbao

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"
)

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	req.Host = rt.target.Host
	return rt.base.RoundTrip(req)
}

func testSource(t *testing.T, handler http.Handler) *Source {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client := &http.Client{Transport: rewriteTransport{target: target, base: http.DefaultTransport}}
	return New(client, nil)
}

func TestDiscover_RSSParsesAndFilters(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(rssPath, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != crawlerUA {
			t.Fatalf("User-Agent = %q, want %q", got, crawlerUA)
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss><channel>
  <item>
    <title></title>
    <description><![CDATA[<b>An toàn hệ thống</b> &amp; ngân hàng]]></description>
    <link>https://congbao.chinhphu.vn/van-ban/thong-tu-so-09-2020-tt-nhnn-144532.htm?utm=1</link>
    <pubDate>Mon, 25 May 2026 10:00:00 GMT</pubDate>
  </item>
  <item>
    <title>Nghị định về chính sách</title>
    <description></description>
    <guid>/van-ban/nghi-dinh-so-148-2026-nd-cp-222.htm</guid>
    <pubDate>Mon, 24 May 2026 10:00:00 GMT</pubDate>
  </item>
  <item>
    <title>Malformed no id</title>
    <link>https://congbao.chinhphu.vn/van-ban/no-id.htm</link>
    <pubDate>Mon, 26 May 2026 10:00:00 GMT</pubDate>
  </item>
</channel></rss>`))
	})
	s := testSource(t, mux)

	docs, err := s.Discover(context.Background(), time.Time{}, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("Discover returned %d docs, want 2", len(docs))
	}

	first := docs[0]
	if first.SourceID != SourceID {
		t.Fatalf("SourceID = %q, want %q", first.SourceID, SourceID)
	}
	if first.ExternalID != "144532" {
		t.Fatalf("ExternalID = %q, want 144532", first.ExternalID)
	}
	if first.Number != "09/2020/TT-NHNN" {
		t.Fatalf("Number = %q, want 09/2020/TT-NHNN", first.Number)
	}
	if first.DocType != "Thông tư" {
		t.Fatalf("DocType = %q, want Thông tư", first.DocType)
	}
	if first.Title != "An toàn hệ thống & ngân hàng" {
		t.Fatalf("Title = %q, want cleaned description", first.Title)
	}
	if first.DetailURL != "https://congbao.chinhphu.vn/van-ban/thong-tu-so-09-2020-tt-nhnn-144532.htm?utm=1" {
		t.Fatalf("DetailURL = %q", first.DetailURL)
	}
	if first.PublishedAt.IsZero() {
		t.Fatal("PublishedAt is zero")
	}

	second := docs[1]
	if second.ExternalID != "222" {
		t.Fatalf("second ExternalID = %q, want 222", second.ExternalID)
	}
	if second.Number != "148/2026/NĐ-CP" {
		t.Fatalf("second Number = %q, want 148/2026/NĐ-CP", second.Number)
	}
	if second.DocType != "Nghị định" {
		t.Fatalf("second DocType = %q, want Nghị định", second.DocType)
	}
	if second.Title != "Nghị định về chính sách" {
		t.Fatalf("second Title = %q, want title fallback", second.Title)
	}
	if second.DetailURL != "https://congbao.chinhphu.vn/van-ban/nghi-dinh-so-148-2026-nd-cp-222.htm" {
		t.Fatalf("second DetailURL = %q, want absolute URL", second.DetailURL)
	}
}

func TestDiscover_WatermarkStrictlyAfter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(rssPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<rss><channel>
  <item>
    <description>new</description>
    <link>https://congbao.chinhphu.vn/van-ban/quyet-dinh-so-840-qd-ttg-1.htm</link>
    <pubDate>Mon, 25 May 2026 10:00:00 GMT</pubDate>
  </item>
  <item>
    <description>at watermark</description>
    <link>https://congbao.chinhphu.vn/van-ban/quyet-dinh-so-841-qd-ttg-2.htm</link>
    <pubDate>Mon, 24 May 2026 10:00:00 GMT</pubDate>
  </item>
</channel></rss>`))
	})
	s := testSource(t, mux)
	since := parseRSSDate("Mon, 24 May 2026 10:00:00 GMT")

	docs, err := s.Discover(context.Background(), since, "")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(docs) != 1 || docs[0].ExternalID != "1" {
		t.Fatalf("docs = %+v, want only id=1 strictly after watermark", docs)
	}
}

func TestSearchByNumber_VerifiesExactMatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/van-ban/nhom/vbqpp", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Origin"); got != baseURL {
			t.Fatalf("Origin = %q, want %q", got, baseURL)
		}
		var req searchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Query != "14/2022/NĐ-CP" {
			t.Fatalf("query = %q, want exact Vietnamese doc number", req.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
		  "success": true,
		  "total": 2,
		  "data": [
		    {"id_van_ban":36738,"so_ky_hieu":"13/2022/NĐ-CP","loai_van_ban":"Nghị định"},
		    {
		      "id_van_ban":36772,
		      "so_ky_hieu":"14/2022/NĐ-CP",
		      "tieu_de":"14/2022/NĐ-CP",
		      "trich_yeu":"sửa đổi, bổ sung một số điều",
		      "ngay_ban_hanh":"2022-01-27T00:00:00",
		      "ngay_co_hieu_luc":null,
		      "loai_van_ban":"Nghị định",
		      "ten_co_quan":["CHÍNH PHỦ"],
		      "nguoi_ky":"VŨ ĐỨC ĐAM",
		      "danh_sach_dang_trong_cong_bao":"Công báo số 223 + 224 ngày 27/01",
		      "danh_sach_ky_cong_bao":[{"ten":"223 + 224","ngay_ban_hanh":"2022-01-27T00:00:00"}],
		      "danh_sach_tep_van_ban":[
		        {"duong_dan":"https://congbaocdn.chinhphu.vn/doc.pdf","thu_tu":2,"ten_file":"doc","file_extension":"pdf"},
		        {"duong_dan":"https://congbaocdn.chinhphu.vn/doc.doc","thu_tu":1,"ten_file":"doc","file_extension":"doc"}
		      ]
		    }
		  ]
		}`))
	})
	s := testSource(t, mux)

	doc, ok, err := s.SearchByNumber(context.Background(), "14/2022/NĐ-CP", "")
	if err != nil {
		t.Fatalf("SearchByNumber: %v", err)
	}
	if !ok {
		t.Fatal("SearchByNumber did not find exact match")
	}
	if doc.ExternalID != "36772" {
		t.Fatalf("ExternalID = %q, want 36772", doc.ExternalID)
	}
	if doc.Number != "14/2022/NĐ-CP" {
		t.Fatalf("Number = %q", doc.Number)
	}
	if doc.DetailURL != "https://congbao.chinhphu.vn/van-ban/nghi-dinh-so-14-2022-nd-cp-36772.htm" {
		t.Fatalf("DetailURL = %q", doc.DetailURL)
	}
	if len(doc.Files) != 2 {
		t.Fatalf("Files len = %d, want 2", len(doc.Files))
	}
	if doc.Files[0].Ext != "doc" || doc.Files[0].Name != "doc.doc" {
		t.Fatalf("first file = %+v, want sorted doc file with extension", doc.Files[0])
	}
	if doc.Files[1].Ext != "pdf" || doc.Files[1].Name != "doc.pdf" {
		t.Fatalf("second file = %+v, want sorted pdf file with extension", doc.Files[1])
	}
	if doc.IssuedAt.IsZero() || doc.GazetteDate.IsZero() {
		t.Fatalf("dates not parsed: issued=%v gazette=%v", doc.IssuedAt, doc.GazetteDate)
	}
}

func TestSearchByNumber_NoExactMatch(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/van-ban/nhom/vbqpp", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"total":1,"data":[{"id_van_ban":1,"so_ky_hieu":"13/2022/NĐ-CP"}]}`))
	})
	s := testSource(t, mux)

	doc, ok, err := s.SearchByNumber(context.Background(), "14/2022/NĐ-CP", "")
	if err != nil {
		t.Fatalf("SearchByNumber: %v", err)
	}
	if ok || doc != nil {
		t.Fatalf("SearchByNumber returned (%+v, %v), want no match", doc, ok)
	}
}

func TestParseSlug(t *testing.T) {
	cases := []struct {
		in       string
		wantID   string
		wantSlug string
	}{
		{
			in:       "https://congbao.chinhphu.vn/van-ban/thong-tu-so-09-2020-tt-nhnn-144532.htm?utm=1#x",
			wantID:   "144532",
			wantSlug: "thong-tu-so-09-2020-tt-nhnn",
		},
		{
			in:       "/van-ban/quyet-dinh-so-840-qd-ttg-123.htm/",
			wantID:   "123",
			wantSlug: "quyet-dinh-so-840-qd-ttg",
		},
		{
			in:       "no-id.htm",
			wantID:   "",
			wantSlug: "no-id",
		},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			gotID, gotSlug := parseSlug(c.in)
			if gotID != c.wantID || gotSlug != c.wantSlug {
				t.Fatalf("parseSlug(%q) = (%q, %q), want (%q, %q)", c.in, gotID, gotSlug, c.wantID, c.wantSlug)
			}
		})
	}
}

func TestParseNumberAndType(t *testing.T) {
	cases := []struct {
		slug    string
		number  string
		docType string
	}{
		{"thong-tu-so-09-2020-tt-nhnn", "09/2020/TT-NHNN", "Thông tư"},
		{"nghi-dinh-so-148-2026-nd-cp", "148/2026/NĐ-CP", "Nghị định"},
		{"quyet-dinh-so-840-qd-ttg", "840/QĐ-TTG", "Quyết định"},
		{"van-ban-hop-nhat-so-43-vbhn-nhnn", "43/VBHN-NHNN", "Văn bản hợp nhất"},
	}
	for _, c := range cases {
		t.Run(c.slug, func(t *testing.T) {
			number, docType := parseNumberAndType(c.slug)
			if number != c.number || string(docType) != c.docType {
				t.Fatalf("parseNumberAndType(%q) = (%q, %q), want (%q, %q)", c.slug, number, docType, c.number, c.docType)
			}
		})
	}
}

func TestCanonicalDetailURL(t *testing.T) {
	cases := []struct {
		link string
		guid string
		want string
	}{
		{link: "https://congbao.chinhphu.vn/van-ban/a-1.htm", want: "https://congbao.chinhphu.vn/van-ban/a-1.htm"},
		{link: "", guid: "/van-ban/a-1.htm", want: "https://congbao.chinhphu.vn/van-ban/a-1.htm"},
		{link: "//congbao.chinhphu.vn/van-ban/a-1.htm", want: "https://congbao.chinhphu.vn/van-ban/a-1.htm"},
	}
	for _, c := range cases {
		t.Run(c.link+c.guid, func(t *testing.T) {
			if got := canonicalDetailURL(c.link, c.guid); got != c.want {
				t.Fatalf("canonicalDetailURL(%q, %q) = %q, want %q", c.link, c.guid, got, c.want)
			}
		})
	}
}

func TestParseRSSDateLayouts(t *testing.T) {
	inputs := []string{
		"Mon, 25 May 2026 10:00:00 GMT",
		"25 May 2026 10:00:00 GMT",
		"25 May 2026 10:00:00 +0000",
	}
	var got []time.Time
	for _, in := range inputs {
		tm := parseRSSDate(in)
		if tm.IsZero() {
			t.Fatalf("parseRSSDate(%q) returned zero", in)
		}
		got = append(got, tm)
	}
	if !reflect.DeepEqual(got[0], got[1]) || !reflect.DeepEqual(got[0], got[2]) {
		t.Fatalf("parsed dates differ: %v", got)
	}
}
