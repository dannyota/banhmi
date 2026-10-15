package run

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/model"
)

func admissionTestWork(kind WorkflowKind) IntakeWork {
	e := ScopedExecution{Country: "vn", WorkflowKind: kind, AttemptKey: "attempt-a", Owner: "owner-a", AdmissionEpoch: 1, AttemptFence: 1}
	switch kind {
	case DocumentIngest:
		e.WorkflowID = "document-ingest/vn/version-a/attempt-a"
		e.Scope = ExecutionScope{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectDocumentVersion, DocumentVersion: "version-a"}}
	case SourceSync:
		e.WorkflowID = "source-sync/vn/source-a/window-a"
		e.Scope = ExecutionScope{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectSourceWindow, SourceWindow: "window-a"}}
	case Reprocess:
		e.WorkflowID = "reprocess/vn/selection-a"
		e.Scope = ExecutionScope{Kind: ScopeIntake, Subject: IntakeSubject{Kind: SubjectReprocessSelection, ReprocessSelection: "selection-a"}}
	case ControlReconcile:
		e.WorkflowID = "corpus-control/vn"
		e.Scope = ExecutionScope{Kind: ScopeControl, CountryControl: "vn"}
	}
	digest := model.Digest(strings.Repeat("a", 64))
	return IntakeWork{Execution: e, Stage: Extract, Substep: "convert", Shard: "shard-a", InputDigest: digest, RecipeDigest: digest, BudgetDigest: digest,
		Budget: Budget{MaxItems: 1000, MaxInputBytes: 1024, MaxPages: 1, MaxElapsed: time.Second, MaxConcurrent: 1}}
}

func TestDocumentAdmissionLimits(t *testing.T) {
	want := DefaultDocumentAdmissionLimits()
	if want.Processing != 32 || want.Open != 256 || want.ReceiptJSONBytes != 65536 || want.QuarantineExpiry != 7*24*time.Hour {
		t.Fatal("defaults changed")
	}
	for _, mutate := range []func(*DocumentAdmissionLimits){
		func(l *DocumentAdmissionLimits) { l.Processing = 0 },
		func(l *DocumentAdmissionLimits) { l.Processing = -1 },
		func(l *DocumentAdmissionLimits) { l.Open = 31 },
		func(l *DocumentAdmissionLimits) { l.ReceiptJSONBytes = 0 },
		func(l *DocumentAdmissionLimits) { l.ReceiptJSONBytes = -1 },
		func(l *DocumentAdmissionLimits) { l.QuarantineExpiry = 0 },
		func(l *DocumentAdmissionLimits) { l.QuarantineExpiry = -time.Microsecond },
		func(l *DocumentAdmissionLimits) { l.QuarantineExpiry = time.Microsecond + 1 },
	} {
		l := want
		mutate(&l)
		if !errors.Is(l.Validate(), ErrInvalidAdmission) {
			t.Fatalf("invalid bound accepted: %+v", l)
		}
	}
	for _, l := range []DocumentAdmissionLimits{want, {Processing: math.MaxInt32, Open: math.MaxInt32, ReceiptJSONBytes: math.MaxInt32, QuarantineExpiry: time.Duration(math.MaxInt64 / 1000 * 1000)}} {
		if err := l.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAdmissionReserveIdentityAndReprocessRefusal(t *testing.T) {
	r := AdmissionReserveRequest{Actor: admissionTestWork(SourceSync), Child: admissionTestWork(DocumentIngest), DeliveryKey: "request-00000000000000000000000000000001"}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*AdmissionReserveRequest){
		func(r *AdmissionReserveRequest) { r.Actor = admissionTestWork(Reprocess) },
		func(r *AdmissionReserveRequest) { r.Child.Execution.WorkflowID = "document-ingest/vn/source-a/version-a" },
		func(r *AdmissionReserveRequest) { r.Child.Execution.AttemptKey = "attempt-b" },
		func(r *AdmissionReserveRequest) { r.Child.Execution.Scope.Subject.DocumentVersion = "version-b" },
		func(r *AdmissionReserveRequest) { r.DeliveryKey = "" },
	} {
		bad := r
		mutate(&bad)
		if !errors.Is(bad.Validate(), ErrInvalidAdmission) {
			t.Fatal("invalid reserve accepted")
		}
	}
}

func TestAdmissionResolutionSelectors(t *testing.T) {
	child := admissionTestWork(DocumentIngest)
	r := AdmissionResolveRequest{Actor: child, DocumentWork: &child}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.DocumentWork, r.WorkflowID = nil, child.Execution.WorkflowID
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "document-ingest/vn/source-a/version-a/attempt-a", "document-ingest/my/version-a/attempt-a", "document-ingest/vn/version-a/"} {
		r.WorkflowID = id
		if !errors.Is(r.Validate(), ErrInvalidAdmission) {
			t.Fatal("invalid selector accepted")
		}
	}
	r.DocumentWork, r.WorkflowID = &child, child.Execution.WorkflowID
	if !errors.Is(r.Validate(), ErrInvalidAdmission) {
		t.Fatal("two selectors accepted")
	}
}

func TestAdmissionChangeCloseAndScanValidation(t *testing.T) {
	c := AdmissionChange{Actor: admissionTestWork(DocumentIngest), SlotKey: "slot-00000000000000000000000000000001", ExpectedRevision: 1}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []AdmissionTerminalReason{AdmissionCancelled, AdmissionAbandoned} {
		if err := (AdmissionCloseRequest{Change: c, Reason: reason}).Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, reason := range []AdmissionTerminalReason{AdmissionCompleted, AdmissionExecutionFailed, AdmissionExpired, AdmissionTerminalGap} {
		if !errors.Is((AdmissionCloseRequest{Change: c, Reason: reason}).Validate(), ErrInvalidAdmission) {
			t.Fatal("unproved lifecycle cause accepted")
		}
	}
	scan := AdmissionScan{Actor: admissionTestWork(ControlReconcile), Limit: 1000}
	if err := scan.Validate(); err != nil {
		t.Fatal(err)
	}
	scan.AfterID = 1
	if !errors.Is(scan.Validate(), ErrInvalidAdmission) {
		t.Fatal("unpaired cursor accepted")
	}
}

func TestAdmissionClosedVocabularies(t *testing.T) {
	for _, s := range []AdmissionState{AdmissionReserved, AdmissionProcessing, AdmissionOpenOnly, AdmissionClosing, AdmissionReleased} {
		if !s.Valid() {
			t.Fatal(s)
		}
	}
	for _, r := range []AdmissionRefusal{AdmissionProcessingFull, AdmissionOpenFull, AdmissionBothFull, AdmissionResourceBusy, AdmissionNotTurn} {
		if !r.Valid() {
			t.Fatal(r)
		}
	}
	for _, r := range []AdmissionTerminalReason{AdmissionCompleted, AdmissionTerminalGap, AdmissionCancelled, AdmissionExpired, AdmissionAbandoned, AdmissionExecutionFailed} {
		if !r.Valid() {
			t.Fatal(r)
		}
	}
	if AdmissionState("").Valid() || AdmissionRefusal("other").Valid() || AdmissionTerminalReason("other").Valid() || DocumentExecutionStatus("other").Valid() {
		t.Fatal("open vocabulary")
	}
}
