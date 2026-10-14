package source

import (
	"context"
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

type captureResolver struct { job CaptureJob; registered bool; outcome bool }
func (r *captureResolver) Resolve(context.Context, api.RequestEnvelope) (CaptureJob, error) { return r.job, nil }
func (r *captureResolver) RegisterStaged(_ context.Context, _ string, _ model.AssetRef, entries []model.ArtifactInventoryEntry) error {
	if len(entries) != 3 || entries[0].Capture.Asset.SHA256 == entries[1].Capture.Asset.SHA256 || entries[1].Capture.Asset.SHA256 == entries[2].Capture.Asset.SHA256 { return errors.New("incorrect object inventory") }
	r.registered = true
	return nil
}
func (r *captureResolver) RegisterOutcome(_ context.Context, _ string, receipt model.AssetRef, entry model.ArtifactInventoryEntry) error {
	if receipt.Validate() != nil || entry.Validate() != nil || entry.Capture.Asset != receipt { return errors.New("invalid outcome inventory") }
	r.outcome = true
	return nil
}

type captureNetwork struct { called bool; sent bool; fail bool }
func (n *captureNetwork) Fetch(ctx context.Context, p api.RequestPlan, _ []capturehttp.Host, authorize capturehttp.Authorize, w io.Writer) (capturehttp.Receipt, error) {
	n.called = true
	req, _ := stdhttp.NewRequestWithContext(ctx, p.Method, p.URL, nil)
	if err := authorize(ctx, req); err != nil { return capturehttp.Receipt{}, err }
	n.sent = true
	if n.fail {
		r := capturehttp.Receipt{RequestKey: p.RequestKey, Status: 503, FinalURL: p.URL, RetryAfter: "120", Outcome: capturehttp.Retryable, Reason: "source_status"}
		return r, &capturehttp.Failure{Key: p.RequestKey, Outcome: capturehttp.Retryable, Reason: "source_status", Receipt: &r}
	}
	_, err := io.WriteString(w, "body")
	return capturehttp.Receipt{RequestKey: p.RequestKey, Status: 200, FinalURL: p.URL, MediaType: "text/plain", ByteLength: 4, SHA256: digestBytes([]byte("body")), Outcome: capturehttp.Success, Started: time.Now(), Finished: time.Now()}, err
}

type residencyFake struct { called bool; allow bool; wrongHash bool; hash model.Digest; policy model.Digest }
func (r *residencyFake) PrepareExport(_ context.Context, b []byte, p model.Digest, _ model.ResourceBudget) (quality.ResidencyRequestEnvelope, error) {
	r.hash, r.policy = model.Digest(digestBytes(b)), p
	return quality.ResidencyRequestEnvelope{Key: "screen-"+strings.Repeat("1", 32), Manifest: r.hash}, nil
}
func (r *residencyFake) Screen(context.Context, quality.ResidencyRequestEnvelope) (model.ResidencyEnvelope, error) {
	r.called = true
	return model.ResidencyEnvelope{Key: "residency-"+strings.Repeat("1", 32), Manifest: r.hash}, nil
}
func (r *residencyFake) ResolveAdmission(context.Context, model.ResidencyEnvelope) (model.ResidencyAdmission, error) {
	class := model.ResidencyPersonal
	if r.allow { class = model.ResidencyPublic }
	hash := r.hash
	if r.wrongHash { hash = model.Digest(strings.Repeat("0", 64)) }
	return model.ResidencyAdmission{ArtifactHash: hash, PolicyHash: r.policy, Class: class, ReasonCode: "test", Report: model.AssetRef{Path: "reports/test", SHA256: r.hash}}, nil
}

type captureObjects struct { blob.Store; blob.Catalog; puts int }
func (s *captureObjects) Register(_ context.Context, m blob.Metadata) (blob.Metadata, error) { return m, nil }
func (s *captureObjects) Put(_ context.Context, m blob.Metadata, r io.Reader) (blob.Object, error) {
	b, err := io.ReadAll(r); s.puts++
	return blob.Object{ObjectRef: m.ObjectRef, SHA256: digestBytes(b), ByteSize: int64(len(b))}, err
}

func TestResidencyBeforeEveryExportAndStagedInventory(t *testing.T) {
	for _, mode := range []string{"reject", "admit", "wrong-hash", "source-failure"} {
		allow := mode != "reject"
		network := &captureNetwork{fail: mode == "source-failure"}
		residency := &residencyFake{allow: allow, wrongHash: mode == "wrong-hash"}
		objects := &captureObjects{}
		plan := api.RequestPlan{Country: "vn", SourceID: "test", RequestKey: "request-test", WindowKey: "window-test", Purpose: model.PurposeListing, URL: "https://source.test/file", Method: "GET", PolicyHash: model.Digest(strings.Repeat("a", 64)), BudgetHash: model.Digest(strings.Repeat("b", 64)), Budget: model.ResourceBudget{MaxInputBytes: 16384, MaxItems: 10, MaxPages: 10, MaxConcurrent: 1, MaxElapsed: time.Second}}
		resolver := &captureResolver{job: CaptureJob{Plan: plan, PlanHosts: []capturehttp.Host{{Name: "source.test"}}, CaptureKey: "capture-"+strings.Repeat("1", 32)}}
		c := Capture{Requests: resolver, Network: network, Residency: residency, Screens: residency, Staging: blob.Stager{Store: objects, Catalog: objects, TempDir: t.TempDir()}}
		envelope, err := c.Capture(context.Background(), api.RequestEnvelope{Key: "request-"+strings.Repeat("1", 32), Manifest: model.Digest(strings.Repeat("a", 64))})
		if !residency.called { t.Fatal("residency screen was bypassed") }
		if !allow || mode == "wrong-hash" {
			var failure *capturehttp.Failure
			if !errors.As(err, &failure) || failure.Outcome != capturehttp.PolicyRejected || network.sent || resolver.registered || objects.puts != 0 { t.Fatalf("rejected export proceeded: %v", err) }
		} else if mode == "source-failure" {
			var failure *capturehttp.Failure
			if !errors.As(err, &failure) || failure.Receipt.RetryAfter != "120" || failure.Receipt.AttemptKey != "request-"+strings.Repeat("1", 32) || !resolver.outcome || resolver.registered || objects.puts != 1 { t.Fatalf("failure receipt was not staged: %v puts=%d", err, objects.puts) }
		} else if err != nil || envelope.Validate() != nil || !network.sent || !resolver.registered || objects.puts != 3 { t.Fatalf("capture=%+v error=%v objects=%d", envelope, err, objects.puts) }
	}
}
