package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Challenge tokens for directory domain verification (ChatGPT apps) live in a
// PRIVATE S3 prefix — s3://{challengeBucket}/{challengePrefix}/{product name} —
// so registering another jurisdiction is an S3 upload, not a redeploy. The
// token itself is not a secret: this handler's whole job is to serve it
// publicly at /.well-known/openai-apps-challenge.
const (
	challengeBucket = "danny-banhmi-public"
	challengePrefix = "challenge"
	challengeRegion = "ap-southeast-1"
)

// fetchChallengeToken reads the token for one product name. Package variable so
// tests stub it; the default reads S3 with a lazily built client.
var fetchChallengeToken = fetchChallengeTokenS3

var (
	challengeClientOnce sync.Once
	challengeClient     *s3.Client
	challengeClientErr  error
)

func fetchChallengeTokenS3(ctx context.Context, name string) (string, error) {
	challengeClientOnce.Do(func() {
		cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(challengeRegion))
		if err != nil {
			challengeClientErr = fmt.Errorf("challenge: load AWS config: %w", err)
			return
		}
		challengeClient = s3.NewFromConfig(cfg)
	})
	if challengeClientErr != nil {
		return "", challengeClientErr
	}
	bucket, key := challengeBucket, challengePrefix+"/"+name
	out, err := challengeClient.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: &key})
	if err != nil {
		return "", fmt.Errorf("challenge: get s3://%s/%s: %w", bucket, key, err)
	}
	defer func() { _ = out.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(out.Body, 4096))
	if err != nil {
		return "", fmt.Errorf("challenge: read s3://%s/%s: %w", bucket, key, err)
	}
	return strings.TrimSpace(string(b)), nil
}

// challengeHandler serves the verification token, reading the S3 file on every
// request — an upload is live on the next poll, no cache, no redeploy. The
// path only sees directory-verification traffic, so a GET per request is
// negligible; Cache-Control: no-store keeps CloudFront from pinning a stale
// token or 404.
func challengeHandler(d landingData, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token, err := fetchChallengeToken(r.Context(), d.Name)
		if err != nil {
			log.Debug("challenge token unavailable", "jurisdiction", d.Code, "err", err)
		}
		w.Header().Set("Cache-Control", "no-store")
		if token == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(token))
	}
}
