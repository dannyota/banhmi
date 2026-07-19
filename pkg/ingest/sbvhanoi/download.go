package sbvhanoi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"danny.vn/banhmi/pkg/ingest"
)

// Download streams an SBV Hanoi attachment into w while computing its SHA-256.
func (s *Source) Download(ctx context.Context, ref ingest.FileRef, w io.Writer) (int64, string, error) {
	if ref.URL == "" {
		return 0, "", fmt.Errorf("download: empty url")
	}
	resp, err := s.get(ctx, ref.URL)
	if err != nil {
		return 0, "", fmt.Errorf("download %s: %w", ref.Name, err)
	}
	defer drainClose(resp.Body)

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), resp.Body)
	if err != nil {
		return n, "", fmt.Errorf("download %s: copy body: %w", ref.Name, err)
	}
	return n, hex.EncodeToString(h.Sum(nil)), nil
}
