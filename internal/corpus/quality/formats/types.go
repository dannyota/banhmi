package formats

import "danny.vn/banhmi/internal/corpus/model"

// Normalizer is the normalizer-v1 recipe, ordered by rule and transform name.
type Normalizer struct {
	SchemaVersion string
	Rules         []NormalizationRule
	Transforms    []string
}

// NormalizationRule identifies an emitted reversible normalization rule.
type NormalizationRule struct {
	Rule    string
	Version string
}

// Parser is the parser-v1 recipe. The baseline parser is none.
type Parser struct {
	SchemaVersion string
	Parser        string
}

// CandidateInput freezes an attempt's selection before conversion.
type CandidateInput struct {
	SchemaVersion          string
	Country                string
	DocumentVersionKey     string
	ProcessingExecutionKey string
	InputDigest            model.Digest
	RecipeDigest           model.Digest
	BudgetDigest           model.Digest
	ObservationKey         string
	Artifacts              []CandidateArtifact
	Language               string
	HTMLSelector           string
	DetectorDigest         model.Digest
	ConversionRecipe       model.AssetRef
	NormalizerManifest     model.AssetRef
	ParserManifest         model.AssetRef
	GatePolicyDigest       model.Digest
	ThresholdVersion       string
	Mode                   string
	Predecessor            *Predecessor
}

// CandidateArtifact preserves the complete committed capture and source ordinal.
type CandidateArtifact struct {
	ArtifactKey        string
	ObservationOrdinal int
	Role               string
	Required           bool
	Capture            model.CaptureRef
	Format             string
}

// Predecessor binds reprocessing to an accepted candidate of the same identity.
type Predecessor struct {
	DocumentVersionKey  string
	RevisionKey         string
	CandidateDigest     model.Digest
	AcceptanceWatermark int64
}

// CandidateOutput freezes all successful conversions in candidate ordinal order.
type CandidateOutput struct {
	SchemaVersion            string
	CandidateDigest          model.Digest
	NormalizerManifestDigest model.Digest
	GatePolicyDigest         model.Digest
	TextHash                 model.Digest
	Artifacts                []OutputArtifact
}

// OutputArtifact retains the six exact logical references from document.json.
type OutputArtifact struct {
	Ordinal          int
	OriginalHash     model.Digest
	DetectorDigest   model.Digest
	RawBlocks        model.AssetRef
	NormalizedBlocks model.AssetRef
	RawText          model.AssetRef
	NormalizedText   model.AssetRef
	OffsetMap        model.AssetRef
	EditLedger       model.AssetRef
}

// PolicyBundle is the typed policy-bundle-v1 value. Its codec explicitly maps
// these fields to rule B snake-case keys; ordinary struct JSON is not the format.
type PolicyBundle struct {
	SchemaVersion    string
	Country          string
	ThresholdVersion string
	ArtifactProfile  []model.MetricRequirement
	RollupProfile    []model.MetricRequirement
	AcceptanceLimits Limits
	GapReasonCodes   []string
}

// Limits bounds acceptance work. Values are configurable policy, not authority.
type Limits struct {
	MaxBlobs       int64
	MaxRows        int64
	MaxJSONBytes   int64
	MaxObjectBytes int64
	MaxElapsedMS   int64
}

// GateReport binds an artifact report to either three conversion objects or none.
type GateReport struct {
	SchemaVersion   string
	Artifact        model.QualityReport
	RequestManifest *model.AssetRef
	Document        *model.AssetRef
	OutputManifest  *model.AssetRef
}

// GateRollup binds every selected artifact report, coverage and document metrics.
type GateRollup struct {
	SchemaVersion    string
	CandidateDigest  model.Digest
	OutputManifest   *model.AssetRef
	PolicyHash       model.Digest
	ThresholdVersion string
	Outcome          model.QualityOutcome
	ArtifactReports  []ArtifactReport
	MissingOrdinals  []int
	Metrics          []model.QualityMetric
	Findings         []model.Finding
	Gaps             []Gap
	Retained         []Retained
	DecisionRef      string
}

// ArtifactReport selects one per-artifact gate report at its candidate ordinal.
type ArtifactReport struct {
	Ordinal      int
	OriginalHash model.Digest
	Report       model.AssetRef
	Outcome      model.QualityOutcome
}

// Gap records an exact exclusion or missing coverage scope.
type Gap struct {
	Ordinal    int
	Scope      model.GapKind
	ScopeKey   string
	ReasonCode string
	Spans      []model.Span
}

// Retained records the retained aggregate normalized-text spans of one ordinal.
type Retained struct {
	Ordinal int
	Spans   []model.Span
}

// GapScope is synthetic gap-scope-v1 evidence. It grants no review authority
// and deliberately omits the final report hash and decision key.
type GapScope struct {
	SchemaVersion   string
	CandidateDigest model.Digest
	OriginalHash    model.Digest
	OutputDigest    model.Digest
	Gaps            []Gap
	Retained        []Retained
}
