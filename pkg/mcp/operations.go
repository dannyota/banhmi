package mcp

import (
	"context"

	"danny.vn/banhmi/internal/evidence/api"
)

// SearchInput is the shared search operation input. Its JSON fields and defaults
// are also the MCP search tool contract.
type SearchInput = api.SearchInput
type GuideInput = api.GuideInput
type GuideOutput = api.GuideOutput
type CorpusStatusInput = api.CorpusStatusInput
type CorpusStatusOutput = api.CorpusStatusOutput
type QualityGapsInput = api.QualityGapsInput
type QualityGapsOutput = api.QualityGapsOutput

// SearchOutput is the shared search operation result. It contains ranked
// evidence without model synthesis.
type SearchOutput = api.SearchOutput

// DocumentInput is the shared document operation input. Its JSON fields and
// defaults are also the MCP document tool contract.
type DocumentInput = api.DocumentInput

// DocumentOutput is the shared document operation result.
type DocumentOutput = api.DocumentOutput

// Search returns the evidence payload used by every search transport.
func (s *Server) Search(ctx context.Context, in SearchInput) (SearchOutput, error) {
	return s.operations.Search(ctx, in)
}

// Document returns the evidence payload used by every document transport.
func (s *Server) Document(ctx context.Context, in DocumentInput) (DocumentOutput, error) {
	return s.operations.Document(ctx, in)
}
