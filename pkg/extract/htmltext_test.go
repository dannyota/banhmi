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
	const want = "Điều 1. Phạm vi điều chỉnh\nThông tư này quy định & hướng dẫn.\nKhoản Nội dung"
	if got != want {
		t.Fatalf("HTML = %q, want %q", got, want)
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

func TestHTMLTextCompatibility(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"nfc-and-entities", "<p>A &amp; B</p><p>e\u0301</p>", "A & B\né"},
		{"blank-lines", "<p>A  B</p><div></div><div></div><p>C<br>D</p>", "A B\n\nC\nD"},
		{"discarded", "<div><style>x</style><script>y</script><noscript>z</noscript></div>", ""},
		{"vn", "<p>Điều 1</p>", "Điều 1"},
		{"my", "<p>Section 1</p>", "Section 1"},
		{"sg", "<p>Section 2</p>", "Section 2"},
		{"id", "<p>Pasal 1</p>", "Pasal 1"},
		{"th", "<p>มาตรา 1</p>", "มาตรา 1"},
		{"kh", "<p>Article 1</p>", "Article 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := HTML(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("HTML = (%q, %v), want (%q, nil)", got, err, tc.want)
			}
		})
	}
}
