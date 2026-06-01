package vbpl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"

	"danny.vn/banhmi/pkg/ingest"
)

// Download streams a file reference into w, computing its SHA-256 in flight, and
// returns the number of bytes written and the lowercase-hex digest. ref.URL is a
// presigned FPT Cloud S3 URL (signed in the query string, ~24h expiry), so it
// needs only a descriptive User-Agent — no Origin/Referer and no AIA chain
// chasing. Transport errors are returned so the caller can record the failure
// and continue. Callers must not log the signed URL query string.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.URL, nil)
	if err != nil {
		return 0, "", fmt.Errorf("download %s: build request: %w", ref.Name, err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := s.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("download %s: %w", ref.Name, err)
	}
	defer drainClose(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, "", fmt.Errorf("download %s: status %d", ref.Name, resp.StatusCode)
	}

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
	if err != nil {
		return n, "", fmt.Errorf("download %s: copy body: %w", ref.Name, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
