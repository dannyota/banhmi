package service

import (
	"context"
	"errors"
	"testing"

	"danny.vn/banhmi/internal/evidence/api"
	"danny.vn/banhmi/pkg/base/config"
)

type fakeOperations struct {
	err         error
	statusCalls int
	gapCalls    int
	searchCalls int
	docCalls    int
	documentIn  api.DocumentInput
}

func (*fakeOperations) Guide(context.Context, api.GuideInput) (api.GuideOutput, error) {
	return api.GuideOutput{}, nil
}
func (f *fakeOperations) CorpusStatus(context.Context, api.CorpusStatusInput) (api.CorpusStatusOutput, error) {
	f.statusCalls++
	return api.CorpusStatusOutput{}, f.err
}
func (f *fakeOperations) QualityGaps(context.Context, api.QualityGapsInput) (api.QualityGapsOutput, error) {
	f.gapCalls++
	return api.QualityGapsOutput{}, f.err
}
func (f *fakeOperations) Search(context.Context, api.SearchInput) (api.SearchOutput, error) {
	f.searchCalls++
	return api.SearchOutput{}, f.err
}
func (f *fakeOperations) Document(_ context.Context, in api.DocumentInput) (api.DocumentOutput, error) {
	f.docCalls++
	f.documentIn = in
	return api.DocumentOutput{}, f.err
}

type fakePin struct {
	ops      api.Operations
	id       string
	closes   int
	closeErr error
}

func (p *fakePin) ReleaseID() string          { return p.id }
func (p *fakePin) Operations() api.Operations { return p.ops }
func (p *fakePin) Close() error {
	p.closes++
	return p.closeErr
}

type fakeResolver struct {
	pin      *fakePin
	pinCalls int
}

func (r *fakeResolver) Pin(context.Context, string, string) (api.ReleasePin, error) {
	r.pinCalls++
	return r.pin, nil
}

func TestCorpusReadsPinAndCloseOnSuccessAndError(t *testing.T) {
	cases := []struct {
		name string
		call func(*Service) error
	}{
		{name: "corpus status", call: func(s *Service) error {
			_, err := s.CorpusStatus(context.Background(), api.CorpusStatusInput{})
			return err
		}},
		{name: "quality gaps", call: func(s *Service) error {
			_, err := s.QualityGaps(context.Background(), api.QualityGapsInput{})
			return err
		}},
		{name: "search", call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{Query: "law"})
			return err
		}},
		{name: "document", call: func(s *Service) error {
			_, err := s.Document(context.Background(), api.DocumentInput{DocumentID: 1})
			return err
		}},
	}
	for _, tc := range cases {
		scenarios := []struct {
			name       string
			backendErr error
			closeErr   error
		}{
			{name: "success"},
			{name: "backend error", backendErr: errors.New("backend failed")},
			{name: "close error", closeErr: errors.New("close failed")},
			{name: "backend and close errors", backendErr: errors.New("backend failed"), closeErr: errors.New("close failed")},
		}
		for _, scenario := range scenarios {
			t.Run(tc.name+"/"+scenario.name, func(t *testing.T) {
				base := &fakeOperations{err: scenario.backendErr}
				pin := &fakePin{ops: base, id: "release-1", closeErr: scenario.closeErr}
				resolver := &fakeResolver{pin: pin}
				svc, err := New("vn", base, config.Default().Retrieve, resolver)
				if err != nil {
					t.Fatal(err)
				}
				gotErr := tc.call(svc)
				if scenario.backendErr != nil && !errors.Is(gotErr, scenario.backendErr) {
					t.Fatalf("error = %v, want backend error %v", gotErr, scenario.backendErr)
				}
				if scenario.closeErr != nil && !errors.Is(gotErr, scenario.closeErr) {
					t.Fatalf("error = %v, want close error %v", gotErr, scenario.closeErr)
				}
				if scenario.backendErr == nil && scenario.closeErr == nil && gotErr != nil {
					t.Fatalf("error = %v, want nil", gotErr)
				}
				if resolver.pinCalls != 1 {
					t.Fatalf("pin calls = %d, want 1", resolver.pinCalls)
				}
				if pin.closes != 1 {
					t.Fatalf("pin closes = %d, want 1", pin.closes)
				}
			})
		}
	}
}

