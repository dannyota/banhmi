package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"sync"
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
	for _, mode := range []string{"complete", "limit", "ignored-limit", "stream", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			store := &stagingStore{}
			s := Stager{Store: store, Catalog: stagingCatalog{}, TempDir: t.TempDir()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			limit := int64(10)
			if mode == "limit" || mode == "ignored-limit" { limit = 2 }
			m, err := s.Stage(ctx, Metadata{MediaType: "text/plain", CaptureSource: "test", CaptureRef: "capture-test", CapturedAt: time.Now()}, limit, func(w io.Writer) error {
				_, err := io.Copy(w, strings.NewReader("hello"))
				if mode == "ignored-limit" { return nil }
				if mode == "stream" { return errors.New("broken stream") }
				if mode == "cancel" { cancel() }
				return err
			})
			if mode != "complete" {
				if err == nil || store.puts != 0 { t.Fatalf("error=%v published=%d", err, store.puts) }
				if (mode == "limit" || mode == "ignored-limit") && !errors.Is(err, ErrByteLimit) { t.Fatalf("limit classification lost: %v", err) }
				return
			}
			h := sha256.Sum256([]byte("hello"))
			if err != nil || m.SHA256 != hex.EncodeToString(h[:]) || m.ByteSize != 5 || store.data != "hello" || store.puts != 1 {
				t.Fatalf("metadata=%+v error=%v puts=%d", m, err, store.puts)
			}
		})
	}
}

type concurrentStagingStore struct {
	Store
	mu sync.Mutex
	objects map[string]string
}

func (s *concurrentStagingStore) Put(_ context.Context, m Metadata, r io.Reader) (Object, error) {
	b, err := io.ReadAll(r)
	if err != nil { return Object{}, err }
	digest := sha256.Sum256(b)
	if m.SHA256 != hex.EncodeToString(digest[:]) { return Object{}, errors.New("mixed staging bytes") }
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[m.ObjectRef] = string(b)
	return Object{ObjectRef: m.ObjectRef, SHA256: m.SHA256, ByteSize: int64(len(b))}, nil
}

func TestConcurrentStagingKeepsStreamsSeparate(t *testing.T) {
	store := &concurrentStagingStore{objects: make(map[string]string)}
	s := Stager{Store: store, Catalog: stagingCatalog{}, TempDir: t.TempDir()}
	start := make(chan struct{})
	results := make(chan error, 4)
	for _, content := range []string{"one", "two", "three", "four"} {
		go func(content string) {
			<-start
			m, err := s.Stage(context.Background(), Metadata{CaptureSource: "test", CaptureRef: "capture-"+content, MediaType: "text/plain", CapturedAt: time.Now()}, 16, func(w io.Writer) error { _, err := io.WriteString(w, content); return err })
			if err == nil && m.ByteSize != int64(len(content)) { err = errors.New("mixed staging length") }
			results <- err
		}(content)
	}
	close(start)
	for range 4 { if err := <-results; err != nil { t.Fatal(err) } }
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.objects) != 4 { t.Fatalf("objects=%d", len(store.objects)) }
}
