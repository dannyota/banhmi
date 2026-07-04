// Package sagebatch embeds many texts in a single AWS SageMaker Processing Job
// and returns the vectors. Like kagglebatch it is a bulk-only embedder for
// offline backfill of the whole corpus, NOT the synchronous embed.Embedder (which
// serves one query at a time).
//
// Flow: upload input JSONL + the embed script to S3, create a SageMaker
// Processing Job with a PyTorch GPU container that loads BGE-M3 and encodes all
// texts, poll until complete, download the output JSONL from S3, and stream
// vectors to the caller via the onVector callback.
package sagebatch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sagemaker"
	smtypes "github.com/aws/aws-sdk-go-v2/service/sagemaker/types"
)

//go:embed embed_script.py
var embedScript string

const (
	// inputFileName is the JSONL file uploaded to S3 for the processing job.
	inputFileName = "input.jsonl.gz"
	// scriptFileName is the Python script uploaded to S3 and run by the
	// processing container.
	scriptFileName = "embed.py"
	// outputFileName is the vectors file the processing script writes.
	outputFileName = "vectors.jsonl.gz"

	// Default container image: AWS Deep Learning Container for PyTorch inference
	// on GPU. The registry ID 763104351884 is the official AWS DLC registry for
	// ap-southeast-1.
	defaultContainerImage = "763104351884.dkr.ecr.ap-southeast-1.amazonaws.com/pytorch-training:2.1.0-gpu-py310-cu118-ubuntu20.04-sagemaker"

	// pollInterval is the gap between job-status polls.
	pollInterval = 15 * time.Second
	// jobTimeout bounds the total wait for a processing job.
	jobTimeout = 90 * time.Minute
)

// Options configures a BatchEmbedder.
type Options struct {
	// Bucket is the S3 bucket for input/output data.
	Bucket string
	// RoleARN is the SageMaker execution role ARN (must have S3/ECR/CloudWatch
	// access).
	RoleARN string
	// Region is the AWS region (e.g. "ap-southeast-1").
	Region string
	// InstanceType is the SageMaker instance type (e.g. "ml.g4dn.xlarge").
	InstanceType string
	// ContainerImage overrides the default PyTorch DLC image.
	ContainerImage string
	// Dims is the expected vector dimension (1024 for BGE-M3); validated on
	// return.
	Dims int
	// KeepArtifacts, when true, leaves S3 input/output data after the job; by
	// default only the output is kept (for debugging).
	KeepArtifacts bool
}

// BatchEmbedder embeds texts in a single SageMaker Processing Job.
type BatchEmbedder struct {
	opts Options
	log  *slog.Logger
	sm   *sagemaker.Client
	s3c  *s3.Client

	// Overridable in tests.
	pollInterval time.Duration
	jobTimeout   time.Duration
}

