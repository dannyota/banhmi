package release

import "fmt"

func (m Manifest) Validate() error {
	if !validCountry(m.Country) {
		return fmt.Errorf("%w: country must be a lowercase two-letter code", ErrIncompleteManifest)
	}
	identities := []struct {
		name  string
		value string
	}{
		{"corpus hash", m.CorpusHash},
		{"source manifest digest", m.SourceManifestDigest},
		{"blob set digest", m.BlobSetDigest},
		{"schema migration digest", m.SchemaMigrationDigest},
		{"citation recipe digest", m.CitationRecipeDigest},
		{"chunking recipe digest", m.ChunkingRecipeDigest},
		{"embedding model digest", m.EmbeddingModelDigest},
		{"embedding tokenizer digest", m.EmbeddingTokenizerDigest},
		{"embedding preprocessing digest", m.EmbeddingPreprocessingDigest},
		{"lexical revision", m.LexicalRevision},
		{"lexical revision digest", m.LexicalRevisionDigest},
		{"lexical recipe digest", m.LexicalRecipeDigest},
		{"lexical tokenizer digest", m.LexicalTokenizerDigest},
		{"lexical preprocessing digest", m.LexicalPreprocessingDigest},
		{"taxonomy content digest", m.TaxonomyContentDigest},
		{"config digest", m.ConfigDigest},
		{"evaluation artifact reference", m.EvaluationArtifactRef},
		{"evaluation artifact digest", m.EvaluationArtifactDigest},
		{"compatible server image digest", m.CompatibleServerImageDigest},
		{"compatible embedder image digest", m.CompatibleEmbedderImageDigest},
	}
	for _, identity := range identities {
		if identity.value == "" {
			return fmt.Errorf("%w: %s is required", ErrIncompleteManifest, identity.name)
		}
	}
	if m.EmbeddingDimensions != EmbeddingDimensions || m.LexicalDimensions != LexicalDimensions {
		return fmt.Errorf("%w: embedding dimensions=%d lexical dimensions=%d", ErrIncompleteManifest,
			m.EmbeddingDimensions, m.LexicalDimensions)
	}
	if m.TaxonomyReleaseID <= 0 {
		return fmt.Errorf("%w: taxonomy release ID must be positive", ErrIncompleteManifest)
	}
	counts := []int64{
		m.SourceObservationCount, m.DocumentRevisionCount, m.ProvisionRevisionCount,
		m.ChunkCount, m.CitationCount, m.ProvenanceCount, m.DenseVectorCount,
		m.LexicalChunkCount, m.GapCount, m.AcceptedGapCount,
	}
	for _, count := range counts {
		if count < 0 {
			return fmt.Errorf("%w: counts must not be negative", ErrIncompleteManifest)
		}
	}
	positiveCounts := []struct {
		name  string
		value int64
	}{
		{"source observation", m.SourceObservationCount},
		{"document revision", m.DocumentRevisionCount},
		{"provision revision", m.ProvisionRevisionCount},
		{"chunk", m.ChunkCount},
	}
	for _, count := range positiveCounts {
		if count.value == 0 {
			return fmt.Errorf("%w: %s count must be positive", ErrIncompleteManifest, count.name)
		}
	}
	if m.CitationCount != m.ChunkCount || m.ProvenanceCount != m.ChunkCount ||
		m.DenseVectorCount != m.ChunkCount || m.LexicalChunkCount != m.ChunkCount {
		return fmt.Errorf("%w: chunks=%d citations=%d provenance=%d dense=%d lexical=%d",
			ErrCoverageMismatch, m.ChunkCount, m.CitationCount, m.ProvenanceCount,
			m.DenseVectorCount, m.LexicalChunkCount)
	}
	if m.AcceptedGapCount != m.GapCount {
		return fmt.Errorf("%w: gaps=%d accepted=%d", ErrUnacceptedGaps, m.GapCount, m.AcceptedGapCount)
	}
	if m.GapCount > 0 && m.GapAcceptanceRef == "" {
		return fmt.Errorf("%w: gap acceptance reference is required", ErrUnacceptedGaps)
	}
	if m.GapCount == 0 && m.GapAcceptanceRef != "" {
		return fmt.Errorf("%w: gap acceptance reference has no gaps", ErrIncompleteManifest)
	}
	return nil
}

func (m Manifest) CompatibleWith(runtime RuntimeIdentity) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if runtime.Bundle != m.Identity() || runtime.Coverage != m.Coverage() ||
		runtime.ServerImageDigest != m.CompatibleServerImageDigest ||
		runtime.EmbedderImageDigest != m.CompatibleEmbedderImageDigest {
		return fmt.Errorf("%w: bundle or image identity differs", ErrIncompatibleRuntime)
	}
	return nil
}

func (m Manifest) Coverage() Coverage {
	return Coverage{
		SourceObservationCount: m.SourceObservationCount,
		DocumentRevisionCount: m.DocumentRevisionCount,
		ProvisionRevisionCount: m.ProvisionRevisionCount,
		ChunkCount: m.ChunkCount, CitationCount: m.CitationCount,
		ProvenanceCount: m.ProvenanceCount, DenseVectorCount: m.DenseVectorCount,
		LexicalChunkCount: m.LexicalChunkCount, GapCount: m.GapCount,
		AcceptedGapCount: m.AcceptedGapCount, GapAcceptanceRef: m.GapAcceptanceRef,
	}
}

func (m Manifest) Identity() BundleIdentity {
	return BundleIdentity{
		Country: m.Country, CorpusHash: m.CorpusHash,
		SourceManifestDigest: m.SourceManifestDigest, BlobSetDigest: m.BlobSetDigest,
		SchemaMigrationDigest: m.SchemaMigrationDigest,
		CitationRecipeDigest: m.CitationRecipeDigest, ChunkingRecipeDigest: m.ChunkingRecipeDigest,
		EmbeddingModelDigest: m.EmbeddingModelDigest,
		EmbeddingTokenizerDigest: m.EmbeddingTokenizerDigest,
		EmbeddingPreprocessingDigest: m.EmbeddingPreprocessingDigest,
		EmbeddingDimensions: m.EmbeddingDimensions,
		LexicalRevision: m.LexicalRevision, LexicalRevisionDigest: m.LexicalRevisionDigest,
		LexicalRecipeDigest: m.LexicalRecipeDigest,
		LexicalTokenizerDigest: m.LexicalTokenizerDigest,
		LexicalPreprocessingDigest: m.LexicalPreprocessingDigest,
		LexicalDimensions: m.LexicalDimensions,
		TaxonomyReleaseID: m.TaxonomyReleaseID, TaxonomyContentDigest: m.TaxonomyContentDigest,
		ConfigDigest: m.ConfigDigest, EvaluationArtifactRef: m.EvaluationArtifactRef,
		EvaluationArtifactDigest: m.EvaluationArtifactDigest,
	}
}
