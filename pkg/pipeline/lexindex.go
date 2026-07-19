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
// Uses the jurisdiction's TextNormalizer so Thai gets TCC segmentation
// (not the VN default which strips combining marks).
func (a *Activities) LexicalIndex(ctx context.Context) (LexicalIndexResult, error) {
	norm := lexical.NormalizerFor(a.jur.TextNormalizer)
	written, err := lexical.IndexCorpusWith(ctx, a.dbpool, 2000, slog.Default(), norm)
	if err != nil {
		return LexicalIndexResult{}, fmt.Errorf("lexical index: %w", err)
	}
	return LexicalIndexResult{Written: written}, nil
}
