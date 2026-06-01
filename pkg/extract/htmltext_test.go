package extract

import (
	"strings"
	"testing"
)

func TestHTML(t *testing.T) {
	in := `<!DOCTYPE html><html><head><title>Document Content</title>` +
		`<style>p{margin:0}</style></head><body>` +
		`<div><p>Điều 1. Phạm vi điều chỉnh</p>` +
		`<p>Thông tư này quy định &amp; hướng dẫn.</p>` +
		`<script>track()</script>` +
		`<table><tr><td>Khoản</td><td>Nội dung</td></tr></table></div></body></html>`
	got, err := HTML(in)
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if strings.Contains(got, "Document Content") {
		t.Fatalf("head/title text not dropped: %q", got)
	}
	if !strings.Contains(got, "Điều 1. Phạm vi điều chỉnh") {
		t.Fatalf("missing heading text: %q", got)
	}
	if !strings.Contains(got, "quy định & hướng dẫn") {
		t.Fatalf("entity not decoded: %q", got)
	}
	if strings.Contains(got, "track()") {
		t.Fatalf("script content not dropped: %q", got)
	}
	if !strings.Contains(got, "Phạm vi điều chỉnh\nThông tư này") {
		t.Fatalf("blocks not line-separated: %q", got)
	}
	if !strings.Contains(got, "Khoản Nội dung") {
		t.Fatalf("table cells not space-joined: %q", got)
	}
}

func TestHTMLEmpty(t *testing.T) {
	got, err := HTML("<div><script>x()</script></div>")
	if err != nil {
		t.Fatalf("HTML: %v", err)
	}
	if got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}
