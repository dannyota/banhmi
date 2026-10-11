package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
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

// EmbedAllParams configures one bounded embedding shard. Owner/ModelDataset/
// Accelerator are Kaggle-specific; SageMaker* fields configure the SageMaker
// engine. Engine selects the batch backend ("kaggle", "sagemaker", or "local").
// Force re-embeds every chunk (overwrite); otherwise only chunks missing the
// canonical embedding are embedded. MaxItems sets the shard cap; a positive
// Limit can lower that cap. The KGAT token comes from KAGGLE_API_TOKEN in the
// worker's environment.
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
	AfterID  int64

	MaxItems      int
	MaxInputBytes int64
	MaxPages      int // logical keyset pages; EmbedAll processes exactly one
	MaxElapsed    time.Duration
	MaxConcurrent int
}

// EmbedAllResult reports how many chunk embeddings were written.
type EmbedAllResult struct {
	Embedded   int
	Rejected   int
	NextCursor int64
	Complete   bool
}

type EmbedAllError struct {
	Result EmbedAllResult
	Cause  error
}

func (e *EmbedAllError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("embed-all failed: embedded=%d rejected=%d next_cursor=%d: %v",
			e.Result.Embedded, e.Result.Rejected, e.Result.NextCursor, e.Cause)
	}
	return fmt.Sprintf("embed-all failed: embedded=%d rejected=%d next_cursor=%d",
		e.Result.Embedded, e.Result.Rejected, e.Result.NextCursor)
}

func (e *EmbedAllError) Unwrap() error { return e.Cause }

func providerCompletionError(ctx context.Context, providerErr error) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(providerErr, err)
	}
	return providerErr
}

func validateEmbedBudget(p EmbedAllParams) error {
	switch {
	case p.MaxItems <= 0:
		return fmt.Errorf("embed max items must be positive")
	case p.MaxItems == int(^uint(0)>>1):
		return fmt.Errorf("embed max items is too large")
	case p.MaxInputBytes <= 0:
		return fmt.Errorf("embed max input bytes must be positive")
	case p.MaxPages <= 0:
		return fmt.Errorf("embed max pages must be positive")
	case p.MaxElapsed <= 0:
		return fmt.Errorf("embed max elapsed must be positive")
	case p.MaxConcurrent <= 0:
		return fmt.Errorf("embed max concurrent must be positive")
	default:
		return nil
	}
}

// EmbedAll loads the target chunks, embeds them in a single batch job (Kaggle or
// SageMaker, selected by p.Engine), and upserts the vectors under the canonical
// model tag (config.EmbedModel) so retrieval — which filters by that tag — finds
// them. It heartbeats while the job runs. This is the batch path for
// IndexAll-scale embedding; the local OVMS embedder remains the serve-time/query
// path.
func (a *Activities) EmbedAll(ctx context.Context, p EmbedAllParams) (EmbedAllResult, error) {
	if err := validateEmbedBudget(p); err != nil {
		return EmbedAllResult{}, &EmbedAllError{Cause: err}
	}
	ctx, cancel := context.WithTimeout(ctx, p.MaxElapsed)
	defer cancel()
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

	parallel := p.Parallel
	if parallel <= 0 {
		parallel = 2
	}
	parallel = min(parallel, p.MaxConcurrent)
	if p.Limit <= 0 || p.Limit > p.MaxItems {
		p.Limit = p.MaxItems
	}

	switch engine {
	case "kaggle":
		log.Info("embed-all: bounded Kaggle embedding", "kernels", parallel,
			"accelerator", p.Accelerator, "force", p.Force)
		return a.embedAllKaggleParallel(ctx, p, parallel, dims, model, dims32)

	case "sagemaker":
		return a.embedAllSageMakerBounded(ctx, p, dims, model, dims32)

	default:
		return EmbedAllResult{}, fmt.Errorf("unsupported embed engine %q", engine)
	}
}