// New returns a BatchEmbedder. AWS credentials come from the standard SDK chain
// (env vars, shared config, IAM role).
func New(opts Options, log *slog.Logger) (*BatchEmbedder, error) {
	if err := validateOpts(opts); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	cfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(opts.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &BatchEmbedder{
		opts:         opts,
		log:          log,
		sm:           sagemaker.NewFromConfig(cfg),
		s3c:          s3.NewFromConfig(cfg),
		pollInterval: pollInterval,
		jobTimeout:   jobTimeout,
	}, nil
}

func validateOpts(opts Options) error {
	if opts.Bucket == "" {
		return errors.New("sagebatch: Bucket is required")
	}
	if opts.RoleARN == "" {
		return errors.New("sagebatch: RoleARN is required")
	}
	if opts.Region == "" {
		return errors.New("sagebatch: Region is required")
	}
	if opts.Dims <= 0 {
		return errors.New("sagebatch: Dims must be positive")
	}
	return nil
}

// inputRow is one line of the input JSONL.
type inputRow struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

// vectorRow is one line of the output JSONL.
type vectorRow struct {
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

// InputWriter streams embed input rows one at a time, mirroring
// kagglebatch.InputWriter so the caller never holds all input texts in memory.
type InputWriter struct {
	buf   *bytes.Buffer
	gz    *gzip.Writer
	enc   *json.Encoder
	count int
}

func newInputWriter() *InputWriter {
	buf := &bytes.Buffer{}
	gz := gzip.NewWriter(buf)
	return &InputWriter{
		buf: buf,
		gz:  gz,
		enc: json.NewEncoder(gz),
	}
}

// Write appends one text as the next input row.
func (w *InputWriter) Write(text string) error {
	if err := w.enc.Encode(inputRow{Index: w.count, Text: text}); err != nil {
		return fmt.Errorf("encode input row %d: %w", w.count, err)
	}
	w.count++
	return nil
}

func (w *InputWriter) close() error {
	return w.gz.Close()
}

// EmbedStream embeds an arbitrary number of texts in a single SageMaker
// Processing Job with bounded memory. write fills the input rows; onVector is
// invoked once per returned vector, keyed by the input index. It returns the
// number of texts embedded; 0 (write produced no rows) is a no-op.
func (b *BatchEmbedder) EmbedStream(ctx context.Context, write func(w *InputWriter) error, onVector func(index int, vec []float32) error) (int, error) {
	iw := newInputWriter()
	if err := write(iw); err != nil {
		return 0, fmt.Errorf("write embed input: %w", err)
	}
	if err := iw.close(); err != nil {
		return 0, fmt.Errorf("close gzip writer: %w", err)
	}
	n := iw.count
	if n == 0 {
		return 0, nil
	}

	runID := fmt.Sprintf("banhmi-embed-%d", time.Now().UTC().UnixNano())
	inputKey := fmt.Sprintf("input/%s/%s", runID, inputFileName)
	scriptKey := fmt.Sprintf("input/%s/%s", runID, scriptFileName)
	outputPrefix := fmt.Sprintf("output/%s", runID)

	// Upload input JSONL to S3.
	b.log.Info("uploading input to S3", "bucket", b.opts.Bucket, "key", inputKey, "rows", n)
	if _, err := b.s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket: &b.opts.Bucket,
		Key:    &inputKey,
		Body:   bytes.NewReader(iw.buf.Bytes()),
	}); err != nil {
		return 0, fmt.Errorf("upload input to S3: %w", err)
	}

	// Upload the embed script to S3.
	if _, err := b.s3c.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      &b.opts.Bucket,
		Key:         &scriptKey,
		Body:        strings.NewReader(embedScript),
		ContentType: aws.String("text/x-python"),
	}); err != nil {
		return 0, fmt.Errorf("upload script to S3: %w", err)
	}

	// Create the SageMaker Processing Job.
	containerImage := b.opts.ContainerImage
	if containerImage == "" {
		containerImage = defaultContainerImage
	}
	instanceType := smtypes.ProcessingInstanceType(b.opts.InstanceType)
	if b.opts.InstanceType == "" {
		instanceType = smtypes.ProcessingInstanceTypeMlG4dnXlarge
	}

	inputS3URI := fmt.Sprintf("s3://%s/input/%s/", b.opts.Bucket, runID)
	outputS3URI := fmt.Sprintf("s3://%s/%s/", b.opts.Bucket, outputPrefix)
	jobName := runID

	b.log.Info("creating SageMaker processing job", "job", jobName,
		"instance", instanceType, "input_s3", inputS3URI, "output_s3", outputS3URI)

	_, err := b.sm.CreateProcessingJob(ctx, &sagemaker.CreateProcessingJobInput{
		ProcessingJobName: &jobName,
		RoleArn:           &b.opts.RoleARN,
		AppSpecification: &smtypes.AppSpecification{
			ImageUri:            &containerImage,
			ContainerEntrypoint: []string{"python3", "/opt/ml/processing/input/embed.py"},
			ContainerArguments:  nil,
		},
		ProcessingResources: &smtypes.ProcessingResources{
			ClusterConfig: &smtypes.ProcessingClusterConfig{
				InstanceCount:  aws.Int32(1),
				InstanceType:   instanceType,
				VolumeSizeInGB: aws.Int32(30),
			},
		},
		ProcessingInputs: []smtypes.ProcessingInput{
			{
				InputName: aws.String("input"),
				S3Input: &smtypes.ProcessingS3Input{
					S3Uri:             &inputS3URI,
					LocalPath:         aws.String("/opt/ml/processing/input"),
					S3DataType:        smtypes.ProcessingS3DataTypeS3Prefix,
					S3InputMode:       smtypes.ProcessingS3InputModeFile,
					S3CompressionType: smtypes.ProcessingS3CompressionTypeNone,
				},
			},
		},
		ProcessingOutputConfig: &smtypes.ProcessingOutputConfig{
			Outputs: []smtypes.ProcessingOutput{
				{
					OutputName: aws.String("output"),
					S3Output: &smtypes.ProcessingS3Output{
						S3Uri:        &outputS3URI,
						LocalPath:    aws.String("/opt/ml/processing/output"),
						S3UploadMode: smtypes.ProcessingS3UploadModeEndOfJob,
					},
				},
			},
		},
		StoppingCondition: &smtypes.ProcessingStoppingCondition{
			MaxRuntimeInSeconds: aws.Int32(3600), // 1h hard limit on the job itself
		},
	})
	if err != nil {
		return 0, fmt.Errorf("create processing job: %w", err)
	}

	// Poll job status.
	if err := b.waitJob(ctx, jobName); err != nil {
		return 0, err
	}

	// Download and parse output vectors from S3.
	outputKey := fmt.Sprintf("%s/%s", outputPrefix, outputFileName)
	b.log.Info("downloading output from S3", "bucket", b.opts.Bucket, "key", outputKey)

	getOut, err := b.s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &b.opts.Bucket,
		Key:    &outputKey,
	})
	if err != nil {
		return 0, fmt.Errorf("download output from S3: %w", err)
	}
	defer func() { _ = getOut.Body.Close() }()

	if err := streamParseVectors(getOut.Body, n, b.opts.Dims, onVector); err != nil {
		return 0, err
	}

	b.log.Info("sagemaker embed complete", "job", jobName, "vectors", n)
	return n, nil
}

