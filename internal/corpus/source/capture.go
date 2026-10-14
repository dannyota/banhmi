// Package source composes network capture and in-country blob staging.
package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"time"

	"danny.vn/banhmi/internal/corpus/blob"
	"danny.vn/banhmi/internal/corpus/model"
	quality "danny.vn/banhmi/internal/corpus/quality/api"
	"danny.vn/banhmi/internal/corpus/source/api"
	capturehttp "danny.vn/banhmi/internal/corpus/source/http"
)

// CaptureJob is a verified local request resolved by the owning ledger. Its
// resolver verifies exact manifest bytes, issuance, policy and remaining budgets.
// All declared hosts, including CA hosts, need caller-owned host claims.
type CaptureJob struct {
	Plan api.RequestPlan
	PlanHosts []capturehttp.Host
	CaptureKey string
}

// CaptureRequests resolves immutable manifests and registers unaccepted captures.
// RegisterStaged never accepts a revision, adds evidence references, advances a
// cursor, settles a claim or releases a host slot. Those mutations belong to M1.3.
type CaptureRequests interface {
	// Resolve verifies ledger issuance and frozen manifest bytes before returning a job.
	Resolve(context.Context, api.RequestEnvelope) (CaptureJob, error)
	// RegisterStaged associates a manifest and all object inventory without acceptance.
	RegisterStaged(context.Context, string, model.AssetRef, []model.ArtifactInventoryEntry) error
	// RegisterOutcome records an unaccepted attempt receipt, without settling claims.
	RegisterOutcome(context.Context, string, model.AssetRef, model.ArtifactInventoryEntry) error
}

// NetworkCapture is the bounded transport seam used by capture composition.
type NetworkCapture interface {
	// Fetch captures once under the caller's claims and screens every outbound hop.
	Fetch(context.Context, api.RequestPlan, []capturehttp.Host, capturehttp.Authorize, io.Writer) (capturehttp.Receipt, error)
}

// ExportScreens stages canonical outbound request bytes inside Vietnam and
// resolves the frozen residency port's result against verified manifest bytes.
// PrepareExport binds the supplied bytes, policy and frozen budget exactly.
type ExportScreens interface {
	// PrepareExport stages bounded request bytes for the in-country screen port.
	PrepareExport(context.Context, []byte, model.Digest, model.ResourceBudget) (quality.ResidencyRequestEnvelope, error)
	// ResolveAdmission verifies the returned immutable residency manifest.
	ResolveAdmission(context.Context, model.ResidencyEnvelope) (model.ResidencyAdmission, error)
}

// Capture implements the frozen Fetcher port. Staging and screen ports must stay
// in-country; no external artifact storage or batch processing runs here.
// Residency screens every outbound network request, including redirects and AIA.
type Capture struct {
	Requests CaptureRequests
	Network NetworkCapture
	Residency quality.ResidencyScreen
	Screens ExportScreens
	Staging blob.Stager
}

// receiptOutputLimit reserves escaped URLs for every bounded source and AIA hop,
// plus fixed receipt fields and bounded response headers. Manifest inventory adds
// fixed references and repeated final URLs. Neither limit depends on body bytes.
const receiptOutputLimit int64 = (1 + (capturehttp.MaxRedirects+1)*capturehttp.MaxIntermediateDepth) * ((1+2*(capturehttp.MaxRedirects+1))*capturehttp.MaxURLBytes*6 + 2*capturehttp.MaxReceiptFieldBytes*6 + 16384)
const manifestOutputLimit int64 = receiptOutputLimit + 131072

var _ api.Fetcher = (*Capture)(nil)

// CaptureManifest is local immutable evidence with one inventory entry per
// captured body and transport receipt. The manifest itself is an infrastructure
// object referenced by CaptureEnvelope, never an additional source artifact.
type CaptureManifest struct {
	Capture model.CaptureRef
	Receipt capturehttp.Receipt
	Artifacts []model.ArtifactInventoryEntry
}

