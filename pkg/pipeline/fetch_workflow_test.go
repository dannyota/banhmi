package pipeline

import (
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"
)

func TestFetchWorkflowCountsCompletedDocWithoutStartingExtract(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	art := ClaimedArtifact{ID: 1, FetchDocID: 42, Kind: "body"}
	var a *Activities
	env.OnActivity(a.ClaimArtifacts, mock.Anything, ClaimParams{Source: "vbpl", Limit: claimBatch}).
		Return([]ClaimedArtifact{art}, nil).
		Once()
	env.OnActivity(a.PlanBody, mock.Anything, art).
		Return("complete", nil).
		Once()
	env.OnActivity(a.ClaimArtifacts, mock.Anything, ClaimParams{Source: "vbpl", Limit: claimBatch}).
		Return([]ClaimedArtifact(nil), nil).
		Once()

	env.ExecuteWorkflow(FetchWorkflow, FetchParams{Source: "vbpl"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("FetchWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("FetchWorkflow error: %v", err)
	}
	var res FetchResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if res.DocsCompleted != 1 {
		t.Fatalf("DocsCompleted = %d, want 1", res.DocsCompleted)
	}
	env.AssertExpectations(t)
}

func TestFetchWorkflowDispatchesAllArtifactKinds(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()

	body := ClaimedArtifact{ID: 1, FetchDocID: 10, Kind: "body"}
	tree := ClaimedArtifact{ID: 2, FetchDocID: 10, Kind: "tree"}
	file := ClaimedArtifact{ID: 3, FetchDocID: 10, Kind: "file"}
	var a *Activities
	env.OnActivity(a.ClaimArtifacts, mock.Anything, ClaimParams{Source: "vbpl", Limit: claimBatch}).
		Return([]ClaimedArtifact{body, tree, file}, nil).
		Once()
	env.OnActivity(a.PlanBody, mock.Anything, body).
		Return("fetching", nil).
		Once()
	env.OnActivity(a.FetchTree, mock.Anything, tree).
		Return("fetching", nil).
		Once()
	env.OnActivity(a.FetchFile, mock.Anything, file).
		Return("fetching", nil).
		Once()
	env.OnActivity(a.ClaimArtifacts, mock.Anything, ClaimParams{Source: "vbpl", Limit: claimBatch}).
		Return([]ClaimedArtifact(nil), nil).
		Once()

	env.ExecuteWorkflow(FetchWorkflow, FetchParams{Source: "vbpl"})

	if !env.IsWorkflowCompleted() {
		t.Fatal("FetchWorkflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("FetchWorkflow error: %v", err)
	}
	var res FetchResult
	if err := env.GetWorkflowResult(&res); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	if res.Bodies != 1 || res.Trees != 1 || res.Files != 1 || res.Claimed != 3 {
		t.Fatalf("result = %+v, want one body, tree, file and three claimed", res)
	}
	env.AssertExpectations(t)
}
