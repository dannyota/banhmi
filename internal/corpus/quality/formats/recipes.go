package formats

import (
	"fmt"
	"sort"

	"danny.vn/banhmi/internal/corpus/conversion"
)

// BaselineNormalizer derives the manifest exclusively from M1.12's exported
// normalization contract. Rules and transforms use the spec's sorted order.
func BaselineNormalizer() Normalizer {
	v := Normalizer{
		SchemaVersion: "normalizer-v1",
		Rules: []NormalizationRule{{
			Rule:    conversion.NormalizationRuleNFCCRLF,
			Version: conversion.NormalizationVersionNFCCRLF,
		}},
		Transforms: []string{
			conversion.TransformSourceDecode,
			conversion.TransformIdentity,
			conversion.TransformNFCCRLF,
		},
	}
	sort.Slice(v.Rules, func(i, j int) bool {
		return ruleLess(v.Rules[i], v.Rules[j])
	})
	sort.Strings(v.Transforms)
	return v
}

// NormalizerCodec constructs the normalizer-v1 rule A codec.
func NormalizerCodec(limits Limits) Codec[Normalizer] {
	return Codec[Normalizer]{limits: limits, validate: func(v Normalizer) error {
		if v.SchemaVersion != "normalizer-v1" || len(v.Rules) == 0 || len(v.Transforms) == 0 {
			return fmt.Errorf("invalid normalizer version or empty recipe")
		}
		for i, r := range v.Rules {
			if r.Rule == "" || r.Version == "" || (i > 0 && !ruleLess(v.Rules[i-1], r)) {
				return fmt.Errorf("normalizer rules must be nonempty, unique and sorted")
			}
		}
		return orderedStrings(v.Transforms)
	}}
}

func ruleLess(a, b NormalizationRule) bool {
	return a.Rule < b.Rule || (a.Rule == b.Rule && a.Version < b.Version)
}

// ParserCodec constructs the parser-v1 rule A codec. The parser name is a
// required identity; none is the unqualified M1 baseline.
func ParserCodec(limits Limits) Codec[Parser] {
	return Codec[Parser]{limits: limits, validate: func(v Parser) error {
		if v.SchemaVersion != "parser-v1" || v.Parser == "" {
			return fmt.Errorf("invalid parser version or identity")
		}
		return nil
	}}
}

func orderedStrings(values []string) error {
	for i, value := range values {
		if value == "" || (i > 0 && values[i-1] >= value) {
			return fmt.Errorf("strings must be nonempty, unique and byte-sorted")
		}
	}
	return nil
}
