package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	pgvector "github.com/pgvector/pgvector-go"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed"
	"danny.vn/banhmi/pkg/rag/embed/gcsbatch"
	"danny.vn/banhmi/pkg/rag/embed/kagglebatch"
	"danny.vn/banhmi/pkg/rag/embed/sagebatch"
	dbgold "danny.vn/banhmi/pkg/store/gold"
)

// embedHeartbeat is how often the EmbedAll activity heartbeats while it waits on
// the (minutes-long) Kaggle GPU job, so Temporal sees it as alive.
const embedHeartbeat = 30 * time.Second

// EmbedAllParams configures a whole-corpus embedding pass. Owner/ModelDataset/
// Accelerator are Kaggle-specific; SageMaker* fields configure the SageMaker
// engine; GCSBatch* fields configure the Cloud Run Job + GCS engine. Engine
// selects the batch backend ("kaggle", "sagemaker", "gcsbatch", or "local").
// Force re-embeds every chunk (overwrite); otherwise only chunks missing the
// canonical embedding are embedded. Limit caps the count (0 = all). The KGAT
// token comes from KAGGLE_API_TOKEN in the worker's env.
type EmbedAllParams struct {
	// Engine selects the batch backend ("kaggle", "sagemaker", "gcsbatch", or "local").
	Engine string
	// Kaggle-specific fields.
	Owner        string
	ModelDataset string
	Accelerator  string
	// SageMaker-specific fields.
	SageMakerBucket         string
	SageMakerRoleARN        string
	SageMakerRegion         string
	SageMakerInstanceType   string
	SageMakerContainerImage string
	// GCS batch fields (used when Engine == "gcsbatch").
	GCSBatchBucket      string // GCS bucket for input/output JSONL
	GCSBatchCloudRunJob string // Cloud Run Job name
	GCSBatchRegion      string // GCP region
	GCSBatchProject     string // GCP project ID
	// Common fields.
	Dims  int
	Force bool
	Limit int
}

// EmbedAllResult reports how many chunk embeddings were written.
type EmbedAllResult struct {
	Embedded int
}