// embedAllSageMakerBounded admits one bounded keyset shard before constructing
// the legacy synchronous SageMaker provider. The provider call is unfenced and
// nonresumable; the durable submit/check/apply path is implemented separately.
func (a *Activities) embedAllSageMakerBounded(ctx context.Context, p EmbedAllParams, dims int, model string, dims32 int32) (EmbedAllResult, error) {
	page, rejected, cursor, complete, err := a.loadChunkPageForEmbed(
		ctx, p.Force, model, dims, p.AfterID, p.Limit, p.MaxInputBytes)
	result := EmbedAllResult{Rejected: rejected, NextCursor: cursor, Complete: complete}
	if err != nil {
		return result, &EmbedAllError{Result: result, Cause: err}
	}
	if len(page) == 0 {
		if rejected > 0 {
			return result, &EmbedAllError{Result: result}
		}
		return result, nil
	}

	be, err := sagebatch.New(sagebatch.Options{
		Bucket: p.SageMakerBucket, RoleARN: p.SageMakerRoleARN, Region: p.SageMakerRegion,
		InstanceType: p.SageMakerInstanceType, ContainerImage: p.SageMakerContainerImage,
		Dims: dims,
	}, nil)
	if err != nil {
		return result, &EmbedAllError{Result: result, Cause: fmt.Errorf("sagemaker embedder: %w", err)}
	}

	var written atomic.Int64
	done := make(chan error, 1)
	go func() {
		_, runErr := be.EmbedStream(ctx,
			func(w *sagebatch.InputWriter) error {
				for _, chunk := range page {
					if err := w.Write(chunk.text); err != nil {
						return err
					}
				}
				return nil
			},
			func(index int, vec []float32) error {
				if index < 0 || index >= len(page) {
					return fmt.Errorf("vector index %d out of range [0,%d)", index, len(page))
				}
				chunk := page[index]
				if len(vec) != dims {
					return fmt.Errorf("chunk %d vector dims = %d, want %d", chunk.id, len(vec), dims)
				}
				if _, err := a.gold.UpsertChunkEmbedding(ctx, dbgold.UpsertChunkEmbeddingParams{
					ChunkID: chunk.id, Model: model, Dims: dims32, Embedding: pgvector.NewVector(vec),
				}); err != nil {
					return fmt.Errorf("upsert embedding chunk %d: %w", chunk.id, err)
				}
				written.Add(1)
				return nil
			},
		)
		done <- runErr
	}()

	ticker := time.NewTicker(embedHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			result.Embedded = int(written.Load())
			return result, &EmbedAllError{Result: result, Cause: ctx.Err()}
		case <-ticker.C:
			a.log.Info("embedding on sagemaker", "written", written.Load(), "selected", len(page))
		case err := <-done:
			result.Embedded = int(written.Load())
			err = providerCompletionError(ctx, err)
			if err != nil {
				return result, &EmbedAllError{Result: result, Cause: fmt.Errorf("sagemaker embed: %w", err)}
			}
			if result.Rejected > 0 {
				return result, &EmbedAllError{Result: result}
			}
			return result, nil
		}
	}
}

// maxPartitionSize is the target chunks per Kaggle kernel. ~25K on dual T4
// takes ~9 min GPU; the 20-min kernel timeout gives margin for queue/upload.
const maxPartitionSize = 25000

// maxPartitionBytes bounds text retained by one Kaggle kernel input.
const maxPartitionBytes = 32 << 20

// maxConcurrentKernels is the Kaggle free-tier GPU session limit.
const maxConcurrentKernels = 2

// cacheLookupBatch bounds the text_hash array per embedding-cache lookup query.
const cacheLookupBatch = 5000

type chunkEntry struct {
	id   int64
	text string
	hash string
}

