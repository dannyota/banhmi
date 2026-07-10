package pipeline

import (
	"context"
	"fmt"
	"sort"

	"danny.vn/banhmi/pkg/base/config"
)

const vbplSource = "vbpl"

// RunAllParams configures one whole-pipeline run.
type RunAllParams struct {
	Sources       []string
	MaxArtifacts  int
	MaxRounds     int
	BackfillLimit int32
	Stage         StageAllParams
	SkipOCR       bool
	Ocr           OcrAllParams
	SkipEmbed     bool
	Embed         EmbedAllParams
}

// RunAllResult summarizes one whole-pipeline run.
type RunAllResult struct {
	DiscoverSlices    int
	Discovered        int
	Enqueued          int
	Rounds            int
	Converged         bool
	Fetched           int
	Extracted         int
	Normalized        int
	RelationsEnqueued int
	OcrProcessed      int
	IndexedChunks     int
	Embedded          int
	LexicalIndexed    int
}

// RunAllParamsFromConfig builds the run-all parameters from config and the
// wired source set.
func RunAllParamsFromConfig(cfg *config.Config, sources []string) RunAllParams {
	return RunAllParams{
		Sources:       sources,
		MaxArtifacts:  0,
		MaxRounds:     3,
		BackfillLimit: 1000,
		Stage:         StageAllParams{},
		Ocr: OcrAllParams{
			Engine:      cfg.OcrEngine(),
			Owner:       cfg.Extract.OCR.Kaggle.Owner,
			Accelerator: cfg.Extract.OCR.Kaggle.Accelerator,
			Command:     cfg.Extract.OCR.Command,
			Script:      cfg.Extract.OCR.Script,
			Languages:   cfg.OCRLanguages(),
			DPI:         cfg.Extract.OCR.DPI,
			BatchSize:   cfg.Extract.OCR.BatchSize,
			Processor:   cfg.Extract.OCR.DocumentAI.Processor,
			Bucket:      cfg.Extract.OCR.DocumentAI.Bucket,
		},
		Embed: EmbedAllParams{
			Engine:                  cfg.EmbedEngine(),
			Owner:                   cfg.Embed.Kaggle.Owner,
			ModelDataset:            cfg.Embed.Kaggle.ModelDataset,
			Accelerator:             cfg.Embed.Kaggle.Accelerator,
			SageMakerBucket:         cfg.Embed.SageMaker.Bucket,
			SageMakerRoleARN:        cfg.Embed.SageMaker.RoleARN,
			SageMakerRegion:         cfg.Embed.SageMaker.Region,
			SageMakerInstanceType:   cfg.Embed.SageMaker.InstanceType,
			SageMakerContainerImage: cfg.Embed.SageMaker.ContainerImage,
			Dims:                    config.EmbedDims,
		},
	}
}

// DiscoverSlices returns the (source, keyword) discovery slices to run for the
// given enabled sources, mirroring the old EnsureSchedules: each source gets a
// keyword-less sweep, and vbpl adds one slice per configured discovery keyword.
func (a *Activities) DiscoverSlices(ctx context.Context, sources []string) ([]DiscoverParams, error) {
	ids := append([]string(nil), sources...)
	sort.Strings(ids)

	var slices []DiscoverParams
	for _, id := range ids {
		if _, ok := a.sources[id]; !ok {
			continue
		}
		slices = append(slices, DiscoverParams{Source: id})
		if id == vbplSource {
			keywords, err := a.configQ.ListDiscoveryKeywords(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("list %s discovery keywords: %w", id, err)
			}
			for _, kw := range keywords {
				slices = append(slices, DiscoverParams{Source: id, Keyword: kw})
			}
		}
	}
	return slices, nil
}