// EmbedAll loads the target chunks, embeds them in a single batch job (Kaggle or
// SageMaker, selected by p.Engine), and upserts the vectors under the canonical
// model tag (config.EmbedModel) so retrieval — which filters by that tag — finds
// them. It heartbeats while the job runs. This is the batch path for
// IndexAll-scale embedding; the local OVMS embedder remains the serve-time/query
// path.
func (a *Activities) EmbedAll(ctx context.Context, p EmbedAllParams) (EmbedAllResult, error) {
	log := a.log

	dims := p.Dims
	if dims <= 0 {
		dims = config.EmbedDims
	}
	model := config.EmbedModel
	dims32 := int32(dims) //nolint:gosec // dims is the fixed BGE-M3 width (1024).

	engine := p.Engine
	if engine == "" {
		engine = "kaggle"
	}

	// Build a generic embed function that takes the same write + onVector
	// callbacks regardless of the backend.
	type writerFn func(text string) error
	type embedFn func(ctx context.Context, write func(fn writerFn) error, onVector func(index int, vec []float32) error) (int, error)

	var runEmbed embedFn

	switch engine {
	case "kaggle":
		be, err := kagglebatch.New(kagglebatch.Options{
			Owner:        p.Owner,
			ModelDataset: p.ModelDataset,
			Accelerator:  p.Accelerator,
			Dims:         dims,
			Token:        a.kaggleToken,
		}, nil)
		if err != nil {
			return EmbedAllResult{}, fmt.Errorf("kaggle embedder: %w", err)
		}
		log.Info("embed-all: embedding on Kaggle (streaming)", "engine", engine,
			"owner", p.Owner, "accelerator", p.Accelerator, "force", p.Force)
		runEmbed = func(ctx context.Context, write func(fn writerFn) error, onVector func(index int, vec []float32) error) (int, error) {
			return be.EmbedStream(ctx,
				func(w *kagglebatch.InputWriter) error {
					return write(w.Write)
				},
				onVector,
			)
		}

	case "sagemaker":
		be, err := sagebatch.New(sagebatch.Options{
			Bucket:         p.SageMakerBucket,
			RoleARN:        p.SageMakerRoleARN,
			Region:         p.SageMakerRegion,
			InstanceType:   p.SageMakerInstanceType,
			ContainerImage: p.SageMakerContainerImage,
			Dims:           dims,
		}, nil)
		if err != nil {
			return EmbedAllResult{}, fmt.Errorf("sagemaker embedder: %w", err)
		}
		log.Info("embed-all: embedding on SageMaker (streaming)", "engine", engine,
			"bucket", p.SageMakerBucket, "instance", p.SageMakerInstanceType, "force", p.Force)
		runEmbed = func(ctx context.Context, write func(fn writerFn) error, onVector func(index int, vec []float32) error) (int, error) {
			return be.EmbedStream(ctx,
				func(w *sagebatch.InputWriter) error {
					return write(w.Write)
				},
				onVector,
			)
		}

	case "gcsbatch":
		be, err := gcsbatch.New(gcsbatch.Options{
			Bucket:      p.GCSBatchBucket,
			CloudRunJob: p.GCSBatchCloudRunJob,
			Region:      p.GCSBatchRegion,
			Project:     p.GCSBatchProject,
			Dims:        dims,
		}, nil)
		if err != nil {
			return EmbedAllResult{}, fmt.Errorf("gcsbatch embedder: %w", err)
		}
		defer func() { _ = be.Close() }()
		log.Info("embed-all: embedding via Cloud Run Job + GCS", "engine", engine,
			"bucket", p.GCSBatchBucket, "job", p.GCSBatchCloudRunJob, "force", p.Force)
		runEmbed = func(ctx context.Context, write func(fn writerFn) error, onVector func(index int, vec []float32) error) (int, error) {
			return be.EmbedStream(ctx,
				func(w *gcsbatch.InputWriter) error {
					return write(w.Write)
				},
				onVector,
			)
		}

	case "cloudrun":
		url := os.Getenv("BANHMI_EMBEDDER_URL")
		if url == "" {
			return EmbedAllResult{}, fmt.Errorf("cloudrun engine requires BANHMI_EMBEDDER_URL")
		}
		embedder, eerr := embed.NewCloudRun(ctx, url, model, dims)
		if eerr != nil {
			return EmbedAllResult{}, fmt.Errorf("cloudrun embedder: %w", eerr)
		}
		log.Info("embed-all: embedding via Cloud Run", "engine", engine, "url", url, "force", p.Force)
		runEmbed = func(ctx context.Context, write func(fn writerFn) error, onVector func(index int, vec []float32) error) (int, error) {
			return embedCloudRunBatch(ctx, embedder, func(fn func(string) error) error {
				return write(fn)
			}, onVector)
		}

	default:
		return EmbedAllResult{}, fmt.Errorf("unsupported embed engine %q", engine)
	}

	// Run the batch job while heartbeating. Memory stays bounded: input chunks
	// stream from the DB
	// straight to the upload file, and vectors are upserted one at a time as they
	// arrive — only the index->id mapping (ids) is retained. The job respects ctx
	// cancellation. ids/written are only touched by this goroutine and read by the
	// caller after <-done, so there is no race.
	var ids []int64
	written := 0
	done := make(chan error, 1)
	go func() {
		_, err := runEmbed(ctx,
			func(writeFn writerFn) error {
				return a.streamChunksForEmbed(ctx, p.Force, model, dims, p.Limit, func(id int64, text string) error {
					ids = append(ids, id)
					return writeFn(text)
				})
			},
			func(index int, vec []float32) error {
				if index < 0 || index >= len(ids) {
					return fmt.Errorf("vector index %d out of range [0,%d)", index, len(ids))
				}
				if len(vec) != dims {
					return fmt.Errorf("chunk %d vector dims = %d, want %d", ids[index], len(vec), dims)
				}
				if _, err := a.gold.UpsertChunkEmbedding(ctx, dbgold.UpsertChunkEmbeddingParams{
					ChunkID:   ids[index],
					Model:     model,
					Dims:      dims32,
					Embedding: pgvector.NewVector(vec),
				}); err != nil {
					return fmt.Errorf("upsert embedding chunk %d: %w", ids[index], err)
				}
				written++
				return nil
			},
		)
		done <- err
	}()

	ticker := time.NewTicker(embedHeartbeat)
	defer ticker.Stop()
	for waiting := true; waiting; {
		select {
		case <-ctx.Done():
			return EmbedAllResult{}, ctx.Err()
		case <-ticker.C:
			a.log.Info(fmt.Sprintf("embedding on %s", engine))
		case err := <-done:
			if err != nil {
				return EmbedAllResult{}, fmt.Errorf("%s embed: %w", engine, err)
			}
			waiting = false
		}
	}

	if written == 0 {
		log.Info("embed-all: no chunks to embed")
	} else {
		log.Info("embed-all: complete", "embedded", written, "engine", engine)
	}
	return EmbedAllResult{Embedded: written}, nil
}

