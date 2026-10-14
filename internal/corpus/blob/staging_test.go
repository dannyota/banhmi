package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type stagingStore struct {
	Store
	puts int
	data string
}

func (s *stagingStore) Put(_ context.Context, m Metadata, r io.Reader) (Object, error) {
	s.puts++
	b, err := io.ReadAll(r)
	s.data = string(b)
	return Object{ObjectRef: m.ObjectRef, ByteSize: int64(len(b)), SHA256: m.SHA256}, err
}

type stagingCatalog struct { Catalog }

func (stagingCatalog) Register(_ context.Context, m Metadata) (Metadata, error) { return m, nil }

func TestStagingPublishesOnlyCompleteContent(t *testing.T) {
	for _, mode := range []string{"complete", "limit", "stream", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store := &stagingStore{}
			s := Stager{Store: store, Catalog: stagingCatalog{}, TempDir: t.TempDir()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limit := int64(10)
			if mode == "limit" { limit = 2 }
			m, err := s.Stage(ctx, Metadata{MediaType: "text/plain", CaptureSource: "test", CaptureRef: "capture-test", CapturedAt: time.Now()}, limit, func(w io.Writer) error {
				_, err := io.Copy(w, strings.NewReader("hello"))
				if mode == "stream" { return errors.New("broken stream") }
				if mode == "cancel" { cancel() }
				return err
			})
			if mode != "complete" {
				if err == nil || store.puts != 0 { t.Fatalf("error=%v published=%d", err, store.puts) }
				return
			}
			h := sha256.Sum256([]byte("hello"))
			if err != nil || m.SHA256 != hex.EncodeToString(h[:]) || m.ByteSize != 5 || store.data != "hello" || store.puts != 1 {
				t.Fatalf("metadata=%+v error=%v puts=%d", m, err, store.puts)
			}
		})
	}
}
