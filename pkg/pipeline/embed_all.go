package pipeline

import (
	"context"
	"fmt"
	"strings"
	"time"

	pgvector "github.com/pgvector/pgvector-go"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed/kagglebatch"
	dbgold "danny.vn/banhmi/pkg/store/gold"
)

// embedHeartbeat is how often the EmbedAll activity heartbeats while it waits on
// the (minutes-long) Kaggle GPU job, so Temporal sees it as alive.
const embedHeartbeat = 30 * time.Second

// EmbedAllParams configures a whole-corpus embedding pass on Kaggle. Owner is the
// Kaggle account that owns the input dataset and embed kernel; ModelDataset
// optionally mounts a BGE-M3 mirror (empty pulls from HuggingFace); Accelerator
// is the Kaggle machine shape. Force re-embeds every chunk (overwrite); otherwise
// only chunks missing the canonical embedding are embedded. Limit caps the count
// (0 = all). The KGAT token comes from KAGGLE_API_TOKEN in the worker's env.
type EmbedAllParams struct {
	Owner        string
	ModelDataset string
	Accelerator  string
	Dims         int
	Force        bool
	Limit        int
}

// EmbedAllResult reports how many chunk embeddings were written.
type EmbedAllResult struct {
	Embedded int
}

// EmbedAllWorkflow embeds the whole corpus (or just the missing chunks) in one
// Kaggle GPU job. It runs the single EmbedAll activity on the EXTERNAL queue —
// the activity is I/O-bound (it waits minutes on Kaggle), so it must not occupy a
// local CPU slot. Query-time embedding is unaffected; this only fills the corpus.
func EmbedAllWorkflow(ctx workflow.Context, p EmbedAllParams) (EmbedAllResult, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           ExternalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 2 * time.Hour,
		HeartbeatTimeout:    10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    10 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    time.Minute,
			MaximumAttempts:    2,
		},
	})

	var a *Activities
	var res EmbedAllResult
	if err := workflow.ExecuteActivity(ctx, a.EmbedAll, p).Get(ctx, &res); err != nil {
		return EmbedAllResult{}, err
	}
	workflow.GetLogger(ctx).Info("embed-all workflow complete", "embedded", res.Embedded, "force", p.Force)
	return res, nil
}

// EmbedAll loads the target chunks, embeds them in a single Kaggle GPU batch via
// kagglebatch, and upserts the vectors under the canonical model tag
// (config.EmbedModel) so retrieval — which filters by that tag — finds them. It
// heartbeats while the kernel runs. This is the Kaggle path for IndexAll-scale
// embedding; the local OVMS embedder remains the serve-time/query path.
func (a *Activities) EmbedAll(ctx context.Context, p EmbedAllParams) (EmbedAllResult, error) {
	log := activity.GetLogger(ctx)

	dims := p.Dims
	if dims <= 0 {
		dims = config.EmbedDims
	}
	model := config.EmbedModel
	dims32 := int32(dims) //nolint:gosec // dims is the fixed BGE-M3 width (1024).

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
	log.Info("embed-all: embedding on Kaggle (streaming)", "owner", p.Owner,
		"accelerator", p.Accelerator, "force", p.Force)

	// Run the (minutes-long) Kaggle job while heartbeating so Temporal sees the
	// activity as alive. Memory stays bounded: input chunks stream from the DB
	// straight to the upload file, and vectors are upserted one at a time as they
	// arrive — only the index->id mapping (ids) is retained. The job respects ctx
	// cancellation. ids/written are only touched by this goroutine and read by the
	// caller after <-done, so there is no race.
	var ids []int64
	written := 0
	done := make(chan error, 1)
	go func() {
		_, err := be.EmbedStream(ctx,
			func(w *kagglebatch.InputWriter) error {
				return a.streamChunksForEmbed(ctx, p.Force, model, dims, p.Limit, func(id int64, text string) error {
					ids = append(ids, id)
					return w.Write(text)
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
			activity.RecordHeartbeat(ctx, "embedding on Kaggle")
		case err := <-done:
			if err != nil {
				return EmbedAllResult{}, fmt.Errorf("kaggle embed: %w", err)
			}
			waiting = false
		}
	}

	if written == 0 {
		log.Info("embed-all: no chunks to embed")
	} else {
		log.Info("embed-all: complete", "embedded", written)
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
