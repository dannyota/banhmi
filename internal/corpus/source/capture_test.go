package source

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	quality "danny.vn/banhmi/internal/corpus/quality/api"
	"danny.vn/banhmi/internal/corpus/source/api"
	capturehttp "danny.vn/banhmi/internal/corpus/source/http"
)

type captureResolver struct {
	job          CaptureJob
	registered   bool
	outcome      bool
	outcomeError bool
}

func (r *captureResolver) Resolve(context.Context, api.RequestEnvelope) (CaptureJob, error) {
	return r.job, nil
}

func (r *captureResolver) RegisterStaged(_ context.Context, _ string, _ model.AssetRef, entries []model.ArtifactInventoryEntry) error {
	if len(entries) != 3 || entries[0].Capture.Asset.SHA256 == entries[1].Capture.Asset.SHA256 || entries[1].Capture.Asset.SHA256 == entries[2].Capture.Asset.SHA256 {
		return errors.New("incorrect object inventory")
	}
	r.registered = true
	return nil
}
func (r *captureResolver) RegisterOutcome(_ context.Context, _ string, receipt model.AssetRef, entry model.ArtifactInventoryEntry) error {
	if receipt.Validate() != nil || entry.Validate() != nil || entry.Capture.Asset != receipt {
		return errors.New("invalid outcome inventory")
	}
	r.outcome = true
	if r.outcomeError {
		return errors.New("outcome unavailable")
	}
	return nil
}

type captureNetwork struct {
	called  bool
	sent    bool
	fail    bool
	content string
	mutate  func(*capturehttp.Receipt)
}

func (n *captureNetwork) Fetch(ctx context.Context, p api.RequestPlan, _ []capturehttp.Host, authorize capturehttp.Authorize, w io.Writer) (capturehttp.Receipt, error) {
	n.called = true
	req, _ := stdhttp.NewRequestWithContext(ctx, p.Method, p.URL, nil)
	if err := authorize(ctx, req); err != nil {
		return capturehttp.Receipt{}, err
	}
	n.sent = true
	if n.fail {
		r := capturehttp.Receipt{RequestKey: p.RequestKey, Status: 503, FinalURL: p.URL, RetryAfter: "120", Outcome: capturehttp.Retryable, Reason: "source_status"}
		return r, &capturehttp.Failure{Key: p.RequestKey, Outcome: capturehttp.Retryable, Reason: "source_status", Receipt: &r}
	}
	content := n.content
	if content == "" {
		content = "body"
	}
	_, err := io.WriteString(w, content)
	r := capturehttp.Receipt{RequestKey: p.RequestKey, Status: 200, FinalURL: p.URL, MediaType: "text/plain", ByteLength: int64(len(content)), SHA256: digestBytes([]byte(content)), Outcome: capturehttp.Success, Started: time.Now(), Finished: time.Now()}
	if n.mutate != nil {
		n.mutate(&r)
	}
	return r, err
}

type residencyFake struct {
	called      bool
	allow       bool
	wrongHash   bool
	wrongPolicy bool
	hash        model.Digest
	policy      model.Digest
}

func (r *residencyFake) PrepareExport(_ context.Context, b []byte, p model.Digest, _ model.ResourceBudget) (quality.ResidencyRequestEnvelope, error) {
	r.hash, r.policy = model.Digest(digestBytes(b)), p
	return quality.ResidencyRequestEnvelope{Key: "screen-" + strings.Repeat("1", 32), Manifest: r.hash}, nil
}
func (r *residencyFake) Screen(context.Context, quality.ResidencyRequestEnvelope) (model.ResidencyEnvelope, error) {
	r.called = true
	return model.ResidencyEnvelope{Key: "residency-" + strings.Repeat("1", 32), Manifest: r.hash}, nil
}
func (r *residencyFake) ResolveAdmission(context.Context, model.ResidencyEnvelope) (model.ResidencyAdmission, error) {
	class := model.ResidencyPersonal
	if r.allow {
		class = model.ResidencyPublic
	}
	hash := r.hash
	if r.wrongHash {
		hash = model.Digest(strings.Repeat("0", 64))
	}
	policy := r.policy
	if r.wrongPolicy {
		policy = model.Digest(strings.Repeat("0", 64))
	}
	return model.ResidencyAdmission{ArtifactHash: hash, PolicyHash: policy, Class: class, ReasonCode: "test", Report: model.AssetRef{Path: "reports/test", SHA256: r.hash}}, nil
}

type captureObjects struct {
	blob.Store
	blob.Catalog
	puts int
	fail bool
}

func (s *captureObjects) Register(_ context.Context, m blob.Metadata) (blob.Metadata, error) {
	return m, nil
}

func (s *captureObjects) Put(_ context.Context, m blob.Metadata, r io.Reader) (blob.Object, error) {
	if s.fail {
		return blob.Object{}, errors.New("storage unavailable")
	}
	b, err := io.ReadAll(r)
	s.puts++
	return blob.Object{ObjectRef: m.ObjectRef, SHA256: digestBytes(b), ByteSize: int64(len(b))}, err
}