// streamChunksForEmbed streams the chunks to embed to fn in id order: every chunk
// when force is set, otherwise only those missing the (model, dims) embedding. The
// text passed to fn is the contextual prefix joined to the content (matching the
// Index embedding text). Streaming a cursor keeps the whole corpus out of memory;
// the cursor is held only for the duration of fn's calls (the fast write phase),
// not the minutes-long Kaggle job.
func (a *Activities) streamChunksForEmbed(ctx context.Context, force bool, model string, dims, limit int, fn func(id int64, text string) error) error {
	if limit <= 0 {
		limit = 1<<31 - 1
	}
	var (
		sql  string
		args []any
	)
	if force {
		sql = `
SELECT c.id, COALESCE(c.context_prefix, ''), c.content
FROM gold.chunk c
ORDER BY c.id
LIMIT $1`
		args = []any{limit}
	} else {
		sql = `
SELECT c.id, COALESCE(c.context_prefix, ''), c.content
FROM gold.chunk c
LEFT JOIN gold.chunk_embedding e
  ON e.chunk_id = c.id AND e.model = $1 AND e.dims = $2
WHERE e.id IS NULL
ORDER BY c.id
LIMIT $3`
		args = []any{model, dims, limit}
	}

	rows, err := a.dbpool.Query(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("load chunks to embed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var prefix, content string
		if err := rows.Scan(&id, &prefix, &content); err != nil {
			return fmt.Errorf("scan chunk: %w", err)
		}
		text := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(prefix), strings.TrimSpace(content)}, "\n"))
		if text == "" {
			return fmt.Errorf("chunk %d has empty embedding text", id)
		}
		if err := fn(id, text); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate chunks: %w", err)
	}
	return nil
}

const cloudRunBatch = 2048

// embedCloudRunBatch streams texts through the write callback, buffering up to
// cloudRunBatch texts before calling embedder.Embed + onVector. This keeps
// memory bounded to one batch at a time. On batch embed failure (after the
// embedder's own retries are exhausted), the batch is logged and skipped so the
// rest of the corpus is still embedded; a summary error is returned at the end.
func embedCloudRunBatch(ctx context.Context, embedder embed.Embedder, write func(fn func(string) error) error, onVector func(index int, vec []float32) error) (int, error) {
	var (
		buf         = make([]string, 0, cloudRunBatch)
		globalIdx   int // running index across all texts, for onVector
		total       int
		failedBatch int
		failedTexts int
	)

	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		batchStart := globalIdx - len(buf)
		slog.Debug("cloudrun embed: sending batch", "start", batchStart, "end", globalIdx, "size", len(buf), "embedded_so_far", total)
		vecs, err := embedder.Embed(ctx, buf)
		if err != nil {
			slog.Warn("cloudrun embed: batch failed, skipping",
				"batch_start", batchStart, "batch_end", globalIdx, "err", err)
			failedBatch++
			failedTexts += len(buf)
			buf = buf[:0]
			return nil
		}
		for j, vec := range vecs {
			if err := onVector(batchStart+j, vec); err != nil {
				return err
			}
			total++
		}
		buf = buf[:0]
		return nil
	}

	if err := write(func(text string) error {
		buf = append(buf, text)
		globalIdx++
		if len(buf) >= cloudRunBatch {
			return flush()
		}
		return nil
	}); err != nil {
		return total, err
	}

	// Flush remaining partial batch.
	if err := flush(); err != nil {
		return total, err
	}

	if failedBatch > 0 {
		return total, fmt.Errorf("embedded %d, %d batches failed (%d texts)", total, failedBatch, failedTexts)
	}
	return total, nil
}