func TestInvalidInputsDoNotPinOrCallBackend(t *testing.T) {
	cases := []struct {
		name string
		call func(*Service) error
	}{
		{name: "empty query", call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{})
			return err
		}},
		{name: "too many results", call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{Query: "law", TopK: 51})
			return err
		}},
		{name: "bad date", call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{Query: "law", AsOf: "2026-02-30"})
			return err
		}},
		{name: "too many filters", call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{Query: "law", Issuer: make([]string, 21)})
			return err
		}},
		{name: "bad gap category", call: func(s *Service) error {
			_, err := s.QualityGaps(context.Background(), api.QualityGapsInput{Category: "unknown"})
			return err
		}},
		{name: "missing document selector", call: func(s *Service) error {
			_, err := s.Document(context.Background(), api.DocumentInput{})
			return err
		}},
		{name: "too many document includes", call: func(s *Service) error {
			_, err := s.Document(context.Background(), api.DocumentInput{DocumentID: 1, Include: make([]string, 21)})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &fakeOperations{}
			resolver := &fakeResolver{pin: &fakePin{ops: base}}
			svc, err := New("vn", base, config.Default().Retrieve, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(svc); !IsInvalidRequest(err) {
				t.Fatalf("error = %v, want invalid request", err)
			}
			if resolver.pinCalls != 0 {
				t.Fatalf("pin calls = %d, want 0", resolver.pinCalls)
			}
			if base.statusCalls+base.gapCalls+base.searchCalls+base.docCalls != 0 {
				t.Fatal("invalid input reached the backend")
			}
		})
	}
}

func TestUnknownDocumentIncludePreservesCompatibilityFallback(t *testing.T) {
	base := &fakeOperations{}
	resolver := &fakeResolver{pin: &fakePin{ops: base}}
	svc, err := New("vn", base, config.Default().Retrieve, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Document(context.Background(), api.DocumentInput{DocumentID: 1, Include: []string{"unknown"}}); err != nil {
		t.Fatal(err)
	}
	if base.docCalls != 1 {
		t.Fatalf("document calls = %d, want 1", base.docCalls)
	}
	if len(base.documentIn.Include) != 1 || base.documentIn.Include[0] != "unknown" {
		t.Fatalf("document include = %v, want unknown preserved", base.documentIn.Include)
	}
}

func TestDefaultsAreCheckedAgainstConfiguredCaps(t *testing.T) {
	cases := []struct {
		name           string
		retrieveConfig config.RetrieveConfig
		call           func(*Service) error
	}{
		{name: "search results", retrieveConfig: retrieveConfigWith(config.ReadLimits{MaxResults: 7}), call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{Query: "law", IncludeRelated: boolPointer(false)})
			return err
		}},
		{name: "related results", retrieveConfig: retrieveConfigWith(config.ReadLimits{MaxRelated: 7}), call: func(s *Service) error {
			_, err := s.Search(context.Background(), api.SearchInput{Query: "law"})
			return err
		}},
		{name: "document chunks", retrieveConfig: retrieveConfigWith(config.ReadLimits{MaxDocumentChunks: 19}), call: func(s *Service) error {
			_, err := s.Document(context.Background(), api.DocumentInput{DocumentID: 1})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := &fakeOperations{}
			resolver := &fakeResolver{pin: &fakePin{ops: base}}
			svc, err := New("vn", base, tc.retrieveConfig, resolver)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.call(svc); !IsInvalidRequest(err) {
				t.Fatalf("error = %v, want invalid request", err)
			}
			if resolver.pinCalls != 0 {
				t.Fatalf("pin calls = %d, want 0", resolver.pinCalls)
			}
		})
	}
}

