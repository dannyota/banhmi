package pipeline

// claimBatch is how many artifacts the fetch drainer claims per round. It is a
// database batch size, not a concurrency limit.
const claimBatch = 10

// FetchParams selects what to drain. MaxArtifacts optionally caps how many
// artifacts one run processes; 0 drains until the source queue is empty.
type FetchParams struct {
	Source       string
	MaxArtifacts int
}

// FetchAllParams selects sources to drain in one pipeline run.
type FetchAllParams struct {
	Sources      []string
	MaxArtifacts int
}

// FetchResult summarizes a Fetch run.
type FetchResult struct {
	Claimed       int // artifacts claimed and processed
	Bodies        int // body artifacts planned (detail fetched, files enqueued)
	Trees         int // provision-tree artifacts fetched or skipped
	Files         int // file artifacts processed (download attempted)
	DocsCompleted int // documents that reached state=complete this run
	DocsPartial   int // documents that reached state=partial (some artifacts dead-lettered)
}

// FetchSourceResult summarizes one source inside a multi-source fetch.
type FetchSourceResult struct {
	Source string
	Result FetchResult
}

// FetchAllResult summarizes a multi-source fetch run.
type FetchAllResult struct {
	Sources       int
	FailedSources int
	Claimed       int
	Bodies        int
	Trees         int
	Files         int
	DocsCompleted int
	DocsPartial   int
	SourceResults []FetchSourceResult
}

// ClaimParams asks the claim activity for up to Limit due artifacts for Source.
type ClaimParams struct {
	Source string
	Limit  int
}

// ClaimedArtifact is the view of a leased fetch_artifact row.
type ClaimedArtifact struct {
	ID         int64
	FetchDocID int64
	Kind       string
	RefKey     string
	FileKind   string
	FileName   string
	URL        string
}
