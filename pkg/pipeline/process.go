package pipeline

// StageParams identifies the document stage target by its ledger id; stage
// activities resolve the source/external_id, bronze files, and silver rows from it.
type StageParams struct {
	FetchDocID int64
}

// ListStageFetchDocIDsAfterParams pages fetch_doc IDs that still need one local
// processing stage.
type ListStageFetchDocIDsAfterParams struct {
	AfterID int64
	Limit   int32
	Force   bool
}

// StageAllParams controls paginated and bounded bulk stage execution.
type StageAllParams struct {
	AfterID       int64
	Limit         int32
	BatchSize     int32
	MaxConcurrent int32
	Force         bool
}

// ExtractAllParams is kept as an alias for existing callers.
type ExtractAllParams = StageAllParams

// ExtractResult summarizes an Extract run.
type ExtractResult struct {
	DocumentID        int64
	Engine            string
	Confidence        float64
	NeedsReview       bool
	SourceUnavailable bool
}

// ExtractAllResult summarizes a batch Extract run.
type ExtractAllResult struct {
	Total             int
	Completed         int
	Failed            int
	NeedsReview       int
	SourceUnavailable int
}

// NormalizeAllResult summarizes a batch Normalize run.
type NormalizeAllResult struct {
	Total                   int
	Completed               int
	Failed                  int
	SectionsWritten         int
	RelationTargetsEnqueued int
	Skipped                 int
}

// IndexAllResult summarizes a batch Index run.
type IndexAllResult struct {
	Total         int
	Completed     int
	Failed        int
	ChunksWritten int
}
