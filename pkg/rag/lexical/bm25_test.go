package lexical

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestTokenizeUnaccent(t *testing.T) {
	got := tokenize("An toàn, hệ-thống thông tin (NHNN)")
	want := []string{"an", "toan", "he", "thong", "thong", "tin", "nhnn"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokenize = %v, want %v", got, want)
	}
	// đ folds to d.
	if got := tokenize("điện đám"); !reflect.DeepEqual(got, []string{"dien", "dam"}) {
		t.Fatalf("đ fold: got %v", got)
	}
}

// TestQueryDiacriticInsensitive: a no-dấu query encodes to the SAME sparse indices
// as the diacritic-bearing one — the recall property native ts_rank_cd lacked.
func TestQueryDiacriticInsensitive(t *testing.T) {
	withDau := QueryVector("an toàn hệ thống thông tin")
	noDau := QueryVector("an toan he thong thong tin")
	if withDau != noDau {
		t.Fatalf("diacritic-insensitive query mismatch:\n withDau=%s\n noDau =%s", withDau, noDau)
	}
}

// parseSparse parses "{id:val,...}/dim" into a map for scoring in tests.
func parseSparse(t *testing.T, lit string) map[int32]float64 {
	t.Helper()
	body := lit[strings.IndexByte(lit, '{')+1 : strings.IndexByte(lit, '}')]
	out := map[int32]float64{}
	if body == "" {
		return out
	}
	for _, kv := range strings.Split(body, ",") {
		parts := strings.SplitN(kv, ":", 2)
		id, _ := strconv.Atoi(parts[0])
		v, _ := strconv.ParseFloat(parts[1], 64)
		out[int32(id)] = v
	}
	return out
}

func dot(q, d map[int32]float64) float64 {
	var s float64
	for id, qv := range q {
		s += qv * d[id]
	}
	return s
}

// TestBM25Ranking: dot(query, doc) == BM25, so a doc about the query topic must
// outscore an off-topic one, and a rare term must outweigh a common one.
func TestBM25Ranking(t *testing.T) {
	corpus := []string{
		"an toàn hệ thống thông tin trong hoạt động ngân hàng",
		"điện toán đám mây cho ngân hàng và bảo mật",
		"quy trình canh tác lúa nước đồng bằng sông cửu long",
		"ngân hàng ngân hàng ngân hàng tổ chức tín dụng",
	}
	e := Train(corpus)
	q := parseSparse(t, QueryVector("an toàn thông tin ngân hàng"))

	score := func(text string) float64 { return dot(q, parseSparse(t, e.DocVector(text))) }
	onTopic := score(corpus[0])
	offTopic := score(corpus[2]) // rice farming
	if onTopic <= offTopic {
		t.Fatalf("on-topic %.3f should outscore off-topic %.3f", onTopic, offTopic)
	}
	if offTopic != 0 {
		t.Fatalf("fully off-topic doc should score 0, got %.3f", offTopic)
	}
	// "ngân hàng" is common (high df → low IDF); a doc spamming it must not beat
	// the doc that actually matches the specific query terms.
	if spam := score(corpus[3]); spam >= onTopic {
		t.Fatalf("common-term spam %.3f should not beat the relevant doc %.3f", spam, onTopic)
	}
}
