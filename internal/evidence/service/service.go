// Package service applies transport-neutral admission and release selection to
// evidence operations.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"danny.vn/banhmi/internal/evidence/api"
	"danny.vn/banhmi/pkg/base/config"
)

// InvalidRequestError reports input rejected before retrieval or database work.
type InvalidRequestError struct{ Message string }

func (e *InvalidRequestError) Error() string { return e.Message }

// ReleaseUnavailableError reports a requested release that cannot be pinned.
type ReleaseUnavailableError struct {
	Country   string
	ReleaseID string
}

func (e *ReleaseUnavailableError) Error() string {
	return fmt.Sprintf("evidence release %q is unavailable for country %q", e.ReleaseID, e.Country)
}

func IsInvalidRequest(err error) bool {
	var target *InvalidRequestError
	return errors.As(err, &target)
}

// Service wraps one country's legacy active-release operations. Release-aware
// deployments inject a resolver later without changing either transport.
type Service struct {
	country  string
	base     api.Operations
	resolver api.ReleaseResolver
	limits   config.ReadLimits
	topK     int
}

type legacyResolver struct {
	country string
	ops     api.Operations
}

type legacyPin struct {
	ops  api.Operations
	once sync.Once
}

// NewLegacyResolver adapts the current active pool to the pin contract. The
// empty release identifier preserves legacy payloads. Named releases remain
// unavailable until a release catalog is installed.
func NewLegacyResolver(country string, operations api.Operations) api.ReleaseResolver {
	return &legacyResolver{country: strings.ToLower(strings.TrimSpace(country)), ops: operations}
}

func (r *legacyResolver) Pin(_ context.Context, country, requested string) (api.ReleasePin, error) {
	if strings.TrimSpace(requested) != "" || country != r.country {
		return nil, &ReleaseUnavailableError{Country: country, ReleaseID: strings.TrimSpace(requested)}
	}
	return &legacyPin{ops: r.ops}, nil
}

func (*legacyPin) ReleaseID() string            { return "" }
func (p *legacyPin) Operations() api.Operations { return p.ops }
func (p *legacyPin) Close() error {
	p.once.Do(func() {})
	return nil
}

// New applies read admission and release selection. RetrieveConfig is required
// because a zero request top_k resolves through the configured TopK before the
// MaxResults cap is checked.
func New(country string, base api.Operations, retrieveConfig config.RetrieveConfig, resolver api.ReleaseResolver) (*Service, error) {
	country = strings.ToLower(strings.TrimSpace(country))
	if country == "" {
		return nil, fmt.Errorf("evidence country is required")
	}
	if base == nil {
		return nil, fmt.Errorf("evidence operations are required")
	}
	if err := retrieveConfig.ReadLimits.Validate(); err != nil {
		return nil, err
	}
	topK := retrieveConfig.TopK
	if topK == 0 {
		topK = 8
	}
	if resolver == nil {
		resolver = NewLegacyResolver(country, base)
	}
	return &Service{country: country, base: base, resolver: resolver, limits: retrieveConfig.ReadLimits, topK: topK}, nil
}

func (s *Service) Guide(ctx context.Context, in api.GuideInput) (api.GuideOutput, error) {
	return s.base.Guide(ctx, in)
}

func (s *Service) CorpusStatus(ctx context.Context, in api.CorpusStatusInput) (out api.CorpusStatusOutput, err error) {
	ops, _, closePin, err := s.operations(ctx, "")
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, closePin()) }()
	return ops.CorpusStatus(ctx, in)
}

func (s *Service) QualityGaps(ctx context.Context, in api.QualityGapsInput) (out api.QualityGapsOutput, err error) {
	if len(in.Category) > s.limits.MaxFilterBytes {
		return api.QualityGapsOutput{}, invalid("category exceeds max_filter_bytes")
	}
	in.Category = strings.TrimSpace(in.Category)
	if in.Limit < 0 {
		return api.QualityGapsOutput{}, invalid("limit must not be negative")
	}
	switch strings.ToLower(strings.TrimSpace(in.Category)) {
	case "", "all", "fetch", "non_binding", "mojibake", "partial_validity", "unresolved_relations", "relation_target_text":
	default:
		return api.QualityGapsOutput{}, invalid("unknown quality gap category")
	}
	ops, _, closePin, err := s.operations(ctx, "")
	if err != nil {
		return api.QualityGapsOutput{}, err
	}
	defer func() { err = errors.Join(err, closePin()) }()
	return ops.QualityGaps(ctx, in)
}

