package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	pgvector "github.com/pgvector/pgvector-go"
	"golang.org/x/sync/errgroup"

	"danny.vn/banhmi/pkg/base/config"
	"danny.vn/banhmi/pkg/rag/embed/kagglebatch"
	"danny.vn/banhmi/pkg/rag/embed/sagebatch"
	dbgold "danny.vn/banhmi/pkg/store/gold"
	dbingest "danny.vn/banhmi/pkg/store/ingest"
)

// embedHeartbeat is how often the EmbedAll activity heartbeats while it waits on
// the (minutes-long) Kaggle GPU job, so Temporal sees it as alive.
const embedHeartbeat = 30 * time.Second

// EmbedAllParams configures a whole-corpus embedding pass. Owner/ModelDataset/
// Accelerator are Kaggle-specific; SageMaker* fields configure the SageMaker
// engine. Engine selects the batch backend ("kaggle", "sagemaker", or "local").
// Force re-embeds every chunk (overwrite); otherwise only chunks missing the
// canonical embedding are embedded. Limit caps the count (0 = all). The KGAT
// token comes from KAGGLE_API_TOKEN in the worker's env.
type EmbedAllParams struct {
	// Engine selects the batch backend ("kaggle", "sagemaker", or "local").
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
	// Common fields.
	Dims     int
	Force    bool
	Limit    int
	Parallel int // number of concurrent Kaggle kernels (default 1; max 2 on free tier)
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

	parallel := p.Parallel
	if parallel <= 0 {
		parallel = 2
	}

	switch engine {
	case "kaggle":
		if parallel > 1 {
			log.Info("embed-all: parallel Kaggle embedding", "kernels", parallel,
				"accelerator", p.Accelerator, "force", p.Force)
			return a.embedAllKaggleParallel(ctx, p, parallel, dims, model, dims32)
		}
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

// maxPartitionSize is the target chunks per Kaggle kernel. ~25K on dual T4
// takes ~9 min GPU; the 20-min kernel timeout gives margin for queue/upload.
const maxPartitionSize = 25000

// maxConcurrentKernels is the Kaggle free-tier GPU session limit.
const maxConcurrentKernels = 2

// cacheLookupBatch bounds the text_hash array per embedding-cache lookup query.
const cacheLookupBatch = 5000

// embedAllKaggleParallel embeds via Kaggle in three steps: a cache pre-pass
// copies vectors for unchanged text straight from ingest.embedding_cache
// (rebuilds re-chunk mostly identical text — those never reach a GPU), the
// remaining misses are split into partitions of ≤maxPartitionSize, and a
// rolling pool of concurrent kernels drains them (no round barrier: a slow
// partition never idles the other slot). Each vector is upserted on arrival
// and written through to the cache, so completed partitions survive if a
// later one fails — only the failed partition needs retry.
func (a *Activities) embedAllKaggleParallel(ctx context.Context, p EmbedAllParams, parallel, dims int, model string, dims32 int32) (EmbedAllResult, error) {
	log := a.log
	if parallel > maxConcurrentKernels {
		parallel = maxConcurrentKernels
	}

	type chunkEntry struct {
		id   int64
		text string
		hash string
	}
	var chunks []chunkEntry
	if err := a.streamChunksForEmbed(ctx, p.Force, model, dims, p.Limit, func(id int64, text string) error {
		sum := sha256.Sum256([]byte(text))
		chunks = append(chunks, chunkEntry{id: id, text: text, hash: hex.EncodeToString(sum[:])})
		return nil
	}); err != nil {
		return EmbedAllResult{}, err
	}
	total := len(chunks)
	if total == 0 {
		log.Info("embed-all: no chunks to embed")
		return EmbedAllResult{}, nil
	}

	// Cache pre-pass: copy vectors for already-seen text, collect the misses.
	var totalWritten atomic.Int64
	var misses []chunkEntry
	for start := 0; start < total; start += cacheLookupBatch {
		end := min(start+cacheLookupBatch, total)
		batch := chunks[start:end]
		hashes := make([]string, len(batch))
		for i, c := range batch {
			hashes[i] = c.hash
		}
		rows, err := a.ledger.LookupEmbeddingCache(ctx, dbingest.LookupEmbeddingCacheParams{
			Model: model, Dims: dims32, TextHashes: hashes,
		})
		if err != nil {
			return EmbedAllResult{}, fmt.Errorf("embedding cache lookup: %w", err)
		}
		vecByHash := make(map[string]pgvector.Vector, len(rows))
		for _, r := range rows {
			vecByHash[r.TextHash] = r.Embedding
		}
		for _, c := range batch {
			vec, ok := vecByHash[c.hash]
			if !ok {
				misses = append(misses, c)
				continue
			}
			if _, err := a.gold.UpsertChunkEmbedding(ctx, dbgold.UpsertChunkEmbeddingParams{
				ChunkID: c.id, Model: model, Dims: dims32, Embedding: vec,
			}); err != nil {
				return EmbedAllResult{Embedded: int(totalWritten.Load())}, fmt.Errorf("upsert cached embedding chunk %d: %w", c.id, err)
			}
			totalWritten.Add(1)
		}
	}
	cacheHits := int(totalWritten.Load())
	if len(misses) == 0 {
		log.Info("embed-all: all chunks served from embedding cache", "cache_hits", cacheHits)
		return EmbedAllResult{Embedded: cacheHits}, nil
	}

	// Build partitions of ≤maxPartitionSize over the cache misses.
	var partitions [][]chunkEntry
	for i := 0; i < len(misses); i += maxPartitionSize {
		partitions = append(partitions, misses[i:min(i+maxPartitionSize, len(misses))])
	}
	log.Info("embed-all: parallel plan", "total", total, "cache_hits", cacheHits,
		"to_embed", len(misses), "partitions", len(partitions),
		"concurrent", parallel, "partition_size", maxPartitionSize)

	// Rolling pool: up to `parallel` kernels in flight; the next partition
	// starts the moment a slot frees. First error cancels the rest; vectors
	// already upserted stay, so the retry embeds only what is still missing.
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallel)
	for pi, part := range partitions {
		g.Go(func() error {
			be, err := kagglebatch.New(kagglebatch.Options{
				Owner:        p.Owner,
				ModelDataset: p.ModelDataset,
				Accelerator:  p.Accelerator,
				Dims:         dims,
				Token:        a.kaggleToken,
			}, nil)
			if err != nil {
				return fmt.Errorf("partition %d: %w", pi, err)
			}
			_, err = be.EmbedStream(gctx,
				func(w *kagglebatch.InputWriter) error {
					for _, c := range part {
						if err := w.Write(c.text); err != nil {
							return err
						}
					}
					return nil
				},
				func(index int, vec []float32) error {
					c := part[index]
					pv := pgvector.NewVector(vec)
					if _, err := a.gold.UpsertChunkEmbedding(gctx, dbgold.UpsertChunkEmbeddingParams{
						ChunkID: c.id, Model: model, Dims: dims32, Embedding: pv,
					}); err != nil {
						return fmt.Errorf("upsert embedding chunk %d: %w", c.id, err)
					}
					if err := a.ledger.UpsertEmbeddingCache(gctx, dbingest.UpsertEmbeddingCacheParams{
						TextHash: c.hash, Model: model, Dims: dims32, Embedding: pv,
					}); err != nil {
						return fmt.Errorf("embedding cache write chunk %d: %w", c.id, err)
					}
					totalWritten.Add(1)
					return nil
				},
			)
			if err != nil {
				return fmt.Errorf("partition %d: %w", pi, err)
			}
			log.Info("embed-all: partition done", "partition", pi, "chunks", len(part))
			return nil
		})
	}

	// Heartbeat while the pool drains.
	done := make(chan error, 1)
	go func() { done <- g.Wait() }()
	ticker := time.NewTicker(embedHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			<-done // let in-flight kernels observe cancellation before returning
			return EmbedAllResult{Embedded: int(totalWritten.Load())}, ctx.Err()
		case <-ticker.C:
			log.Info("embedding on kaggle", "written", totalWritten.Load(), "total", total)
		case err := <-done:
			w := int(totalWritten.Load())
			if err != nil {
				log.Error("embed partition failed", "err", err, "written", w)
				return EmbedAllResult{Embedded: w}, fmt.Errorf("parallel embed (%d/%d embedded): %w", w, total, err)
			}
			log.Info("embed-all: parallel complete", "embedded", w, "cache_hits", cacheHits, "partitions", len(partitions))
			return EmbedAllResult{Embedded: w}, nil
		}
	}
}
