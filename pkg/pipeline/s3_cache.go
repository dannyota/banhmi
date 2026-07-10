package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const filesPrefix = "files/"

// FileStore is a best-effort blob cache for fetched files. Put uploads a local
// file to the store (errors are logged and never propagated — a store failure
// must not block a fetch). Get downloads a file only when it is missing locally;
// if it is already present on disk, it returns immediately.
type FileStore interface {
	// Put uploads the named file (under storageDir) to the remote store.
	// Errors are best-effort: logged as warnings, never propagated.
	Put(ctx context.Context, name string) error
	// Get ensures the named file exists under storageDir. If it is already
	// present locally, it returns nil immediately. Otherwise it downloads
	// from the remote store.
	Get(ctx context.Context, name string) error
}

// validFileName rejects names containing path separators or traversal patterns.
func validFileName(name string) bool {
	return name != "" &&
		!strings.Contains(name, "/") &&
		!strings.Contains(name, "\\") &&
		!strings.Contains(name, "..")
}

// ensureLocalFile guarantees that name exists under storageDir. If it is
// already present locally, it returns immediately. Otherwise it downloads from
// the remote file store. When no remote store is configured and the file is
// missing locally, it returns an error.
func (a *Activities) ensureLocalFile(ctx context.Context, name string) error {
	localPath := filepath.Join(a.storageDir, name)
	if _, err := os.Stat(localPath); err == nil {
		return nil
	}
	if a.files == nil {
		return fmt.Errorf("file %s not found locally and no remote file store configured", name)
	}
	return a.files.Get(ctx, name)
}

// BuildFileStore returns an S3-backed FileStore when s3Bucket is non-empty, or
// nil (no remote cache) otherwise. Exported so the composition root (pkg/app)
// can build it from config without importing internal details.
func BuildFileStore(ctx context.Context, s3Bucket, storageDir string, log *slog.Logger) (FileStore, error) {
	if s3Bucket == "" {
		return nil, nil
	}
	store, err := newS3Store(ctx, s3Bucket, storageDir)
	if err != nil {
		return nil, err
	}
	log.Info("file cache: S3 enabled", "bucket", s3Bucket)
	return store, nil
}

// s3Store implements FileStore backed by an S3 bucket. Key layout: files/{name}.
type s3Store struct {
	client     *s3.Client
	bucket     string
	storageDir string
}

// newS3Store creates an S3-backed file store. Region and credentials come from
// the standard AWS SDK chain (env vars, IMDS, shared config).
func newS3Store(ctx context.Context, bucket, storageDir string) (*s3Store, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("s3 cache: load AWS config: %w", err)
	}
	return &s3Store{
		client:     s3.NewFromConfig(cfg),
		bucket:     bucket,
		storageDir: storageDir,
	}, nil
}

// Put uploads the named file to S3 as s3://{bucket}/files/{name}.
func (s *s3Store) Put(ctx context.Context, name string) error {
	if !validFileName(name) {
		return fmt.Errorf("s3 cache: invalid file name %q", name)
	}

	localPath := filepath.Join(s.storageDir, name)
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("s3 cache: open local file %s: %w", name, err)
	}
	defer func() { _ = f.Close() }()

	uploadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	key := filesPrefix + name
	_, err = s.client.PutObject(uploadCtx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   f,
	})
	if err != nil {
		return fmt.Errorf("s3 cache: upload s3://%s/%s: %w", s.bucket, key, err)
	}
	return nil
}

// Get ensures the named file exists locally. If present, returns nil. Otherwise
// downloads from s3://{bucket}/files/{name}.
func (s *s3Store) Get(ctx context.Context, name string) error {
	localPath := filepath.Join(s.storageDir, name)
	if _, err := os.Stat(localPath); err == nil {
		return nil
	}

	if !validFileName(name) {
		return fmt.Errorf("invalid file name %q", name)
	}

	if err := os.MkdirAll(s.storageDir, 0o755); err != nil {
		return fmt.Errorf("create storage dir: %w", err)
	}

	dlCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	key := filesPrefix + name
	result, err := s.client.GetObject(dlCtx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("s3 cache: download s3://%s/%s: %w", s.bucket, key, err)
	}
	defer func() { _ = result.Body.Close() }()

	tmp, err := os.CreateTemp(s.storageDir, "s3-dl-*")
	if err != nil {
		return fmt.Errorf("s3 cache: create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := io.Copy(tmp, result.Body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("s3 cache: download s3://%s/%s: %w", s.bucket, key, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("s3 cache: chmod temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("s3 cache: close temp: %w", err)
	}
	if err := os.Rename(tmpName, localPath); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("s3 cache: rename to %s: %w", localPath, err)
	}
	return nil
}