func (s *Service) Search(ctx context.Context, in api.SearchInput) (out api.SearchOutput, err error) {
	if err := s.validateRawSearchSize(in); err != nil {
		return out, err
	}
	in = normalizeSearchInput(in)
	if err := s.validateSearch(in); err != nil {
		return out, err
	}
	ops, releaseID, closePin, err := s.operations(ctx, in.ReleaseID)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, closePin()) }()
	in.ReleaseID = ""
	out, err = ops.Search(ctx, in)
	if err == nil && releaseID != "" {
		out.ReleaseID = releaseID
	}
	return out, err
}

func (s *Service) Document(ctx context.Context, in api.DocumentInput) (out api.DocumentOutput, err error) {
	if err := s.validateRawDocumentSize(in); err != nil {
		return out, err
	}
	in = normalizeDocumentInput(in)
	if err := s.validateDocument(in); err != nil {
		return out, err
	}
	ops, releaseID, closePin, err := s.operations(ctx, in.ReleaseID)
	if err != nil {
		return out, err
	}
	defer func() { err = errors.Join(err, closePin()) }()
	in.ReleaseID = ""
	out, err = ops.Document(ctx, in)
	if err == nil && releaseID != "" {
		out.ReleaseID = releaseID
	}
	return out, err
}

func (s *Service) operations(ctx context.Context, requested string) (api.Operations, string, func() error, error) {
	requested = strings.TrimSpace(requested)
	if s.resolver == nil {
		if requested != "" {
			return nil, "", func() error { return nil }, &ReleaseUnavailableError{Country: s.country, ReleaseID: requested}
		}
		return s.base, "", func() error { return nil }, nil
	}
	pin, err := s.resolver.Pin(ctx, s.country, requested)
	if err != nil {
		return nil, "", func() error { return nil }, err
	}
	if pin == nil || pin.Operations() == nil {
		if pin != nil {
			closeErr := pin.Close()
			return nil, "", func() error { return nil }, errors.Join(&ReleaseUnavailableError{Country: s.country, ReleaseID: requested}, closeErr)
		}
		return nil, "", func() error { return nil }, &ReleaseUnavailableError{Country: s.country, ReleaseID: requested}
	}
	return pin.Operations(), pin.ReleaseID(), pin.Close, nil
}

func (s *Service) validateSearch(in api.SearchInput) error {
	if strings.TrimSpace(in.Query) == "" {
		return invalid("query is required")
	}
	if len(in.Query) > s.limits.MaxQueryBytes {
		return invalid("query exceeds max_query_bytes")
	}
	resolvedTopK := in.TopK
	if resolvedTopK == 0 {
		resolvedTopK = s.topK
	}
	if resolvedTopK < 0 || resolvedTopK > s.limits.MaxResults {
		return invalid("top_k is outside the configured limit")
	}
	resolvedRelatedK := in.RelatedK
	if resolvedRelatedK == 0 && (in.IncludeRelated == nil || *in.IncludeRelated) && strings.ToLower(strings.TrimSpace(in.Detail)) != "compact" {
		resolvedRelatedK = 8
	}
	if resolvedRelatedK < 0 || resolvedRelatedK > s.limits.MaxRelated {
		return invalid("related_k is outside the configured limit")
	}
	if len(in.Issuer)+len(in.DocType) > s.limits.MaxFilterValues {
		return invalid("issuer and doc_type exceed max_filter_values")
	}
	for _, value := range in.Issuer {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid("filter value exceeds max_filter_bytes")
		}
	}
	for _, value := range in.DocType {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid("filter value exceeds max_filter_bytes")
		}
	}
	for name, value := range map[string]string{
		"detail":      in.Detail,
		"as_of":       in.AsOf,
		"issued_from": in.IssuedFrom,
		"issued_to":   in.IssuedTo,
		"release_id":  in.ReleaseID,
	} {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid(name + " exceeds max_filter_bytes")
		}
	}
	for name, value := range map[string]string{"as_of": in.AsOf, "issued_from": in.IssuedFrom, "issued_to": in.IssuedTo} {
		if value != "" {
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return invalid(name + " must use YYYY-MM-DD")
			}
		}
	}
	if in.IssuedFrom != "" && in.IssuedTo != "" && in.IssuedFrom > in.IssuedTo {
		return invalid("issued_from must not follow issued_to")
	}
	return nil
}

