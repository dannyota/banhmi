package pipeline

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestExtractAllWorkflowUsesNeedsExtractSelector(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var a *Activities
	env.OnActivity(a.ListFetchDocIDsNeedingExtractAfter, mock.Anything, ListStageFetchDocIDsAfterParams{
		AfterID: 0,
		Limit:   2,
	}).Return([]int64{10, 11}, nil).Once()
	env.OnActivity(a.Extract, mock.Anything, StageParams{FetchDocID: int64(10)}).
		Return(ExtractResult{DocumentID: 100, NeedsReview: true}, nil).Once()
	env.OnActivity(a.Extract, mock.Anything, StageParams{FetchDocID: int64(11)}).
		Return(ExtractResult{DocumentID: 101}, nil).Once()
	env.OnActivity(a.ListFetchDocIDsNeedingExtractAfter, mock.Anything, ListStageFetchDocIDsAfterParams{
		AfterID: 11,
		Limit:   2,
	}).Return([]int64(nil), nil).Once()

	env.ExecuteWorkflow(ExtractAllWorkflow, StageAllParams{BatchSize: 2, MaxConcurrent: 1})

	if !env.IsWorkflowCompleted() {
		t.Fatal("ExtractAllWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("ExtractAllWorkflow error: %v", err)
	}
	var res ExtractAllResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if res.Total != 2 || res.Completed != 2 || res.NeedsReview != 1 {
		t.Fatalf("result = %+v, want total=2 completed=2 needs_review=1", res)
	}
	env.AssertExpectations(t)
}

func TestNormalizeAllWorkflowUsesNeedsNormalizeSelector(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var a *Activities
	env.OnActivity(a.ListFetchDocIDsNeedingNormalizeAfter, mock.Anything, ListStageFetchDocIDsAfterParams{
		AfterID: 0,
		Limit:   2,
	}).Return([]int64{20}, nil).Once()
	env.OnActivity(a.Normalize, mock.Anything, StageParams{FetchDocID: int64(20)}).
		Return(NormalizeResult{DocumentID: 200, SectionsWritten: 7, RelationTargetsEnqueued: 3}, nil).Once()

	env.ExecuteWorkflow(NormalizeAllWorkflow, StageAllParams{BatchSize: 2, MaxConcurrent: 1})

	if !env.IsWorkflowCompleted() {
		t.Fatal("NormalizeAllWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("NormalizeAllWorkflow error: %v", err)
	}
	var res NormalizeAllResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if res.Total != 1 || res.Completed != 1 || res.SectionsWritten != 7 || res.RelationTargetsEnqueued != 3 {
		t.Fatalf("result = %+v, want one normalized doc with counts", res)
	}
	env.AssertExpectations(t)
}

func TestNormalizeAllWorkflowPassesForceToSelector(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var a *Activities
	env.OnActivity(a.ListFetchDocIDsNeedingNormalizeAfter, mock.Anything, ListStageFetchDocIDsAfterParams{
		AfterID: 0,
		Limit:   2,
		Force:   true,
	}).Return([]int64(nil), nil).Once()

	env.ExecuteWorkflow(NormalizeAllWorkflow, StageAllParams{BatchSize: 2, MaxConcurrent: 1, Force: true})

	if !env.IsWorkflowCompleted() {
		t.Fatal("NormalizeAllWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("NormalizeAllWorkflow error: %v", err)
	}
	env.AssertExpectations(t)
}

func TestIndexAllWorkflowUsesNeedsIndexSelector(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var a *Activities
	env.OnActivity(a.ListFetchDocIDsNeedingIndexAfter, mock.Anything, ListStageFetchDocIDsAfterParams{
		AfterID: 0,
		Limit:   2,
	}).Return([]int64{30}, nil).Once()
	env.OnActivity(a.Index, mock.Anything, StageParams{FetchDocID: int64(30)}).
		Return(IndexResult{DocumentID: 300, ChunksWritten: 11}, nil).Once()

	env.ExecuteWorkflow(IndexAllWorkflow, StageAllParams{BatchSize: 2, MaxConcurrent: 1})

	if !env.IsWorkflowCompleted() {
		t.Fatal("IndexAllWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("IndexAllWorkflow error: %v", err)
	}
	var res IndexAllResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if res.Total != 1 || res.Completed != 1 || res.ChunksWritten != 11 {
		t.Fatalf("result = %+v, want one indexed doc with chunks", res)
	}
	env.AssertExpectations(t)
}

func TestIndexAllWorkflowPassesForceToSelector(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	var a *Activities
	env.OnActivity(a.ListFetchDocIDsNeedingIndexAfter, mock.Anything, ListStageFetchDocIDsAfterParams{
		AfterID: 0,
		Limit:   2,
		Force:   true,
	}).Return([]int64(nil), nil).Once()

	env.ExecuteWorkflow(IndexAllWorkflow, StageAllParams{BatchSize: 2, MaxConcurrent: 1, Force: true})

	if !env.IsWorkflowCompleted() {
		t.Fatal("IndexAllWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("IndexAllWorkflow error: %v", err)
	}
	env.AssertExpectations(t)
}
