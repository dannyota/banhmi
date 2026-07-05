package pipeline

import (
	"context"
	"fmt"
	"log/slog"

	"danny.vn/banhmi/pkg/rag/lexical"
)

// LexicalIndexResult is returned by the LexicalIndex activity.
type LexicalIndexResult struct {
	Written int
}

// LexicalIndex trains BM25 on the chunk corpus and writes sparse vectors.
func (a *Activities) LexicalIndex(ctx context.Context) (LexicalIndexResult, error) {
	written, err := lexical.IndexCorpus(ctx, a.dbpool, 2000, slog.Default())
	if err != nil {
		return LexicalIndexResult{}, fmt.Errorf("lexical index: %w", err)
	}
	return LexicalIndexResult{Written: written}, nil
}
