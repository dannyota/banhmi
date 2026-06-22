// Package lexical encodes text into BM25 sparse vectors for pgvector's sparsevec
// type — banhmi's RDS-portable lexical retrieval arm (pg_search/ParadeDB is not
// available on managed RDS). BM25 weights are baked into the stored DOCUMENT
// vector (IDF, term saturation, length normalization); the QUERY vector is just
// term presence, so a sparse inner product (pgvector `<#>`) equals the BM25 score.
//
// Term → dimension uses the hashing trick (FNV-1a mod Dim), so query-time encoding
// needs no persisted vocabulary — only the same deterministic hash. unaccent +
// lower-casing in the tokenizer make diacritic-less queries match (the recall the
// dense BGE-M3 vector misses). The trained Encoder (IDF + average length) is needed
// only when building document vectors.
package lexical

import (
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Dim is the fixed sparsevec dimension. The hashing trick maps each term into
// [1, Dim]; at ~10^5 corpus terms a 2^20 space keeps collisions negligible while
// staying far under pgvector's 16k non-zero-elements-per-vector limit (a chunk
// has at most a few hundred distinct terms).
const Dim = 1 << 20

// BM25 saturation (k1) and length-normalization (b) constants — standard defaults.
const (
	k1 = 1.2
	b  = 0.75
)

// tokenize lower-cases, strips Vietnamese diacritics (NFD → drop combining marks,
// đ→d), and splits on any non-letter/digit. Vietnamese is space-delimited, so this
// syllable-token split mirrors Postgres' 'simple' tokenizer with unaccent folded in.
func tokenize(s string) []string {
	s = norm.NFD.String(strings.ToLower(s))
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r): // combining mark (diacritic)
			continue
		case r == 'đ':
			b.WriteRune('d')
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// DiacriticFree reports whether s carries no Vietnamese diacritics — i.e. the
// user typed without dấu (and not the distinct letter đ). The retriever uses this
// to route such queries to a lexical-boosted fusion, because the dense BGE-M3
// vector degrades badly on diacritic-less Vietnamese while the unaccent-folded
// BM25 arm still matches.
func DiacriticFree(s string) bool {
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) || r == 'đ' || r == 'Đ' {
			return false
		}
	}
	return true
}

// termID maps a term to its 1-based sparsevec index via the hashing trick.
func termID(term string) int32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(term))
	return int32(h.Sum32()%uint32(Dim)) + 1
}

// Encoder holds the trained BM25 statistics needed to build document vectors:
// per-term IDF and the corpus average document length. Build it with Train.
type Encoder struct {
	idf   map[string]float64
	avgdl float64
}

// Train computes IDF (BM25 form) and average document length over the corpus.
// texts is one entry per document (chunk content + any prefix).
func Train(texts []string) *Encoder {
	n := len(texts)
	df := make(map[string]int)
	total := 0
	for _, t := range texts {
		toks := tokenize(t)
		total += len(toks)
		seen := make(map[string]struct{}, len(toks))
		for _, w := range toks {
			if _, ok := seen[w]; ok {
				continue
			}
			seen[w] = struct{}{}
			df[w]++
		}
	}
	idf := make(map[string]float64, len(df))
	for w, d := range df {
		// BM25 IDF (always-positive variant): ln(1 + (N - df + 0.5)/(df + 0.5)).
		idf[w] = math.Log(1 + (float64(n)-float64(d)+0.5)/(float64(d)+0.5))
	}
	avgdl := 1.0
	if n > 0 && total > 0 {
		avgdl = float64(total) / float64(n)
	}
	return &Encoder{idf: idf, avgdl: avgdl}
}

// DocVector returns the BM25 document sparse vector for text as a pgvector
// sparsevec literal. Terms not seen during Train are skipped (IDF unknown).
func (e *Encoder) DocVector(text string) string {
	toks := tokenize(text)
	dl := float64(len(toks))
	tf := make(map[string]int, len(toks))
	for _, w := range toks {
		tf[w]++
	}
	weights := make(map[int32]float64, len(tf))
	for w, f := range tf {
		idf, ok := e.idf[w]
		if !ok || idf <= 0 {
			continue
		}
		num := float64(f) * (k1 + 1)
		den := float64(f) + k1*(1-b+b*dl/e.avgdl)
		weights[termID(w)] += idf * num / den
	}
	return sparseLiteral(weights)
}

// QueryVector returns the query sparse vector — term presence (1.0) per token —
// as a pgvector sparsevec literal. Stateless: it needs only the shared hash, so
// query-time encoding requires no trained Encoder or persisted vocabulary. The
// inner product with a document vector then equals that document's BM25 score.
func QueryVector(text string) string {
	weights := make(map[int32]float64)
	for _, w := range tokenize(text) {
		weights[termID(w)] = 1.0
	}
	return sparseLiteral(weights)
}

// sparseLiteral renders a pgvector sparsevec literal "{i1:v1,i2:v2,...}/Dim" with
// indices in ascending order (required by pgvector). An empty map renders "{}/Dim".
func sparseLiteral(weights map[int32]float64) string {
	ids := make([]int, 0, len(weights))
	for id := range weights {
		ids = append(ids, int(id))
	}
	sort.Ints(ids)
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d:%g", id, weights[int32(id)])
	}
	fmt.Fprintf(&b, "}/%d", Dim)
	return b.String()
}