// embedAllKaggleParallel handles one bounded keyset shard, resolves cache hits,
// and sends bounded miss partitions to a rolling pool of Kaggle kernels. The
// database row cursor closes before cache writes or provider waits. Each vector
// is upserted on arrival and written through to the existing model/text cache.
// The provider call is the legacy synchronous, unfenced, nonresumable path.
func (a *Activities) embedAllKaggleParallel(ctx context.Context, p EmbedAllParams, parallel, dims int, model string, dims32 int32) (EmbedAllResult, error) {
	log := a.log
	if parallel > maxConcurrentKernels {
		parallel = maxConcurrentKernels
	}
	if parallel <= 0 {
		parallel = 1
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, gctx := errgroup.WithContext(runCtx)
	g.SetLimit(parallel)
	var totalSelected, totalWritten, totalRejected, cacheHits, partitionCount, nextCursor atomic.Int64
	var shardComplete atomic.Bool

	done := make(chan error, 1)
	go func() {
		produceErr := func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			page, rejected, cursor, complete, err := a.loadChunkPageForEmbed(
				gctx, p.Force, model, dims, p.AfterID, p.Limit, p.MaxInputBytes)
			if err != nil {
				return err
			}
			nextCursor.Store(cursor)
			shardComplete.Store(complete)
			totalRejected.Store(int64(rejected))
			totalSelected.Store(int64(len(page) + rejected))
			if len(page) == 0 {
				return nil
			}

			misses := make([]chunkEntry, 0, len(page))
			for start := 0; start < len(page); start += cacheLookupBatch {
				end := min(start+cacheLookupBatch, len(page))
				batch := page[start:end]
				hashes := make([]string, len(batch))
				for i, chunk := range batch {
					hashes[i] = chunk.hash
				}
				rows, err := a.ledger.LookupEmbeddingCache(gctx, dbingest.LookupEmbeddingCacheParams{
					Model: model, Dims: dims32, TextHashes: hashes,
				})
				if err != nil {
					return fmt.Errorf("embedding cache lookup: %w", err)
				}
				vecByHash := make(map[string]pgvector.Vector, len(rows))
				for _, row := range rows {
					vecByHash[row.TextHash] = row.Embedding
				}
				for _, chunk := range batch {
					vec, ok := vecByHash[chunk.hash]
					if !ok {
						misses = append(misses, chunk)
						continue
					}
					if _, err := a.gold.UpsertChunkEmbedding(gctx, dbgold.UpsertChunkEmbeddingParams{
						ChunkID: chunk.id, Model: model, Dims: dims32, Embedding: vec,
					}); err != nil {
						return fmt.Errorf("upsert cached embedding chunk %d: %w", chunk.id, err)
					}
					totalWritten.Add(1)
					cacheHits.Add(1)
				}
			}

			parts, err := partitionChunkEntries(misses, maxPartitionSize, maxPartitionBytes)
			if err != nil {
				return err
			}
			for _, part := range parts {
				partNumber := partitionCount.Add(1)
				g.Go(func() error {
						be, err := kagglebatch.New(kagglebatch.Options{
							Owner: p.Owner, ModelDataset: p.ModelDataset, Accelerator: p.Accelerator,
							Dims: dims, Token: a.kaggleToken,
						}, nil)
						if err != nil {
							return fmt.Errorf("partition %d: %w", partNumber, err)
						}
						_, err = be.EmbedStream(gctx,
							func(w *kagglebatch.InputWriter) error {
								for _, chunk := range part {
									if err := w.Write(chunk.text); err != nil {
										return err
									}
								}
								return nil
							},
							func(index int, vec []float32) error {
								if index < 0 || index >= len(part) {
									return fmt.Errorf("partition %d vector index %d out of range [0,%d)", partNumber, index, len(part))
								}
								chunk := part[index]
								if len(vec) != dims {
									return fmt.Errorf("chunk %d vector dims = %d, want %d", chunk.id, len(vec), dims)
								}
								pv := pgvector.NewVector(vec)
								if _, err := a.gold.UpsertChunkEmbedding(gctx, dbgold.UpsertChunkEmbeddingParams{
									ChunkID: chunk.id, Model: model, Dims: dims32, Embedding: pv,
								}); err != nil {
									return fmt.Errorf("upsert embedding chunk %d: %w", chunk.id, err)
								}
								if err := a.ledger.UpsertEmbeddingCache(gctx, dbingest.UpsertEmbeddingCacheParams{
									TextHash: chunk.hash, Model: model, Dims: dims32, Embedding: pv,
								}); err != nil {
									return fmt.Errorf("embedding cache write chunk %d: %w", chunk.id, err)
								}
								totalWritten.Add(1)
								return nil
							},
						)
						if err != nil {
							return fmt.Errorf("partition %d: %w", partNumber, err)
						}
						log.Info("embed-all: partition done", "partition", partNumber, "chunks", len(part))
						return nil
				})
			}
			return nil
		}()
		if produceErr != nil {
			cancel()
		}
		workerErr := g.Wait()
		if workerErr != nil {
			done <- workerErr
			return
		}
		if produceErr != nil {
			done <- produceErr
			return
		}
		done <- nil
	}()

	ticker := time.NewTicker(embedHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			result := EmbedAllResult{Embedded: int(totalWritten.Load()), Rejected: int(totalRejected.Load()),
				NextCursor: nextCursor.Load(), Complete: shardComplete.Load()}
			return result, &EmbedAllError{Result: result, Cause: ctx.Err()}
		case <-ticker.C:
			log.Info("embedding on kaggle", "written", totalWritten.Load(), "selected", totalSelected.Load())
		case err := <-done:
			result := EmbedAllResult{Embedded: int(totalWritten.Load()), Rejected: int(totalRejected.Load()),
				NextCursor: nextCursor.Load(), Complete: shardComplete.Load()}
			err = providerCompletionError(ctx, err)
			if err != nil {
				return result, &EmbedAllError{Result: result,
					Cause: fmt.Errorf("parallel embed (%d/%d embedded): %w", result.Embedded, totalSelected.Load(), err)}
			}
			if totalSelected.Load() == 0 {
				log.Info("embed-all: no chunks to embed")
				return result, nil
			}
			log.Info("embed-all: parallel complete", "embedded", result.Embedded, "rejected", result.Rejected, "cache_hits", cacheHits.Load(),
				"partitions", partitionCount.Load())
			if result.Rejected > 0 {
				return result, &EmbedAllError{Result: result}
			}
			return result, nil
		}
	}
}

