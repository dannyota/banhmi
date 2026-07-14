package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"danny.vn/banhmi/pkg/extract/docai"
)

const ocrCachePrefix = "ocr/"

// s3OCRCache implements docai.Cache backed by an S3 bucket.
// Objects are stored at key ocr/{sha256}.txt in the same per-jurisdiction
// bucket the file store uses.
type s3OCRCache struct {
	client *s3.Client
	bucket string
}

// Compile-time check.
var _ docai.Cache = (*s3OCRCache)(nil)

// newS3OCRCache creates an S3-backed OCR text cache.
func newS3OCRCache(ctx context.Context, bucket string) (*s3OCRCache, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("s3 ocr cache: load AWS config: %w", err)
	}
	return &s3OCRCache{
		client: s3.NewFromConfig(cfg),
		bucket: bucket,
	}, nil
}

// Get retrieves cached OCR text for a document. Returns ("", false, nil) on a
// cache miss (NoSuchKey).
func (c *s3OCRCache) Get(ctx context.Context, sha256 string) (string, bool, error) {
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
		return "", false, fmt.Errorf("s3 ocr cache get %s: %w", key, err)
	}
	defer func() { _ = result.Body.Close() }()

	data, err := io.ReadAll(result.Body)
	if err != nil {
		return "", false, fmt.Errorf("s3 ocr cache read %s: %w", key, err)
	}
	return string(data), true, nil
}

// Put stores OCR text for a document.
func (c *s3OCRCache) Put(ctx context.Context, sha256, text string) error {
	key := ocrCachePrefix + sha256 + ".txt"
	_, err := c.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(c.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader([]byte(text)),
		ContentType: aws.String("text/plain; charset=utf-8"),
	})
	if err != nil {
		return fmt.Errorf("s3 ocr cache put %s: %w", key, err)
	}
	return nil
}

// BuildOCRCache returns an S3-backed docai.Cache when s3Bucket is non-empty,
// or nil otherwise. Exported so the composition root can build it from config.
func BuildOCRCache(ctx context.Context, s3Bucket string, log *slog.Logger) (docai.Cache, error) {
	if s3Bucket == "" {
		log.Info("ocr cache: disabled (no S3 bucket)")
		return nil, nil
	}
	cache, err := newS3OCRCache(ctx, s3Bucket)
	if err != nil {
		return nil, err
	}
	log.Info("ocr cache: S3 enabled", "bucket", s3Bucket)
	return cache, nil
}