func (s *Service) validateRawSearchSize(in api.SearchInput) error {
	if len(in.Query) > s.limits.MaxQueryBytes {
		return invalid("query exceeds max_query_bytes")
	}
	if len(in.Issuer)+len(in.DocType) > s.limits.MaxFilterValues {
		return invalid("issuer and doc_type exceed max_filter_values")
	}
	for _, value := range in.Issuer {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid("filter value exceeds max_filter_bytes")
		}
	}
	for _, value := range in.DocType {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid("filter value exceeds max_filter_bytes")
		}
	}
	for name, value := range map[string]string{
		"detail": in.Detail, "as_of": in.AsOf, "issued_from": in.IssuedFrom,
		"issued_to": in.IssuedTo, "release_id": in.ReleaseID,
	} {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid(name + " exceeds max_filter_bytes")
		}
	}
	return nil
}

func (s *Service) validateRawDocumentSize(in api.DocumentInput) error {
	if len(in.Include) > s.limits.MaxFilterValues {
		return invalid("include exceeds max_filter_values")
	}
	for name, value := range map[string]string{
		"doc_number": in.DocNumber, "citation": in.Citation, "release_id": in.ReleaseID,
	} {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid(name + " exceeds max_filter_bytes")
		}
	}
	for _, value := range in.Include {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid("include value exceeds max_filter_bytes")
		}
	}
	return nil
}

func (s *Service) validateDocument(in api.DocumentInput) error {
	if in.DocumentID < 0 {
		return invalid("document_id must not be negative")
	}
	if in.DocumentID == 0 && strings.TrimSpace(in.DocNumber) == "" {
		return invalid("document_id or doc_number is required")
	}
	resolvedLimit := in.Limit
	if resolvedLimit == 0 {
		resolvedLimit = 20
	}
	if resolvedLimit < 0 || resolvedLimit > s.limits.MaxDocumentChunks {
		return invalid("limit is outside max_document_chunks")
	}
	if in.Offset < 0 {
		return invalid("offset must not be negative")
	}
	for name, value := range map[string]string{
		"doc_number":  in.DocNumber,
		"citation":    in.Citation,
		"release_id": in.ReleaseID,
	} {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid(name + " exceeds max_filter_bytes")
		}
	}
	if len(in.Include) > s.limits.MaxFilterValues {
		return invalid("include exceeds max_filter_values")
	}
	for _, value := range in.Include {
		if len(value) > s.limits.MaxFilterBytes {
			return invalid("include value exceeds max_filter_bytes")
		}
	}
	return nil
}

func invalid(message string) error { return &InvalidRequestError{Message: message} }

func normalizeSearchInput(in api.SearchInput) api.SearchInput {
	in.Query = strings.TrimSpace(in.Query)
	in.Detail = strings.TrimSpace(in.Detail)
	in.AsOf = strings.TrimSpace(in.AsOf)
	in.IssuedFrom = strings.TrimSpace(in.IssuedFrom)
	in.IssuedTo = strings.TrimSpace(in.IssuedTo)
	in.ReleaseID = strings.TrimSpace(in.ReleaseID)
	return in
}

func normalizeDocumentInput(in api.DocumentInput) api.DocumentInput {
	in.DocNumber = strings.TrimSpace(in.DocNumber)
	in.Citation = strings.TrimSpace(in.Citation)
	in.ReleaseID = strings.TrimSpace(in.ReleaseID)
	return in
}