func (a *Activities) loadChunkPageForEmbed(ctx context.Context, force bool, model string, dims int, afterID int64, itemLimit int, byteLimit int64) ([]chunkEntry, int, int64, bool, error) {
	tx, err := a.dbpool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return nil, 0, afterID, false, fmt.Errorf("begin bounded chunk snapshot: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	var args []any
	if force {
		args = []any{afterID, itemLimit + 1}
	} else {
		args = []any{model, dims, afterID, itemLimit + 1}
	}
	rows, err := tx.Query(ctx, embedChunkPageQuery(force), args...)
	if err != nil {
		return nil, 0, afterID, false, fmt.Errorf("load chunks to embed after %d: %w", afterID, err)
	}
	type metadata struct {
		id    int64
		bytes int64
	}
	meta := make([]metadata, 0, itemLimit+1)
	for rows.Next() {
		var item metadata
		if err := rows.Scan(&item.id, &item.bytes); err != nil {
			rows.Close()
			return nil, 0, afterID, false, fmt.Errorf("scan chunk metadata: %w", err)
		}
		meta = append(meta, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, 0, afterID, false, fmt.Errorf("iterate chunk metadata: %w", err)
	}
	rows.Close()
	if len(meta) == 0 {
		return nil, 0, afterID, true, nil
	}
	complete := len(meta) <= itemLimit
	if len(meta) > itemLimit {
		meta = meta[:itemLimit]
	}
	ids := make([]int64, 0, len(meta))
	var pageBytes int64
	rejected := 0
	cursor := afterID
	for _, item := range meta {
		if item.bytes <= 0 || item.bytes > int64(maxPartitionBytes) || item.bytes > byteLimit {
			rejected++
			cursor = item.id
			continue
		}
		if item.bytes > byteLimit-pageBytes {
			complete = false
			break
		}
		pageBytes += item.bytes
		ids = append(ids, item.id)
		cursor = item.id
	}
	if len(ids) == 0 {
		return nil, rejected, cursor, complete, nil
	}
	textRows, err := tx.Query(ctx, embedChunkTextQuery(), ids)
	if err != nil {
		return nil, rejected, cursor, complete, fmt.Errorf("load bounded chunk text: %w", err)
	}
	page := make([]chunkEntry, 0, len(ids))
	for textRows.Next() {
		var id int64
		var prefix, content string
		if err := textRows.Scan(&id, &prefix, &content); err != nil {
			textRows.Close()
			return nil, rejected, cursor, complete, fmt.Errorf("scan bounded chunk text: %w", err)
		}
		chunkText := strings.TrimSpace(strings.Join([]string{strings.TrimSpace(prefix), strings.TrimSpace(content)}, "\n"))
		if chunkText == "" || len(chunkText) > maxPartitionBytes {
			textRows.Close()
			return nil, rejected, cursor, complete, fmt.Errorf("chunk %d changed after size preflight", id)
		}
		sum := sha256.Sum256([]byte(chunkText))
		page = append(page, chunkEntry{id: id, text: chunkText, hash: hex.EncodeToString(sum[:])})
	}
	if err := textRows.Err(); err != nil {
		textRows.Close()
		return nil, rejected, cursor, complete, fmt.Errorf("iterate bounded chunk text: %w", err)
	}
	textRows.Close()
	if len(page) != len(ids) {
		return nil, rejected, cursor, complete, fmt.Errorf("loaded %d bounded chunks, want %d", len(page), len(ids))
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, rejected, cursor, complete, fmt.Errorf("commit bounded chunk snapshot: %w", err)
	}
	return page, rejected, cursor, complete, nil
}

func embedChunkPageQuery(force bool) string {
	if force {
		return `
SELECT c.id, octet_length(COALESCE(c.context_prefix, '')) + octet_length(c.content) + 1
FROM gold.chunk c
WHERE c.id > $1
ORDER BY c.id
LIMIT $2`
	}
	return `
SELECT c.id, octet_length(COALESCE(c.context_prefix, '')) + octet_length(c.content) + 1
FROM gold.chunk c
LEFT JOIN gold.chunk_embedding e
  ON e.chunk_id = c.id AND e.model = $1 AND e.dims = $2
WHERE e.id IS NULL AND c.id > $3
ORDER BY c.id
LIMIT $4`
}

func embedChunkTextQuery() string {
	return `
SELECT c.id, COALESCE(c.context_prefix, ''), c.content
FROM gold.chunk c
WHERE c.id = ANY($1::bigint[])
ORDER BY c.id`
}

func partitionChunkEntries(chunks []chunkEntry, itemLimit, byteLimit int) ([][]chunkEntry, error) {
	if itemLimit <= 0 || byteLimit <= 0 {
		return nil, fmt.Errorf("partition item and byte limits must be positive")
	}
	if len(chunks) == 0 {
		return nil, nil
	}
	var partitions [][]chunkEntry
	for start := 0; start < len(chunks); {
		if len(chunks[start].text) > byteLimit {
			return nil, fmt.Errorf("chunk %d input bytes %d exceed partition limit %d",
				chunks[start].id, len(chunks[start].text), byteLimit)
		}
		end := start
		bytes := 0
		for end < len(chunks) && end-start < itemLimit {
			nextBytes := len(chunks[end].text)
			if end > start && nextBytes > byteLimit-bytes {
				break
			}
			bytes += nextBytes
			end++
			if bytes >= byteLimit {
				break
			}
		}
		partitions = append(partitions, chunks[start:end:end])
		start = end
	}
	return partitions, nil
}
