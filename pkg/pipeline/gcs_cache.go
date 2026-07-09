package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"cloud.google.com/go/storage"
)

const gcsFilesPrefix = "files/"

// uploadToGCS uploads a local file to GCS as a best-effort cache. Errors are
// logged as warnings and never propagated — a GCS failure must not block a
// fetch.
func (a *Activities) uploadToGCS(ctx context.Context, name string) {
	if a.dataBucket == "" {
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

	client, err := storage.NewClient(uploadCtx)
	if err != nil {
		a.log.Warn("gcs cache: create storage client", "err", err)
		return
	}
	defer func() { _ = client.Close() }()

	obj := client.Bucket(a.dataBucket).Object(gcsFilesPrefix + name)
	w := obj.NewWriter(uploadCtx)
	if _, err := io.Copy(w, f); err != nil {
		_ = w.Close()
		a.log.Warn("gcs cache: upload", "file", name, "bucket", a.dataBucket, "err", err)
		return
	}
	if err := w.Close(); err != nil {
		a.log.Warn("gcs cache: finalize upload", "file", name, "bucket", a.dataBucket, "err", err)
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
		return nil // already local
	}

	if a.dataBucket == "" {
		return fmt.Errorf("file %s not found locally and no GCS data bucket configured", name)
	}

	a.log.Info("gcs cache: downloading", "file", name, "bucket", a.dataBucket,
		slog.String("dst", localPath))

	if err := os.MkdirAll(a.storageDir, 0o755); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	client, err := storage.NewClient(dlCtx)
	if err != nil {
		return fmt.Errorf("gcs cache: create storage client: %w", err)
	}
	defer func() { _ = client.Close() }()

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