// EmbedAll is a convenience wrapper over EmbedStream that materializes all
// input and output in memory.
func (b *BatchEmbedder) EmbedAll(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, len(texts))
	n, err := b.EmbedStream(ctx,
		func(w *InputWriter) error {
			for _, t := range texts {
				if err := w.Write(t); err != nil {
					return err
				}
			}
			return nil
		},
		func(index int, vec []float32) error {
			out[index] = vec
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	if n != len(texts) {
		return nil, fmt.Errorf("sagemaker returned %d vectors for %d texts", n, len(texts))
	}
	return out, nil
}

// waitJob polls the processing job status until it completes, fails, or the
// context/timeout expires.
func (b *BatchEmbedder) waitJob(ctx context.Context, jobName string) error {
	ctx, cancel := context.WithTimeout(ctx, b.jobTimeout)
	defer cancel()

	for {
		resp, err := b.sm.DescribeProcessingJob(ctx, &sagemaker.DescribeProcessingJobInput{
			ProcessingJobName: &jobName,
		})
		if err != nil {
			return fmt.Errorf("describe processing job %s: %w", jobName, err)
		}

		switch resp.ProcessingJobStatus {
		case smtypes.ProcessingJobStatusCompleted:
			b.log.Info("processing job complete", "job", jobName)
			return nil
		case smtypes.ProcessingJobStatusFailed:
			reason := ""
			if resp.FailureReason != nil {
				reason = *resp.FailureReason
			}
			return fmt.Errorf("processing job %s failed: %s", jobName, reason)
		case smtypes.ProcessingJobStatusStopped:
			return fmt.Errorf("processing job %s was stopped", jobName)
		case smtypes.ProcessingJobStatusStopping:
			return fmt.Errorf("processing job %s is stopping", jobName)
		}

		b.log.Debug("processing job running", "job", jobName, "status", resp.ProcessingJobStatus)
		if err := sleep(ctx, b.pollInterval); err != nil {
			return err
		}
	}
}

// streamParseVectors parses the output JSONL from an io.Reader (possibly gzip),
// validating that every index in [0,n) appears exactly once and each vector has
// exactly dims components. It invokes onVector for each in arrival order.
func streamParseVectors(r io.Reader, n, dims int, onVector func(index int, vec []float32) error) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer func() { _ = gz.Close() }()

	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	seen := make([]bool, n)
	count := 0
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var row vectorRow
		if err := json.Unmarshal(line, &row); err != nil {
			return fmt.Errorf("parse vectors line %d: %w", lineNo, err)
		}
		if row.Index < 0 || row.Index >= n {
			return fmt.Errorf("vector index %d out of range [0,%d)", row.Index, n)
		}
		if seen[row.Index] {
			return fmt.Errorf("duplicate vector for index %d", row.Index)
		}
		if len(row.Embedding) != dims {
			return fmt.Errorf("vector %d has %d dims, want %d", row.Index, len(row.Embedding), dims)
		}
		seen[row.Index] = true
		count++
		if err := onVector(row.Index, row.Embedding); err != nil {
			return fmt.Errorf("handle vector %d: %w", row.Index, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("scan vectors: %w", err)
	}
	if count != n {
		for i, ok := range seen {
			if !ok {
				return fmt.Errorf("missing vector for index %d (%d of %d returned)", i, count, n)
			}
		}
		return fmt.Errorf("sagemaker returned %d vectors for %d inputs", count, n)
	}
	return nil
}

// sleep waits for d or until ctx is done, returning ctx.Err() if cancelled.
func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