// Capture executes once and returns only the staged manifest projection. It does
// not decode, convert, accept revisions or perform source/infrastructure retries.
func (c *Capture) Capture(ctx context.Context, envelope api.RequestEnvelope) (model.CaptureEnvelope, error) {
	fail := func(outcome capturehttp.Outcome, reason string) (model.CaptureEnvelope, error) {
		return model.CaptureEnvelope{}, &capturehttp.Failure{Key: envelope.Key, Outcome: outcome, Reason: reason}
	}
	if err := envelope.Validate(); err != nil {
		return model.CaptureEnvelope{}, &capturehttp.Failure{Outcome: capturehttp.PolicyRejected, Reason: "invalid_envelope"}
	}
	if c.Requests == nil || c.Network == nil || c.Residency == nil || c.Screens == nil { return fail(capturehttp.Infrastructure, "capture_configuration") }
	job, err := c.Requests.Resolve(ctx, envelope)
	if err != nil { return fail(capturehttp.Infrastructure, "request_manifest_unavailable") }
	if job.Plan.Validate() != nil || model.ValidateEnvelopeKey(job.CaptureKey, "capture") != nil { return fail(capturehttp.PolicyRejected, "invalid_resolved_request") }
	ctx, cancel := context.WithTimeout(ctx, job.Plan.Budget.MaxElapsed)
	defer cancel()
	authorize := func(ctx context.Context, req *stdhttp.Request) error {
		outbound, err := capturehttp.ExportBytes(req, job.Plan.Budget.MaxInputBytes)
		if err != nil { return err }
		screen, err := c.Screens.PrepareExport(ctx, outbound, job.Plan.PolicyHash, job.Plan.Budget)
		if err != nil || screen.Validate() != nil { return &capturehttp.Failure{Outcome: capturehttp.Infrastructure, Reason: "screen_manifest_unavailable"} }
		result, err := c.Residency.Screen(ctx, screen)
		if err != nil || result.Validate() != nil { return &capturehttp.Failure{Outcome: capturehttp.Infrastructure, Reason: "residency_screen_failed"} }
		admission, err := c.Screens.ResolveAdmission(ctx, result)
		if err != nil { return &capturehttp.Failure{Outcome: capturehttp.Infrastructure, Reason: "residency_receipt_unavailable"} }
		if admission.Validate() != nil || admission.ArtifactHash != model.Digest(digestBytes(outbound)) || admission.PolicyHash != job.Plan.PolicyHash || !admission.Admitted() {
			return &capturehttp.Failure{Outcome: capturehttp.PolicyRejected, Reason: "residency_rejected"}
		}
		return nil
	}
	meta := blob.Metadata{MediaType: "application/octet-stream", CaptureSource: job.Plan.SourceID, CaptureRef: job.CaptureKey, CapturedAt: time.Now().UTC()}
	var receipt capturehttp.Receipt
	var transportErr error
	body, err := c.Staging.Stage(ctx, meta, job.Plan.Budget.MaxInputBytes, func(w io.Writer) error {
		receipt, transportErr = c.Network.Fetch(ctx, job.Plan, job.PlanHosts, authorize, w)
		bindAttempt(&receipt, envelope.Key)
		var failure *capturehttp.Failure
		if errors.As(transportErr, &failure) {
			transportErr = &capturehttp.Failure{Key: envelope.Key, Outcome: failure.Outcome, Reason: failure.Reason, Receipt: &receipt}
		}
		return transportErr
	})
	if transportErr != nil {
		// Cancellation cannot authorize detached writes. The local Failure still
		// carries its receipt so the ledger can preserve it during reconciliation.
		if receipt.RequestKey != "" && ctx.Err() == nil {
			encoded, marshalErr := json.Marshal(receipt)
			if marshalErr != nil { return model.CaptureEnvelope{}, transportErr }
			meta.MediaType = "application/json"
			staged, stageErr := c.stageBytes(ctx, meta, receiptOutputLimit, encoded)
			if stageErr != nil { return model.CaptureEnvelope{}, transportErr }
			outcomeRef := model.CaptureRef{Country: job.Plan.Country, SourceID: job.Plan.SourceID, CaptureKey: job.CaptureKey, Asset: asset(staged), MediaType: "application/json", RequestHash: envelope.Manifest, ResponseHash: model.Digest(staged.SHA256), TransportReceipt: asset(staged)}
			entry := model.ArtifactInventoryEntry{ArtifactKey: job.CaptureKey+"-outcome", Role: "outcome_receipt", URL: job.Plan.URL, Capture: &outcomeRef}
			if entry.Validate() != nil { return model.CaptureEnvelope{}, transportErr }
			if c.Requests.RegisterOutcome(ctx, envelope.Key, asset(staged), entry) != nil { return model.CaptureEnvelope{}, transportErr }
		}
		var failure *capturehttp.Failure
		if errors.As(transportErr, &failure) { return model.CaptureEnvelope{}, transportErr }
		return fail(capturehttp.Infrastructure, "transport_port_failed")
	}
	if err != nil { return fail(capturehttp.Infrastructure, "body_staging_failed") }
	if receipt.Outcome != capturehttp.Success || receipt.Truncated || receipt.RequestKey != job.Plan.RequestKey || receipt.Status < 200 || receipt.Status >= 300 || receipt.Started.IsZero() || receipt.Finished.Before(receipt.Started) || receipt.SHA256 != body.SHA256 || receipt.ByteLength != body.ByteSize { return model.CaptureEnvelope{}, &capturehttp.Failure{Key: envelope.Key, Outcome: capturehttp.PolicyRejected, Reason: "capture_receipt_mismatch", Receipt: &receipt} }
	receiptBytes, err := json.Marshal(receipt)
	if err != nil { return fail(capturehttp.Infrastructure, "receipt_serialization") }
	meta.MediaType = "application/json"
	stagedReceipt, err := c.stageBytes(ctx, meta, receiptOutputLimit, receiptBytes)
	if err != nil { return fail(capturehttp.Infrastructure, "receipt_staging_failed") }
	ref := model.CaptureRef{Country: job.Plan.Country, SourceID: job.Plan.SourceID, CaptureKey: job.CaptureKey, Asset: asset(body), MediaType: receipt.MediaType, RequestHash: envelope.Manifest, ResponseHash: model.Digest(body.SHA256), TransportReceipt: asset(stagedReceipt)}
	if ref.Validate() != nil { return fail(capturehttp.Infrastructure, "invalid_capture_reference") }
	receiptRef := ref
	receiptRef.Asset, receiptRef.ResponseHash, receiptRef.MediaType = asset(stagedReceipt), model.Digest(stagedReceipt.SHA256), "application/json"
	manifest := CaptureManifest{Capture: ref, Receipt: receipt, Artifacts: []model.ArtifactInventoryEntry{
		{ArtifactKey: job.CaptureKey+"-body", Role: "captured_response", URL: receipt.FinalURL, Capture: &ref},
		{ArtifactKey: job.CaptureKey+"-receipt", Role: "transport_receipt", URL: receipt.FinalURL, Ordinal: 1, Capture: &receiptRef},
	}}
	for _, entry := range manifest.Artifacts { if entry.Validate() != nil { return fail(capturehttp.Infrastructure, "invalid_inventory") } }
	manifestBytes, err := json.Marshal(manifest)
	if err != nil { return fail(capturehttp.Infrastructure, "manifest_serialization") }
	stagedManifest, err := c.stageBytes(ctx, meta, manifestOutputLimit, manifestBytes)
	if err != nil { return fail(capturehttp.Infrastructure, "manifest_staging_failed") }
	manifestRef := ref
	manifestRef.Asset, manifestRef.ResponseHash, manifestRef.MediaType = asset(stagedManifest), model.Digest(stagedManifest.SHA256), "application/json"
	inventory := append(manifest.Artifacts, model.ArtifactInventoryEntry{ArtifactKey: job.CaptureKey+"-manifest", Role: "capture_manifest", URL: receipt.FinalURL, Ordinal: 2, Capture: &manifestRef})
	if inventory[2].Validate() != nil { return fail(capturehttp.Infrastructure, "invalid_manifest_inventory") }
	if err := c.Requests.RegisterStaged(ctx, job.CaptureKey, asset(stagedManifest), inventory); err != nil { return fail(capturehttp.Infrastructure, "capture_registration_failed") }
	result := model.CaptureEnvelope{Key: job.CaptureKey, Manifest: model.Digest(stagedManifest.SHA256)}
	return result, nil
}

func (c *Capture) stageBytes(ctx context.Context, meta blob.Metadata, limit int64, content []byte) (blob.Metadata, error) {
	return c.Staging.Stage(ctx, meta, limit, func(w io.Writer) error { _, err := io.Copy(w, bytes.NewReader(content)); return err })
}

func asset(m blob.Metadata) model.AssetRef { return model.AssetRef{Path: m.ObjectRef, SHA256: model.Digest(m.SHA256), ByteSize: m.ByteSize} }

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func bindAttempt(receipt *capturehttp.Receipt, key string) {
	receipt.AttemptKey = key
	for i := range receipt.Intermediates { bindAttempt(&receipt.Intermediates[i], key) }
}

