package sbvhanoi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"danny.vn/banhmi/pkg/ingest"
)

func TestParseListPage(t *testing.T) {
	html := `<a href="?_4_WAR_portalvbpqportlet_cur=2">2</a>
<tbody class="table-data">
<tr>
<td class="table-cell vbpq-sohieuvanban first"><a href="https://sbv.hanoi.gov.vn/van-ban-quy-pham-phap-luat?p_p_id=4_WAR_portalvbpqportlet&amp;_4_WAR_portalvbpqportlet_id=77102&amp;_4_WAR_portalvbpqportlet_mvcPath=%2Fhtml%2Fportlet%2Flist%2Fview_detail.jsp">2345/QĐ-NHNN</a></td>
<td class="table-cell"><a href="#">QĐ về triển khai các giải pháp an toàn, bảo mật trong thanh toán trực tuyến</a></td>
<td class="table-cell vbpq-ngaybanhanh"><a href="#">18/12/2023</a></td>
<td class="table-cell vbpq-nguoiky"><a href="#">Phạm Tiến Dũng</a></td>
<td class="table-cell vbpq-file-dinh-kem last"></td>
</tr>
</tbody>`

	docs, last := parseListPage(html, defaultBaseURL)
	if last != 2 {
		t.Fatalf("last page = %d, want 2", last)
	}
	if len(docs) != 1 {
		t.Fatalf("docs = %d, want 1", len(docs))
	}
	d := docs[0]
	if d.ExternalID != "77102" || d.Number != "2345/QĐ-NHNN" {
		t.Fatalf("doc identity = %q/%q", d.ExternalID, d.Number)
	}
	if d.DocType != "Quyết định" {
		t.Fatalf("doc type = %q", d.DocType)
	}
	if got := d.IssuedAt.Format("2006-01-02"); got != "2023-12-18" {
		t.Fatalf("issued = %s", got)
	}
	if !strings.Contains(d.DetailURL, "_4_WAR_portalvbpqportlet_id=77102") {
		t.Fatalf("detail URL missing id: %s", d.DetailURL)
	}
}

func TestListURLUsesBroadPageSizeAndOptionalKeyword(t *testing.T) {
	src := &Source{baseURL: defaultBaseURL}

	u, err := url.Parse(src.listURL(2, ""))
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	q := u.Query()
	if got := q.Get("_4_WAR_portalvbpqportlet_delta"); got != "200" {
		t.Fatalf("delta = %q, want 200", got)
	}
	if got := q.Get("_4_WAR_portalvbpqportlet_cur"); got != "2" {
		t.Fatalf("cur = %q, want 2", got)
	}
	if got := q.Get("_4_WAR_portalvbpqportlet_keyword"); got != "" {
		t.Fatalf("keyword = %q, want empty", got)
	}

	u, err = url.Parse(src.listURL(1, "2345/QĐ-NHNN"))
	if err != nil {
		t.Fatalf("parse keyword URL: %v", err)
	}
	if got := u.Query().Get("_4_WAR_portalvbpqportlet_keyword"); got != "2345/QĐ-NHNN" {
		t.Fatalf("keyword = %q", got)
	}
}

func TestFetchDetailParsesMetadataAndAttachment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, detailHTML())
	}))
	defer srv.Close()

	src := testSource(srv)
	doc, err := src.FetchDetail(context.Background(), ingest.DetailRef{ExternalID: "77102"})
	if err != nil {
		t.Fatalf("FetchDetail: %v", err)
	}
	if doc.Number != "2345/QĐ-NHNN" {
		t.Fatalf("number = %q", doc.Number)
	}
	if doc.EffectiveAt.Format("2006-01-02") != "2024-07-01" {
		t.Fatalf("effective = %s", doc.EffectiveAt.Format("2006-01-02"))
	}
	if len(doc.Files) != 1 {
		t.Fatalf("files = %d, want 1", len(doc.Files))
	}
	f := doc.Files[0]
	if f.Name != "120240628145341_2345.pdf" || f.Ext != "pdf" || f.Kind != "main" {
		t.Fatalf("file = %+v", f)
	}
	if !strings.HasPrefix(f.URL, srv.URL+"/documents/") {
		t.Fatalf("file URL = %s", f.URL)
	}
}

func TestDownloadStreamsAndHashes(t *testing.T) {
	payload := []byte("%PDF test")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	src := testSource(srv)
	var out bytes.Buffer
	n, sha, err := src.Download(context.Background(), ingest.FileRef{URL: srv.URL + "/doc.pdf", Name: "doc.pdf", Ext: "pdf"}, &out)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if n != int64(len(payload)) || out.String() != string(payload) {
		t.Fatalf("downloaded %d/%q", n, out.String())
	}
	if sha != "bb094c25184067415837d8dc66cfa65366384a80625877252719369a2dc80575" {
		t.Fatalf("sha = %s", sha)
	}
}

func testSource(srv *httptest.Server) *Source {
	return &Source{
		http:    srv.Client(),
		log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		baseURL: srv.URL,
	}
}

func detailHTML() string {
	return `<div class="vbpq-detail">
<table style="width: 100%;">
<tr><td class="vbpq-detail-col1"> Số/Kí hiệu </td><td>2345/QĐ-NHNN</td></tr>
<tr><td class="vbpq-detail-col1"> Ngày ban hành </td><td>18/12/2023</td></tr>
<tr><td class="vbpq-detail-col1"> Ngày có hiệu lực </td><td>01/07/2024</td></tr>
<tr><td class="vbpq-detail-col1"> Người ký </td><td>Phạm Tiến Dũng</td></tr>
<tr><td class="vbpq-detail-col1"> Trích yếu </td><td>QĐ về triển khai các giải pháp an toàn, bảo mật trong thanh toán trực tuyến</td></tr>
<tr><td class="vbpq-detail-col1"> Cơ quan ban hành </td><td>NHNN Việt Nam</td></tr>
<tr><td class="vbpq-detail-col1"> Thể loại </td><td>Quyết định</td></tr>
</table>
</div>
<div class="vbpq-attachfile">
<div class="vbpq-tldk"> Tài liệu đính kèm </div>
<table><tr>
<td><a href="/documents/160566/5681154/120240628145341_2345.pdf/f34593b2-ad00-4271-9d38-8c1687c02be9">120240628145341_2345.pdf</a></td>
</tr></table>
</div>
<div class="vbpq-attachfile"><div class="vbpq-tldk"> Văn bản khác </div></div>`
}
