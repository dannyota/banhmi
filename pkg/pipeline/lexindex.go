package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.temporal.io/sdk/workflow"

	"danny.vn/banhmi/pkg/rag/lexical"
)

const workflowLexicalIndex = "LexicalIndex"

// LexicalIndexResult is returned by the LexicalIndex activity.
type LexicalIndexResult struct {
	Written int
}

// LexicalIndexWorkflow wraps the LexicalIndex activity as a standalone workflow
// for the -lexindex dev flag. Inside RunAll it runs as a direct activity call.
func LexicalIndexWorkflow(ctx workflow.Context) (LexicalIndexResult, error) {
	var acts *Activities
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           LocalActivityTaskQueue(workflow.GetInfo(ctx).TaskQueueName),
		StartToCloseTimeout: 30 * time.Minute,
		HeartbeatTimeout:    5 * time.Minute,
	})
	var res LexicalIndexResult
	if err := workflow.ExecuteActivity(actCtx, acts.LexicalIndex).Get(actCtx, &res); err != nil {
		return res, fmt.Errorf("lexical index: %w", err)
	}
	return res, nil
}

// LexicalIndex trains BM25 on the chunk corpus and writes sparse vectors.
func (a *Activities) LexicalIndex(ctx context.Context) (LexicalIndexResult, error) {
	written, err := lexical.IndexCorpus(ctx, a.dbpool, 2000, slog.Default())
	if err != nil {
		return LexicalIndexResult{}, fmt.Errorf("lexical index: %w", err)
	}
	return LexicalIndexResult{Written: written}, nil
}