func TestResidencyBeforeEveryExportAndStagedInventory(t *testing.T) {
	for _, mode := range []string{"reject", "admit", "wrong-hash", "wrong-policy", "source-failure"} {
		allow := mode != "reject"
		network := &captureNetwork{fail: mode == "source-failure"}
		residency := &residencyFake{allow: allow, wrongHash: mode == "wrong-hash", wrongPolicy: mode == "wrong-policy"}
		objects := &captureObjects{}
		plan := api.RequestPlan{Country: "vn", SourceID: "test", RequestKey: "request-test", WindowKey: "window-test", Purpose: model.PurposeListing, URL: "https://source.test/file", Method: "GET", PolicyHash: model.Digest(strings.Repeat("a", 64)), BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxInputBytes: 16384, MaxItems: 10, MaxPages: 10, MaxConcurrent: 1, MaxElapsed: time.Second}}
		resolver := &captureResolver{job: CaptureJob{Plan: plan, PlanHosts: []capturehttp.Host{{Name: "source.test"}}, CaptureKey: "capture-" + strings.Repeat("1", 32)}}
		c := Capture{Requests: resolver, Network: network, Residency: residency, Screens: residency, Staging: blob.Stager{Store: objects, Catalog: objects, TempDir: t.TempDir()}}
		envelope, err := c.Capture(context.Background(), api.RequestEnvelope{Key: "request-" + strings.Repeat("1", 32), Manifest: model.Digest(strings.Repeat("a", 64))})
		if !residency.called {
			t.Fatal("residency screen was bypassed")
		}
		if !allow || mode == "wrong-hash" || mode == "wrong-policy" {
			var failure *capturehttp.Failure
			if !errors.As(err, &failure) || failure.Outcome != capturehttp.PolicyRejected || network.sent || resolver.registered || objects.puts != 0 {
				t.Fatalf("rejected export proceeded: %v", err)
			}
		} else if mode == "source-failure" {
			var failure *capturehttp.Failure
			if !errors.As(err, &failure) || failure.Receipt.RetryAfter != "120" || failure.Receipt.AttemptKey != "request-"+strings.Repeat("1", 32) || !resolver.outcome || resolver.registered || objects.puts != 1 {
				t.Fatalf("failure receipt was not staged: %v puts=%d", err, objects.puts)
			}
		} else if err != nil || envelope.Validate() != nil || !network.sent || !resolver.registered || objects.puts != 3 {
			t.Fatalf("capture=%+v error=%v objects=%d", envelope, err, objects.puts)
		}
	}
}

func captureFixture(t *testing.T, network NetworkCapture, limit int64) (*Capture, *captureResolver, *captureObjects) {
	t.Helper()
	plan := api.RequestPlan{Country: "vn", SourceID: "test", RequestKey: "request-test", WindowKey: "window-test", Purpose: model.PurposeListing, URL: "https://source.test/file", Method: "GET", PolicyHash: model.Digest(strings.Repeat("a", 64)), BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxInputBytes: limit, MaxItems: 10, MaxPages: 100, MaxConcurrent: 1, MaxElapsed: time.Second}}
	resolver := &captureResolver{job: CaptureJob{Plan: plan, PlanHosts: []capturehttp.Host{{Name: "source.test"}}, CaptureKey: "capture-" + strings.Repeat("1", 32)}}
	residency := &residencyFake{allow: true}
	objects := &captureObjects{}
	return &Capture{Requests: resolver, Network: network, Residency: residency, Screens: residency, Staging: blob.Stager{Store: objects, Catalog: objects, TempDir: t.TempDir()}}, resolver, objects
}

func captureTestEnvelope() api.RequestEnvelope {
	return api.RequestEnvelope{Key: "request-" + strings.Repeat("1", 32), Manifest: model.Digest(strings.Repeat("a", 64))}
}

func TestReceiptMismatchesStopContractWork(t *testing.T) {
	for name, mutate := range map[string]func(*capturehttp.Receipt){
		"hash":        func(r *capturehttp.Receipt) { r.SHA256 = strings.Repeat("0", 64) },
		"length":      func(r *capturehttp.Receipt) { r.ByteLength++ },
		"request-key": func(r *capturehttp.Receipt) { r.RequestKey = "request-other" },
		"truncated":   func(r *capturehttp.Receipt) { r.Truncated = true },
	} {
		t.Run(name, func(t *testing.T) {
			c, resolver, _ := captureFixture(t, &captureNetwork{mutate: mutate}, 4096)
			_, err := c.Capture(context.Background(), captureTestEnvelope())
			var failure *capturehttp.Failure
			if !errors.As(err, &failure) || failure.Outcome != capturehttp.PolicyRejected || failure.Reason != "capture_receipt_mismatch" || failure.Receipt == nil || resolver.registered {
				t.Fatalf("contract mismatch retried or registered: %v", err)
			}
		})
	}
}

type redirectFailureNetwork struct{}

