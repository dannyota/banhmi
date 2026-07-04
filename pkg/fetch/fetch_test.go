package fetch_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/pkg/fetch"
)

func TestChromeTransport_BI(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	client := &http.Client{Transport: fetch.ChromeTransport(), Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://jdih.bi.go.id/api/WebJDIH/GetDataStatistikProdukHukum", nil)
	req.Header.Set("User-Agent", fetch.DefaultUA)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("BI API: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("BI API: status %d", resp.StatusCode)
	}
	t.Logf("BI API: status %d", resp.StatusCode)
}

func TestChromeTransport_BPK_PDF(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	// BPK PDF downloads don't need Cloudflare cookies — test utls alone.
	client := &http.Client{Transport: fetch.ChromeTransport(), Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://peraturan.bpk.go.id/Download/413974/POJK%205%20Tahun%202026.pdf", nil)
	req.Header.Set("User-Agent", fetch.DefaultUA)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("BPK PDF: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("BPK PDF: status %d (want 200)", resp.StatusCode)
	}
	buf := make([]byte, 4)
	resp.Body.Read(buf)
	if !strings.HasPrefix(string(buf), "%PDF") {
		t.Fatalf("BPK PDF: not a PDF (first 4 bytes: %q)", buf)
	}
	t.Logf("BPK PDF: status 200, begins %%PDF")
}

func TestCloudflareMinter_BPK(t *testing.T) {
	if testing.Short() {
		t.Skip("network test")
	}
	minter := &fetch.CloudflareMinter{
		ChallengeURL: "https://peraturan.bpk.go.id/Search?jenis=80",
		Timeout:      90 * time.Second,
	}
	cookies, ua, err := minter.Mint(context.Background())
	if err != nil {
		t.Fatalf("Cloudflare mint: %v", err)
	}
	if !strings.Contains(cookies, "cf_clearance") {
		t.Fatalf("no cf_clearance in cookies: %s", cookies[:min(len(cookies), 100)])
	}
	t.Logf("minted: ua=%s cookies=%d chars", ua[:40], len(cookies))

	// Verify reuse: fetch a listing page with the minted cookies.
	client := &http.Client{Transport: fetch.ChromeTransport(), Timeout: 30 * time.Second}
	req, _ := http.NewRequestWithContext(context.Background(), "GET",
		"https://peraturan.bpk.go.id/Search?jenis=80", nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cookie", cookies)
	req.Header.Set("Accept", "text/html,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("BPK listing: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("BPK listing: status %d after mint (want 200)", resp.StatusCode)
	}
	t.Logf("BPK listing: status 200 (cookies reused OK)")
}
