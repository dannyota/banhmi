package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// ErrByteLimit identifies incomplete content that must never be published.
var ErrByteLimit = errors.New("staging byte limit exceeded")

// Stager streams into a bounded local temporary file before catalog registration
// and immutable object publication. Store and Catalog must be in-country ports.
// It never adds retention references or accepts evidence. Failed publication can
// leave unreferenced objects for the caller's garbage collector.
type Stager struct {
	Store   Store
	Catalog Catalog
	TempDir string
}

// Stage invokes produce once, hashes exactly the written bytes, and publishes
// only after successful completion. Metadata keys come from the trusted caller.
// The catalog issues the final object reference before Store.Put uses it.
func (s Stager) Stage(ctx context.Context, m Metadata, limit int64, produce func(io.Writer) error) (Metadata, error) {
	if m.CaptureRef == "" || strings.ContainsAny(m.CaptureRef, "\r\n\t /:\\") {
		return Metadata{}, fmt.Errorf("stage: invalid capture key")
	}
	if s.Store == nil || s.Catalog == nil || produce == nil || limit <= 0 {
		return Metadata{}, fmt.Errorf("stage %s: invalid staging configuration", m.CaptureRef)
	}
	if err := ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("stage %s: %w", m.CaptureRef, err)
	}
	f, err := os.CreateTemp(s.TempDir, "corpus-stage-*")
	if err != nil {
		return Metadata{}, fmt.Errorf("stage %s: temporary object unavailable", m.CaptureRef)
	}
	defer func() {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}()
	w := &stagingWriter{ctx: ctx, dst: f, digest: sha256.New(), remaining: limit}
	if err := produce(w); err != nil {
		if errors.Is(err, ErrByteLimit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Metadata{}, fmt.Errorf("stage %s: %w", m.CaptureRef, err)
		}
		return Metadata{}, fmt.Errorf("stage %s: stream failed", m.CaptureRef)
	}
	if w.failure != nil {
		return Metadata{}, fmt.Errorf("stage %s: write failed", m.CaptureRef)
	}
	if err := ctx.Err(); err != nil {
		return Metadata{}, fmt.Errorf("stage %s: %w", m.CaptureRef, err)
	}
	m.SHA256 = hex.EncodeToString(w.digest.Sum(nil))
	m.ByteSize = w.length
	m.ObjectRef = "sha256/" + m.SHA256
	if err := m.Validate(); err != nil {
		return Metadata{}, fmt.Errorf("stage %s: invalid metadata", m.CaptureRef)
	}
	registered, err := s.Catalog.Register(ctx, m)
	if err != nil {
		return Metadata{}, fmt.Errorf("stage %s: catalog registration failed", m.CaptureRef)
	}
	if registered.Validate() != nil || registered.ObjectRef != m.ObjectRef || registered.SHA256 != m.SHA256 || registered.ByteSize != m.ByteSize {
		return Metadata{}, fmt.Errorf("stage %s: catalog content mismatch", m.CaptureRef)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("stage %s: rewind failed", m.CaptureRef)
	}
	object, err := s.Store.Put(ctx, registered, f)
	if err != nil {
		return Metadata{}, fmt.Errorf("stage %s: object publication failed", m.CaptureRef)
	}
	if object.ObjectRef != registered.ObjectRef || object.SHA256 != m.SHA256 || object.ByteSize != m.ByteSize {
		return Metadata{}, fmt.Errorf("stage %s: published content mismatch", m.CaptureRef)
	}
	return registered, nil
}

type stagingWriter struct {
	ctx       context.Context
	dst       io.Writer
	digest    hash.Hash
	remaining int64
	length    int64
	failure   error
}

func (w *stagingWriter) Write(p []byte) (int, error) {
	if w.failure != nil {
		return 0, w.failure
	}
	if err := w.ctx.Err(); err != nil {
		w.failure = err
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		w.failure = ErrByteLimit
		return 0, w.failure
	}
	n, err := w.dst.Write(p)
	_, _ = w.digest.Write(p[:n])
	w.length += int64(n)
	w.remaining -= int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	w.failure = err
	return n, err
}
