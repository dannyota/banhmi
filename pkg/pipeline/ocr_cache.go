package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"danny.vn/banhmi/pkg/extract/docai"
)

const ocrCachePrefix = "ocr/"

// ocrCache implements docai.Cache with the same shape as the fetched-file
// cache: the primary copy is a local text file at {storageDir}/ocr/{sha256}.txt
// and an S3 object at ocr/{sha256}.txt in the per-jurisdiction data bucket is
// a best-effort durable mirror. Get touches S3 only when the local file is
// missing (and writes it back to disk on a hit); Put never fails the OCR run
// on a mirror error. The local file survives DB rebuilds and works without
// AWS credentials; the mirror survives the laptop.
type ocrCache struct {
	storageDir string
	client     *s3.Client // nil = local-only
	bucket     string
	log        *slog.Logger
}

// Compile-time check.
var _ docai.Cache = (*ocrCache)(nil)

func (c *ocrCache) localPath(sha256 string) string {
	return filepath.Join(c.storageDir, "ocr", sha256+".txt")
}

// Get returns cached OCR text: local file first, then the S3 mirror. A mirror
// hit is written back to the local file so later runs stay offline.
func (c *ocrCache) Get(ctx context.Context, sha256 string) (string, bool, error) {
	local := c.localPath(sha256)
	data, err := os.ReadFile(local)
	if err == nil {
		return string(data), true, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("ocr cache read %s: %w", local, err)
	}
	if c.client == nil {
		return "", false, nil
	}

	key := ocrCachePrefix + sha256 + ".txt"
	result, err := c.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var nsk *s3types.NoSuchKey
		if errors.As(err, &nsk) {
			return "", false, nil
		}
		// aws-sdk-go-v2 may also surface NoSuchKey via a ResponseError with
		// code "NoSuchKey" without wrapping the typed error.
		if strings.Contains(err.Error(), "NoSuchKey") {
			return "", false, nil
		}
		return "", false, fmt.Errorf("ocr cache get s3 %s: %w", key, err)
	}
	defer func() { _ = result.Body.Close() }()

	data, err = io.ReadAll(result.Body)
	if err != nil {
		return "", false, fmt.Errorf("ocr cache read s3 %s: %w", key, err)
	}
	if err := c.writeLocal(local, data); err != nil {
		c.log.Warn("ocr cache: local write-back failed", "path", local, "err", err)
	}
	return string(data), true, nil
}

// Put stores OCR text locally and mirrors it to S3 best-effort.
func (c *ocrCache) Put(ctx context.Context, sha256, text string) error {
	local := c.localPath(sha256)
	if err := c.writeLocal(local, []byte(text)); err != nil {
		return fmt.Errorf("ocr cache write %s: %w", local, err)
	}
	if c.client == nil {
		return nil
	}
	key := ocrCachePrefix + sha256 + ".txt"
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(text)),
		ContentType: aws.String("text/plain; charset=utf-8"),
	})
	if err != nil {
		c.log.Warn("ocr cache: s3 mirror failed", "key", key, "err", err)
	}
	return nil
}

func (c *ocrCache) writeLocal(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// BuildOCRCache returns a docai.Cache that stores OCR text as local files
// under storageDir/ocr/, mirrored to the S3 bucket when one is configured.
// Returns nil (caching off) only when storageDir is empty and no bucket is
// set. Exported so the composition root can build it from config.
func BuildOCRCache(ctx context.Context, s3Bucket, storageDir string, log *slog.Logger) (docai.Cache, error) {
	if storageDir == "" && s3Bucket == "" {
		log.Info("ocr cache: disabled (no storage dir, no S3 bucket)")
		return nil, nil
	}
	c := &ocrCache{storageDir: storageDir, log: log}
	if s3Bucket != "" {
		cfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, fmt.Errorf("ocr cache: load AWS config: %w", err)
		}
		c.client = s3.NewFromConfig(cfg)
		c.bucket = s3Bucket
		log.Info("ocr cache: local files + S3 mirror", "dir", filepath.Join(storageDir, "ocr"), "bucket", s3Bucket)
	} else {
		log.Info("ocr cache: local files only", "dir", filepath.Join(storageDir, "ocr"))
	}
	return c, nil
}
