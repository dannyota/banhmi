package pipeline

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"danny.vn/banhmi/pkg/ingest"
	dbconfig "danny.vn/banhmi/pkg/store/config"
)

// fakeKeywordRows is a minimal pgx.Rows yielding one string column per row,
// backing the fake ListDiscoveryKeywords result set.
type fakeKeywordRows struct {
	terms []string
	i     int
}

func (r *fakeKeywordRows) Close()                                       {}
func (r *fakeKeywordRows) Err() error                                   { return nil }
func (r *fakeKeywordRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeKeywordRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeKeywordRows) Next() bool {
	if r.i >= len(r.terms) {
		return false
	}
	r.i++
	return true
}

func (r *fakeKeywordRows) Scan(dest ...any) error {
	*(dest[0].(*string)) = r.terms[r.i-1]
	return nil
}

func (r *fakeKeywordRows) Values() ([]any, error) { return nil, nil }
func (r *fakeKeywordRows) RawValues() [][]byte    { return nil }
func (r *fakeKeywordRows) Conn() *pgx.Conn        { return nil }

// fakeKeywordDB is a dbconfig.DBTX that serves ListDiscoveryKeywords from a
// map keyed by the bound source parameter. DiscoverSlices only issues that one
// query; the other DBTX methods are never reached.
type fakeKeywordDB struct {
	keywords map[string][]string
}

func (f *fakeKeywordDB) Exec(context.Context, string, ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (f *fakeKeywordDB) Query(_ context.Context, _ string, args ...interface{}) (pgx.Rows, error) {
	source, _ := args[0].(string)
	return &fakeKeywordRows{terms: f.keywords[source]}, nil
}

func (f *fakeKeywordDB) QueryRow(context.Context, string, ...interface{}) pgx.Row { return nil }

func discoverSlicesActivities(keywords map[string][]string, sourceIDs ...string) *Activities {
	sources := make(map[string]ingest.Source, len(sourceIDs))
	for _, id := range sourceIDs {
		sources[id] = nil // DiscoverSlices only checks wiring, never calls the source
	}
	return &Activities{
		configQ: dbconfig.New(&fakeKeywordDB{keywords: keywords}),
		sources: sources,
	}
}

func TestDiscoverSlices(t *testing.T) {
	tests := []struct {
		name     string
		keywords map[string][]string
		wired    []string
		ask      []string
		want     []DiscoverParams
	}{
		{
			// Regression guard for live VN: vbpl keeps its sweep + one slice
			// per configured keyword, keyword-less sources get only the sweep.
			name: "vbpl keywords preserved",
			keywords: map[string][]string{
				"vbpl": {"an ninh mạng", "dữ liệu cá nhân"},
			},
			wired: []string{"congbao", "vbpl"},
			ask:   []string{"vbpl", "congbao"},
			want: []DiscoverParams{
				{Source: "congbao"},
				{Source: "vbpl"},
				{Source: "vbpl", Keyword: "an ninh mạng"},
				{Source: "vbpl", Keyword: "dữ liệu cá nhân"},
			},
		},
		{
			// A non-vbpl source with keyword rows gets keyword slices too —
			// keyword discovery is config-driven, not source-hardcoded.
			name: "non-vbpl source with keywords",
			keywords: map[string][]string{
				"bpk": {"perbankan", "pelindungan data pribadi"},
			},
			wired: []string{"bi", "bpk"},
			ask:   []string{"bpk", "bi"},
			want: []DiscoverParams{
				{Source: "bi"},
				{Source: "bpk"},
				{Source: "bpk", Keyword: "perbankan"},
				{Source: "bpk", Keyword: "pelindungan data pribadi"},
			},
		},
		{
			name:     "source without keyword rows gets only the sweep",
			keywords: map[string][]string{},
			wired:    []string{"congbao"},
			ask:      []string{"congbao"},
			want:     []DiscoverParams{{Source: "congbao"}},
		},
		{
			name: "unwired source is skipped",
			keywords: map[string][]string{
				"vbpl": {"an ninh mạng"},
			},
			wired: []string{"congbao"},
			ask:   []string{"vbpl", "congbao"},
			want:  []DiscoverParams{{Source: "congbao"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := discoverSlicesActivities(tt.keywords, tt.wired...)
			got, err := a.DiscoverSlices(context.Background(), tt.ask)
			if err != nil {
				t.Fatalf("DiscoverSlices: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("slices = %+v, want %+v", got, tt.want)
			}
		})
	}
}
