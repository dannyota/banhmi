package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPartitionChunkEntriesBoundsItemsAndBytes(t *testing.T) {
	chunks := []chunkEntry{
		{id: 1, text: "1234"},
		{id: 2, text: "5678"},
		{id: 3, text: "90"},
		{id: 4, text: "abcd"},
	}
	parts, err := partitionChunkEntries(chunks, 3, 8)
	if err != nil {
		t.Fatalf("partitionChunkEntries: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("partitions = %d, want 2", len(parts))
	}
	if len(parts[0]) != 2 || chunkEntriesBytes(parts[0]) != 8 {
		t.Fatalf("first partition = %+v, want two items and 8 bytes", parts[0])
	}
	if len(parts[1]) != 2 || chunkEntriesBytes(parts[1]) != 6 {
		t.Fatalf("second partition = %+v, want two items and 6 bytes", parts[1])
	}
}

func TestPartitionChunkEntriesBoundsItemCount(t *testing.T) {
	chunks := []chunkEntry{{id: 1, text: "a"}, {id: 2, text: "b"}, {id: 3, text: "c"}}
	parts, err := partitionChunkEntries(chunks, 2, 100)
	if err != nil {
		t.Fatalf("partitionChunkEntries: %v", err)
	}
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 1 {
		t.Fatalf("partition sizes = [%d %d], want [2 1]", len(parts[0]), len(parts[1]))
	}
}

func TestPartitionChunkEntriesRejectsOversizeInput(t *testing.T) {
	chunks := []chunkEntry{
		{id: 1, text: "small"},
		{id: 2, text: "this input exceeds the byte cap"},
		{id: 3, text: "tail"},
	}
	parts, err := partitionChunkEntries(chunks, 10, 8)
	if err == nil {
		t.Fatal("partitionChunkEntries accepted an oversized input")
	}
	if parts != nil {
		t.Fatalf("partitions = %+v, want none after rejection", parts)
	}
}

func TestPartitionChunkEntriesBoundsBackingStorage(t *testing.T) {
	chunks := []chunkEntry{{id: 1, text: "a"}, {id: 2, text: "b"}, {id: 3, text: "c"}}
	parts, err := partitionChunkEntries(chunks, 2, 100)
	if err != nil {
		t.Fatalf("partitionChunkEntries: %v", err)
	}
	if cap(parts[0]) != len(parts[0]) {
		t.Fatalf("first partition cap = %d, len = %d", cap(parts[0]), len(parts[0]))
	}
	if cap(parts[1]) != len(parts[1]) {
		t.Fatalf("second partition cap = %d, len = %d", cap(parts[1]), len(parts[1]))
	}
}

func TestEmbedChunkPageQueryForceHandling(t *testing.T) {
	forced := embedChunkPageQuery(true)
	missing := embedChunkPageQuery(false)
	if strings.Contains(forced, "chunk_embedding") {
		t.Fatalf("forced query filters existing embeddings")
	}
	if !strings.Contains(missing, "e.id IS NULL") {
		t.Fatalf("non-forced query does not select missing embeddings")
	}
	for _, query := range []string{forced, missing} {
		if !strings.Contains(query, "c.id >") || !strings.Contains(query, "LIMIT") {
			t.Fatalf("query is not keyset limited: %s", query)
		}
		if !strings.Contains(query, "octet_length") || strings.Contains(query, "COALESCE(c.context_prefix, ''), c.content") {
			t.Fatalf("metadata query loads chunk text before applying its byte bound: %s", query)
		}
	}
}

func TestValidateEmbedBudgetRequiresEveryBound(t *testing.T) {
	valid := EmbedAllParams{
		MaxItems: 1, MaxInputBytes: 1, MaxPages: 1, MaxElapsed: time.Second,
		MaxConcurrent: 1,
	}
	tests := []struct {
		name   string
		mutate func(*EmbedAllParams)
	}{
		{name: "items", mutate: func(p *EmbedAllParams) { p.MaxItems = 0 }},
		{name: "bytes", mutate: func(p *EmbedAllParams) { p.MaxInputBytes = 0 }},
		{name: "pages", mutate: func(p *EmbedAllParams) { p.MaxPages = 0 }},
		{name: "elapsed", mutate: func(p *EmbedAllParams) { p.MaxElapsed = 0 }},
		{name: "concurrent", mutate: func(p *EmbedAllParams) { p.MaxConcurrent = 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := valid
			tt.mutate(&params)
			if err := validateEmbedBudget(params); err == nil {
				t.Fatal("validateEmbedBudget accepted a missing bound")
			}
		})
	}
}

func TestEmbedAllErrorWrapsCause(t *testing.T) {
	want := errors.New("provider stopped")
	err := &EmbedAllError{Result: EmbedAllResult{Embedded: 2, Rejected: 1}, Cause: want}
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped cause", err)
	}
}

func TestProviderCompletionPrefersCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := providerCompletionError(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	want := errors.New("provider failed")
	if err := providerCompletionError(context.Background(), want); !errors.Is(err, want) {
		t.Fatalf("error = %v, want provider failure", err)
	}
	if err := providerCompletionError(ctx, want); !errors.Is(err, want) || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want provider failure and cancellation", err)
	}
}

func chunkEntriesBytes(chunks []chunkEntry) int {
	total := 0
	for _, chunk := range chunks {
		total += len(chunk.text)
	}
	return total
}
