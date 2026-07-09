package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloud.google.com/go/storage"
)

const gcsFilesPrefix = "files/"

// gcsStorage returns the shared GCS client, initializing it lazily on first
// use. Returns nil if dataBucket is empty (GCS disabled).
func (a *Activities) gcsStorage() *storage.Client {
	if a.dataBucket == "" {
		return nil
	}
	a.gcsOnce.Do(func() {
		c, err := storage.NewClient(context.Background())
		if err != nil {
			a.log.Warn("gcs cache: failed to create storage client", "err", err)
			return
		}
		a.gcsClient = c
	})
	return a.gcsClient
}

// validGCSName rejects names containing path separators or traversal patterns.
func validGCSName(name string) bool {
	return name != "" &&
		!strings.Contains(name, "/") &&
		!strings.Contains(name, "\\") &&
		!strings.Contains(name, "..")
}

// uploadToGCS uploads a local file to GCS as a best-effort cache. Errors are
// logged as warnings and never propagated — a GCS failure must not block a
// fetch.
func (a *Activities) uploadToGCS(ctx context.Context, name string) {
	client := a.gcsStorage()
	if client == nil {
		return
	}
	if !validGCSName(name) {
		a.log.Warn("gcs cache: invalid file name, skipping upload", "file", name)
		return
	}

	localPath := filepath.Join(a.storageDir, name)
	f, err := os.Open(localPath)
	if err != nil {
		a.log.Warn("gcs cache: open local file for upload", "file", name, "err", err)
		return
	}
	defer func() { _ = f.Close() }()

	uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	obj := client.Bucket(a.dataBucket).Object(gcsFilesPrefix + name)
	w := obj.NewWriter(uploadCtx)
	if _, err := io.Copy(w, f); err != nil {
		_ = w.Close()
		a.log.Warn("gcs cache: upload failed", "file", name, "bucket", a.dataBucket, "err", err)
		return
	}
	if err := w.Close(); err != nil {
		a.log.Warn("gcs cache: finalize upload failed", "file", name, "bucket", a.dataBucket, "err", err)
		return
	}
	a.log.Debug("gcs cache: uploaded", "file", name, "bucket", a.dataBucket)
}

// ensureLocalFile guarantees that name exists under storageDir. If it is
// already present locally, it returns immediately. Otherwise it downloads
// from GCS (gs://{dataBucket}/files/{name}). When dataBucket is empty and the
// file is missing locally, it returns an error — there is no fallback.
func (a *Activities) ensureLocalFile(ctx context.Context, name string) error {
	localPath := filepath.Join(a.storageDir, name)
	if _, err := os.Stat(localPath); err == nil {
		return nil
	}

	if !validGCSName(name) {
		return fmt.Errorf("invalid file name %q", name)
	}

	client := a.gcsStorage()
	if client == nil {
		return fmt.Errorf("file %s not found locally and no GCS data bucket configured", name)
	}

	a.log.Info("gcs cache: downloading", "file", name, "bucket", a.dataBucket)

	if err := os.MkdirAll(a.storageDir, 0o755); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	obj := client.Bucket(a.dataBucket).Object(gcsFilesPrefix + name)
	r, err := obj.NewReader(dlCtx)
	if err != nil {
		return fmt.Errorf("gcs cache: open gs://%s/%s%s: %w", a.dataBucket, gcsFilesPrefix, name, err)
	}
	defer func() { _ = r.Close() }()

	tmp, err := os.CreateTemp(a.storageDir, "gcs-dl-*")
	if err != nil {
		return fmt.Errorf("gcs cache: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("gcs cache: download gs://%s/%s%s: %w", a.dataBucket, gcsFilesPrefix, name, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("gcs cache: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("gcs cache: close temp: %w", err)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("gcs cache: rename to %s: %w", localPath, err)
	}

	a.log.Info("gcs cache: downloaded", "file", name, "bucket", a.dataBucket)
	return nil
}
