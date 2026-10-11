package release

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestManifestValidateAcceptsCompleteRecordedGap(t *testing.T) {
	manifest := validManifest()
	manifest.GapCount = 1
	manifest.AcceptedGapCount = 1
	manifest.GapAcceptanceRef = "blob:gap-review"

	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestManifestValidateRejectsMissingIdentity(t *testing.T) {
	manifest := validManifest()
	manifest.EmbeddingTokenizerDigest = ""

	err := manifest.Validate()
	if !errors.Is(err, ErrIncompleteManifest) {
		t.Fatalf("Validate() error = %v, want ErrIncompleteManifest", err)
	}
}

func TestManifestValidateRejectsUnacceptedGap(t *testing.T) {
	manifest := validManifest()
	manifest.GapCount = 2
	manifest.AcceptedGapCount = 1
	manifest.GapAcceptanceRef = "blob:gap-review"

	err := manifest.Validate()
	if !errors.Is(err, ErrUnacceptedGaps) {
		t.Fatalf("Validate() error = %v, want ErrUnacceptedGaps", err)
	}
}

func TestManifestValidateRejectsCoverageMismatch(t *testing.T) {
	manifest := validManifest()
	manifest.ProvenanceCount--

	err := manifest.Validate()
	if !errors.Is(err, ErrCoverageMismatch) {
		t.Fatalf("Validate() error = %v, want ErrCoverageMismatch", err)
	}
}

func TestManifestValidateRejectsAllZeroCoverage(t *testing.T) {
	manifest := validManifest()
	manifest.SourceObservationCount = 0
	manifest.DocumentRevisionCount = 0
	manifest.ProvisionRevisionCount = 0
	manifest.ChunkCount = 0
	manifest.CitationCount = 0
	manifest.ProvenanceCount = 0
	manifest.DenseVectorCount = 0
	manifest.LexicalChunkCount = 0

	err := manifest.Validate()
	if !errors.Is(err, ErrIncompleteManifest) {
		t.Fatalf("Validate() error = %v, want ErrIncompleteManifest", err)
	}
}

func TestManifestValidateRejectsZeroRequiredEvidenceCount(t *testing.T) {
	tests := []struct {
		name   string
		update func(*Manifest)
	}{
		{name: "source observations", update: func(m *Manifest) { m.SourceObservationCount = 0 }},
		{name: "document revisions", update: func(m *Manifest) { m.DocumentRevisionCount = 0 }},
		{name: "provision revisions", update: func(m *Manifest) { m.ProvisionRevisionCount = 0 }},
		{name: "chunks", update: func(m *Manifest) {
			m.ChunkCount = 0
			m.CitationCount = 0
			m.ProvenanceCount = 0
			m.DenseVectorCount = 0
			m.LexicalChunkCount = 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.update(&manifest)
			if err := manifest.Validate(); !errors.Is(err, ErrIncompleteManifest) {
				t.Fatalf("Validate() error = %v, want ErrIncompleteManifest", err)
			}
		})
	}
}

func TestReleaseValidateRejectsMissingBuildID(t *testing.T) {
	release := validRelease("candidate")
	release.BuildID = uuid.Nil

	err := release.Validate()
	if !errors.Is(err, ErrIncompleteManifest) {
		t.Fatalf("Validate() error = %v, want ErrIncompleteManifest", err)
	}
}

func TestManifestCompatibleWithRejectsImageMismatch(t *testing.T) {
	manifest := validManifest()
	err := manifest.CompatibleWith(RuntimeIdentity{
		Bundle:              manifest.Identity(),
		Coverage:            manifest.Coverage(),
		ServerImageDigest:   "sha256:other",
		EmbedderImageDigest: manifest.CompatibleEmbedderImageDigest,
	})
	if !errors.Is(err, ErrIncompatibleRuntime) {
		t.Fatalf("CompatibleWith() error = %v, want ErrIncompatibleRuntime", err)
	}
}

func validManifest() Manifest {
	return Manifest{
		Country:                            "vn",
		CorpusHash:                         "sha256:corpus",
		SourceManifestDigest:                "sha256:sources",
		BlobSetDigest:                       "sha256:blobs",
		SchemaMigrationDigest:               "sha256:schema",
		CitationRecipeDigest:                "sha256:citation",
		ChunkingRecipeDigest:                "sha256:chunking",
		EmbeddingModelDigest:                "sha256:model",
		EmbeddingTokenizerDigest:            "sha256:tokenizer",
		EmbeddingPreprocessingDigest:        "sha256:preprocessing",
		EmbeddingDimensions:                 1024,
		LexicalRevision:                     "lexical-v1",
		LexicalRevisionDigest:               "sha256:lexical-revision",
		LexicalRecipeDigest:                 "sha256:lexical-recipe",
		LexicalTokenizerDigest:              "sha256:lexical-tokenizer",
		LexicalPreprocessingDigest:          "sha256:lexical-preprocessing",
		LexicalDimensions:                   1048576,
		TaxonomyReleaseID:                   7,
		TaxonomyContentDigest:               "sha256:taxonomy",
		ConfigDigest:                        "sha256:config",
		EvaluationArtifactRef:               "blob:evaluation",
		EvaluationArtifactDigest:            "sha256:evaluation",
		CompatibleServerImageDigest:         "sha256:server",
		CompatibleEmbedderImageDigest:       "sha256:embedder",
		SourceObservationCount:              3,
		DocumentRevisionCount:               2,
		ProvisionRevisionCount:              4,
		ChunkCount:                          5,
		CitationCount:                       5,
		ProvenanceCount:                     5,
		DenseVectorCount:                    5,
		LexicalChunkCount:                   5,
	}
}