func (redirectFailureNetwork) Fetch(_ context.Context, p api.RequestPlan, _ []capturehttp.Host, _ capturehttp.Authorize, _ io.Writer) (capturehttp.Receipt, error) {
	r := capturehttp.Receipt{RequestKey: p.RequestKey, Started: time.Now(), Finished: time.Now(), FinalURL: p.URL, Status: 302, Outcome: capturehttp.PolicyRejected, Reason: "redirect_limit"}
	for i := 0; i <= capturehttp.MaxRedirects; i++ {
		r.Redirects = append(r.Redirects, capturehttp.Hop{From: p.URL + strings.Repeat("a", 1500), To: p.URL + strings.Repeat("b", 1500), Status: 302})
	}
	return r, &capturehttp.Failure{Outcome: r.Outcome, Reason: r.Reason, Receipt: &r}
}

func TestReceiptAndManifestHaveIndependentOutputAllowances(t *testing.T) {
	c, resolver, objects := captureFixture(t, &captureNetwork{content: "x"}, 256)
	envelope, err := c.Capture(context.Background(), captureTestEnvelope())
	if err != nil || envelope.Validate() != nil || !resolver.registered || objects.puts != 3 {
		t.Fatalf("small body blocked provenance: %v puts=%d", err, objects.puts)
	}
	c, resolver, objects = captureFixture(t, redirectFailureNetwork{}, 4096)
	_, err = c.Capture(context.Background(), captureTestEnvelope())
	var failure *capturehttp.Failure
	if !errors.As(err, &failure) || failure.Reason != "redirect_limit" || failure.Receipt == nil || len(failure.Receipt.Redirects) != 4 || !resolver.outcome || objects.puts != 1 {
		t.Fatalf("large failure receipt lost: %v puts=%d", err, objects.puts)
	}
}

func TestOutcomePersistenceFailurePreservesOriginalReceipt(t *testing.T) {
	for _, mode := range []string{"stage", "register"} {
		t.Run(mode, func(t *testing.T) {
			c, resolver, objects := captureFixture(t, redirectFailureNetwork{}, 4096)
			objects.fail = mode == "stage"
			resolver.outcomeError = mode == "register"
			_, err := c.Capture(context.Background(), captureTestEnvelope())
			var failure *capturehttp.Failure
			if !errors.As(err, &failure) || failure.Outcome != capturehttp.PolicyRejected || failure.Reason != "redirect_limit" || failure.Receipt == nil || len(failure.Receipt.Redirects) != 4 || failure.Receipt.AttemptKey != captureTestEnvelope().Key || resolver.registered {
				t.Fatalf("outcome replaced after persistence failure: %v", err)
			}
		})
	}
}

func TestProvenanceOutputBoundsCoverAdmittedHops(t *testing.T) {
	locator := "https://source.test/" + strings.Repeat("&", capturehttp.MaxURLBytes-len("https://source.test/"))
	r := capturehttp.Receipt{RequestKey: "request-test", AttemptKey: captureTestEnvelope().Key, FinalURL: locator, MediaType: strings.Repeat("&", capturehttp.MaxReceiptFieldBytes), RetryAfter: strings.Repeat("&", capturehttp.MaxReceiptFieldBytes), Started: time.Now(), Finished: time.Now(), Outcome: capturehttp.PolicyRejected, Reason: "redirect_limit"}
	for i := 0; i <= capturehttp.MaxRedirects; i++ {
		r.Redirects = append(r.Redirects, capturehttp.Hop{From: locator, To: locator, Status: 302})
	}
	for i := 0; i < (capturehttp.MaxRedirects+1)*capturehttp.MaxIntermediateDepth; i++ {
		intermediate := r
		intermediate.Intermediates = nil
		r.Intermediates = append(r.Intermediates, intermediate)
	}
	encoded, err := json.Marshal(r)
	if err != nil || int64(len(encoded)) > receiptOutputLimit {
		t.Fatalf("admitted receipt exceeds output bound: bytes=%d limit=%d err=%v", len(encoded), receiptOutputLimit, err)
	}
	ref := model.CaptureRef{Country: "vn", SourceID: "test", CaptureKey: "capture-" + strings.Repeat("1", 32), Asset: model.AssetRef{Path: "sha256/" + strings.Repeat("a", 64), SHA256: model.Digest(strings.Repeat("a", 64)), ByteSize: 1}, MediaType: "text/plain", RequestHash: model.Digest(strings.Repeat("a", 64)), ResponseHash: model.Digest(strings.Repeat("a", 64))}
	ref.TransportReceipt = ref.Asset
	manifest := CaptureManifest{Capture: ref, Receipt: r, Artifacts: []model.ArtifactInventoryEntry{
		{ArtifactKey: ref.CaptureKey + "-body", Role: "captured_response", URL: locator, Capture: &ref},
		{ArtifactKey: ref.CaptureKey + "-receipt", Role: "transport_receipt", URL: locator, Capture: &ref, Ordinal: 1},
	}}
	encoded, err = json.Marshal(manifest)
	if err != nil || int64(len(encoded)) > manifestOutputLimit {
		t.Fatalf("admitted manifest exceeds output bound: bytes=%d limit=%d err=%v", len(encoded), manifestOutputLimit, err)
	}
}