func TestConfiguredTopKIsCheckedAgainstCap(t *testing.T) {
	base := &fakeOperations{}
	resolver := &fakeResolver{pin: &fakePin{ops: base}}
	retrieveConfig := config.Default().Retrieve
	retrieveConfig.TopK = 40
	retrieveConfig.MaxResults = 20
	svc, err := New("vn", base, retrieveConfig, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Search(context.Background(), api.SearchInput{Query: "law", IncludeRelated: boolPointer(false)}); !IsInvalidRequest(err) {
		t.Fatalf("error = %v, want invalid request", err)
	}
	if resolver.pinCalls != 0 {
		t.Fatalf("pin calls = %d, want 0", resolver.pinCalls)
	}
}

func TestConfiguredTopKBelowCompiledDefaultIsAccepted(t *testing.T) {
	base := &fakeOperations{}
	resolver := &fakeResolver{pin: &fakePin{ops: base}}
	retrieveConfig := config.Default().Retrieve
	retrieveConfig.TopK = 3
	retrieveConfig.MaxResults = 7
	svc, err := New("vn", base, retrieveConfig, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Search(context.Background(), api.SearchInput{Query: "law", IncludeRelated: boolPointer(false)}); err != nil {
		t.Fatal(err)
	}
	if resolver.pinCalls != 1 {
		t.Fatalf("pin calls = %d, want 1", resolver.pinCalls)
	}
}

func TestRawByteLimitsApplyBeforeWhitespaceNormalization(t *testing.T) {
	retrieveConfig := config.Default().Retrieve
	retrieveConfig.MaxQueryBytes = 4
	retrieveConfig.MaxFilterBytes = 4
	base := &fakeOperations{}
	resolver := &fakeResolver{pin: &fakePin{ops: base}}
	svc, err := New("vn", base, retrieveConfig, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Search(context.Background(), api.SearchInput{Query: "xxxx", IncludeRelated: boolPointer(false)}); err != nil {
		t.Fatalf("exact-boundary query error = %v", err)
	}
	if _, err := svc.Search(context.Background(), api.SearchInput{Query: "    x", IncludeRelated: boolPointer(false)}); !IsInvalidRequest(err) {
		t.Fatalf("query error = %v, want invalid request", err)
	}
	if _, err := svc.Document(context.Background(), api.DocumentInput{DocumentID: 1, Citation: "    x"}); !IsInvalidRequest(err) {
		t.Fatalf("document error = %v, want invalid request", err)
	}
	if resolver.pinCalls != 1 {
		t.Fatalf("pin calls = %d, want 1 exact-boundary request", resolver.pinCalls)
	}
}

func TestExplicitReleaseWithoutResolverFails(t *testing.T) {
	svc, err := New("vn", &fakeOperations{}, config.Default().Retrieve, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Search(context.Background(), api.SearchInput{Query: "law", ReleaseID: "r1"})
	var unavailable *ReleaseUnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("error = %v, want ReleaseUnavailableError", err)
	}
}

func TestLegacyResolverNormalizesCountry(t *testing.T) {
	base := &fakeOperations{}
	svc, err := New(" vn ", base, config.Default().Retrieve, NewLegacyResolver(" VN ", base))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CorpusStatus(context.Background(), api.CorpusStatusInput{}); err != nil {
		t.Fatal(err)
	}
	if base.statusCalls != 1 {
		t.Fatalf("status calls = %d, want 1", base.statusCalls)
	}
}

func TestPinnedSearchReturnsRelease(t *testing.T) {
	base := &fakeOperations{}
	pin := &fakePin{ops: base, id: "r1"}
	svc, err := New("vn", base, config.Default().Retrieve, &fakeResolver{pin: pin})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Search(context.Background(), api.SearchInput{Query: "law"})
	if err != nil {
		t.Fatal(err)
	}
	if out.ReleaseID != "r1" {
		t.Fatalf("release_id = %q, want r1", out.ReleaseID)
	}
}

func TestNilPinnedOperationsReturnsCleanupFailure(t *testing.T) {
	base := &fakeOperations{}
	closeErr := errors.New("close failed")
	pin := &fakePin{closeErr: closeErr}
	svc, err := New("vn", base, config.Default().Retrieve, &fakeResolver{pin: pin})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Search(context.Background(), api.SearchInput{Query: "law"})
	var unavailable *ReleaseUnavailableError
	if !errors.As(err, &unavailable) || !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want release unavailable and close failure", err)
	}
	if pin.closes != 1 {
		t.Fatalf("pin closes = %d, want 1", pin.closes)
	}
}

func retrieveConfigWith(overrides config.ReadLimits) config.RetrieveConfig {
	retrieveConfig := config.Default().Retrieve
	limits := retrieveConfig.ReadLimits
	if overrides.MaxResults != 0 {
		limits.MaxResults = overrides.MaxResults
	}
	if overrides.MaxRelated != 0 {
		limits.MaxRelated = overrides.MaxRelated
	}
	if overrides.MaxDocumentChunks != 0 {
		limits.MaxDocumentChunks = overrides.MaxDocumentChunks
	}
	retrieveConfig.ReadLimits = limits
	return retrieveConfig
}

func boolPointer(value bool) *bool { return &value }

func TestFormatCoveragePreservesJurisdictionCopy(t *testing.T) {
	cases := map[string]string{
		"vn": "banhmi has extracted and indexed 2 official documents (3 provisions) across 4 government sources",
		"my": "laksa has extracted and indexed 2 official documents (3 provisions) across 4 official sources",
		"id": "rendang has extracted and indexed 2 official documents (3 provisions) across 4 official sources",
		"sg": "kaya has extracted and indexed 2 official documents (3 provisions) across 4 official sources",
		"th": "tomyum has extracted and indexed 2 official documents (3 provisions) across 4 official sources",
		"kh": "amok has extracted and indexed 2 official documents (3 provisions) across 4 official sources",
	}
	for country, want := range cases {
		if got := formatCoverage(country, 2, 3, 4); got != want {
			t.Errorf("formatCoverage(%q) = %q, want %q", country, got, want)
		}
	}
}
