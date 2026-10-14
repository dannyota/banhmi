//go:build linux

package conversion

// Validation field inventory for the baseline staged-output contract:
//
// Class (a) means independently established from the hash-checked original or
// its trusted request and compared, including baseline constants and absent
// claims. Class (b) means structurally or cryptographically bound to class (a),
// with the named binding checked before retention. There are no class (c) fields.
// Each row covers every named field and every element of a repeated field.
//
// | Output / fields | Class | Derivation or binding |
// | --- | --- | --- |
// | Document.SchemaVersion | a | validateSemantics requires blocks-v1 |
// | Document.Original (CaptureRef: Country, SourceID, CaptureKey, Asset, MediaType, RequestHash, ResponseHash, TransportReceipt) | a | exact trusted Request.Artifact equality; original bytes hash/length checked |
// | Document.ObservationKey, ArtifactRole, RecipeHash, CandidateDigest | a | exact trusted manifest equality |
// | Document.DetectorHash | b | hash of detection result bound to original and detector recipe |
// | Document.Blocks count and order | a | independent HTML owners / sequential DOCX paragraphs |
// | Block.Kind, RawText | a | HTML owner kind and decoded tokens / DOCX paragraph kind and Word text |
// | Block.Language | a | exact trusted manifest language |
// | Block.Authority, ParentID, Content, Findings | a | unknown authority; no parent, content or block findings in baseline; verifyChains comparison |
// | Block.ID, Ordinal | b | canonical hash of kind/raw/NFC text plus source-owner index; verifyChains |
// | Block.NormalizedText (Path, SHA256, ByteSize), object bytes | b | exact NFC/CRLF transform of independently verified raw text; verifyChains |
// | Block.SourceSpans count and order | a | independent contributing tokens / two DOCX anchors |
// | Source Span.ArtifactHash, Space, Range.Start/End, Member, Object, TokenOrdinal | a | original hash, ZIP main-member hash, format coordinate roots, token/paragraph offsets and names |
// | Source Span.Page, ReasonCode | a | Page absent; reason absent except DOCX original BYTE_MAPPING_UNAVAILABLE |
// | Source Span.BlockID | b | canonical owner ID; validSourceRoots and verifyChains |
// | Document.RawBlocks, NormalizedBlocks (Path, SHA256, ByteSize), object bytes | b | finish canonical JSON of verified blocks and spans |
// | Document.RawText, NormalizedText (Path, SHA256, ByteSize), object bytes | b | ordered concatenation of verified block text; verifyChains and finish |
// | Document.OffsetMap, EditLedger (Path, SHA256, ByteSize), object bytes | b | finish hashes canonical verified chains |
// | Map.Sources, Destinations (every Span field), Transform, Ordinal | b | exact source-to-raw-to-NFC chain of verified blocks; verifyChains |
// | Ledger.Kind, Ordinal, Rule, Version, Inputs, Outputs (every Span field) | b | exact normalization chain of verified raw text; verifyChains |
// | Ledger.Removed, Replacement, Evidence (Path, SHA256, ByteSize), object bytes | b | verifyEdit and verifyChains bind bytes to reversible raw/NFC edit |
// | DecodedEdit.Kind, Rule, Version, Inputs, Outputs, Removed, Replacement | b | verifyEdit exact ledger equality and reverseText; verifyChains |
// | Detection.Version, Format, Original, Recipe | a | detect-v1; original format and trusted original/detector hashes |
// | Quality.SchemaVersion, ArtifactHash, CandidateDigest, RequestDigest, RecipeHash | a | baseline schema and exact trusted request bindings |
// | Quality.DetectorHash, DocumentHash; report and detection serialization | b | exact detection/document bytes hashes; canonical JSON rejects hidden duplicate fields |
// | Quality.Outcome | a | baseline always quarantined, never eligible |
// | Quality.Metrics count/order, Kind, State, Numerator, Denominator, Threshold, Mandatory, Passed, ReasonCode | a | two unknown, mandatory, failed, zero-score baseline metrics; validateSemantics |
// | Metric.Evidence, Exclusions (all reference/span fields) | a | baseline emits none; any supplied claims rejected |
// | Quality.Findings count, ReasonCode, Severity, Scope, ScopeKey, Spans (every Span field) | a | independent source-gap multiset plus two generic blocking artifact findings; validateSourceClaims |
// | Finding.Evidence | a | baseline emits none; any supplied claims rejected |
// | Quality.Findings order | b | comparison as multiset of independently derived complete findings; order cannot alter claims |
// | Gaps / qualification / decision fields | a | absent from this wire schema; decodeJSON rejects unknown fields |
// | Output manifest entries: Path, SHA256, ByteSize; bundle Data | b | sorted exact output closure, canonical base64, hash/length checks, result request paths and objects |
// | Child manifest.Outputs: Path, Type, Size | b | sandbox path/size and byte-type preflight; receipt hashes bind bytes; semantic validation derives claims from bytes regardless of Type |
// | Validation verdict.Version, Request, Outputs (every AssetRef field) | b | validation-v1, trusted request digest, exact validated output references and canonical JSON |
// | Request/recipe bindings: bundle Version, Mode, Request, Original, Recipe | b | readBundle canonical manifest; trusted caller digest; original hash/length; validation forbids recipe bytes; convert hash-checks recipe |
//
// 34 grouped rows: 20 class (a), 14 class (b), zero class (c). AssetRef always
// includes Path, SHA256 and ByteSize. CaptureRef equality includes its complete
// nested values. The Request binding includes its Artifact, Recipe and Budget;
// the manifest binding includes RequestKey, ObservationKey, ArtifactRole,
// Language, HTMLSelector, DetectorHash, CandidateDigest and OutputBytes. Recipe
// bytes are opaque hash-bound input, not a second source of baseline settings.
// No extra wire fields survive decodeJSON. Canonical chain checks
// are bindings, not independent source derivation: source scans never call block
// adapters or reconstruct converted outputs. Changes to any wire type require
// updating this inventory and its isolated substitution controls.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"strings"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

func derivationFailure() error {
	return fmt.Errorf("validate: SOURCE_DERIVATION_MISMATCH")
}

// validateDerivation checks the source-to-raw edge without invoking either block
// adapter, normalization, or canonical reconstruction. The caller has already
// verified original's exact hash and length against the trusted request.
func validateDerivation(original []byte, document model.ConvertedDocument, m ConversionManifest, limits sandbox.Limits) error {
	return validateSourceDerivation(original, document, m, limits, nil)
}

func validateSourceDerivation(original []byte, document model.ConvertedDocument, m ConversionManifest, limits sandbox.Limits, gaps *sourceGaps) error {
	media, parameters, err := mime.ParseMediaType(m.Request.Artifact.MediaType)
	if err != nil {
		return derivationFailure()
	}
	if charset := parameters["charset"]; charset != "" && !strings.EqualFold(charset, "utf-8") && !strings.EqualFold(charset, "utf8") {
		return derivationFailure()
	}
	switch media {
	case "text/html":
		if !bytes.HasPrefix(bytes.TrimSpace(original), []byte("<")) {
			return derivationFailure()
		}
		return validateHTMLSource(original, document.Blocks, m, limits, gaps)
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		if !bytes.HasPrefix(original, []byte{'P', 'K', 3, 4}) {
			return derivationFailure()
		}
		return validateDOCXSource(original, document.Blocks, m, limits, gaps)
	default:
		return derivationFailure()
	}
}

type sourceFragment struct {
	ordinal int
	owner   int
	text    string
	empty   bool
}

// validateHTMLDerivation decodes exact original tokens. Raw HTML text uses
// html.UnescapeString and preserves whitespace, including CRLF and form blanks.
// A br contributes one newline; structural and image anchors can carry no text.
// Matching whole tokens also prevents ranges inside tags or character references
// from claiming a different decoding context.
func validateHTMLDerivation(original []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits) error {
	return validateHTMLSource(original, blocks, m, limits, nil)
}

func validateHTMLSource(original []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits, gaps *sourceGaps) error {
	if !utf8.Valid(original) {
		return derivationFailure()
	}
	if int64(len(original)) > limits.ExpandedBytes {
		return resourceFailure("expanded")
	}
	fragments, owners, err := scanHTMLOwners(original, m, limits, gaps)
	if err != nil {
		return err
	}
	if len(blocks) != len(owners) {
		return derivationFailure()
	}
	consumed := make(map[model.ByteRange]bool, len(fragments))
	for owner, block := range blocks {
		if len(block.SourceSpans) != owners[owner].count || block.Kind != owners[owner].kind {
			return derivationFailure()
		}
		var decoded strings.Builder
		var previousEnd int64
		for i, span := range block.SourceSpans {
			if span.Range == nil || span.Range.Start < 0 || span.Range.End > int64(len(original)) || span.TokenOrdinal == nil || (i > 0 && span.Range.Start < previousEnd) {
				return derivationFailure()
			}
			fragment, ok := fragments[*span.Range]
			if !ok || fragment.owner != owner || consumed[*span.Range] || fragment.ordinal != *span.TokenOrdinal || (fragment.empty && (block.RawText != "" || len(block.SourceSpans) != 1)) {
				return derivationFailure()
			}
			consumed[*span.Range] = true
			if int64(len(fragment.text)) > m.OutputBytes-int64(decoded.Len()) {
				return resourceFailure("output")
			}
			decoded.WriteString(fragment.text)
			previousEnd = span.Range.End
		}
		if len(block.SourceSpans) == 0 || decoded.String() != block.RawText {
			return derivationFailure()
		}
	}
	return nil
}

// validateDOCXDerivation inventories the original ZIP under the sandbox bounds,
// but decompresses only the member named by block anchors. Baseline source-root
// validation limits these anchors to word/document.xml and pairs their names.
func validateDOCXDerivation(original []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits) error {
	return validateDOCXSource(original, blocks, m, limits, nil)
}

func validateDOCXSource(original []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits, gaps *sourceGaps) error {
	zr, err := zip.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		return derivationFailure()
	}
	if len(zr.File) > limits.Members {
		return resourceFailure("items")
	}
	seen := map[string]bool{}
	remaining := limits.ExpandedBytes
	var body []byte
	for _, file := range zr.File {
		name := strings.TrimSuffix(file.Name, "/")
		if !fs.ValidPath(name) || strings.ContainsAny(file.Name, "\\:") || seen[name] || file.Mode()&os.ModeSymlink != 0 {
			return derivationFailure()
		}
		seen[name] = true
		if file.FileInfo().IsDir() {
			continue
		}
		if !file.Mode().IsRegular() {
			return derivationFailure()
		}
		if file.UncompressedSize64 > uint64(remaining) {
			return resourceFailure("expanded")
		}
		remaining -= int64(file.UncompressedSize64)
		if file.UncompressedSize64 > 0 && (file.CompressedSize64 == 0 || (file.UncompressedSize64-1)/file.CompressedSize64 >= uint64(limits.ExpansionRatio)) {
			return resourceFailure("ratio")
		}
		if file.Name != docxBodyPath {
			gaps.add("UNQUALIFIED_PACKAGE_MEMBER", model.Span{ArtifactHash: m.Request.Artifact.Asset.SHA256, Space: model.OriginalBytes, Member: file.Name, ReasonCode: "BYTE_MAPPING_UNAVAILABLE"})
			continue
		}
		reader, err := file.Open()
		if err != nil {
			return derivationFailure()
		}
		body, err = io.ReadAll(io.LimitReader(reader, limits.ExpandedBytes+1))
		closeErr := reader.Close()
		if int64(len(body)) > limits.ExpandedBytes {
			return resourceFailure("expanded")
		}
		if err != nil || closeErr != nil || uint64(len(body)) != file.UncompressedSize64 {
			return derivationFailure()
		}
	}
	if len(body) == 0 {
		return derivationFailure()
	}
	memberHash := digest(body)
	for _, block := range blocks {
		if len(block.SourceSpans) != 2 || block.SourceSpans[1].ArtifactHash != memberHash || block.SourceSpans[0].Member != docxBodyPath || block.SourceSpans[1].Member != docxBodyPath {
			return derivationFailure()
		}
	}
	return validateDOCXParagraphSource(body, blocks, m, limits, gaps)
}

// validateDOCXParagraphs streams the trusted member once and compares each
// anchored paragraph in place. Only Word t character data, tabs and line breaks
// contribute text. No blocks, normalized text, reports or findings are rebuilt.
func validateDOCXParagraphs(body []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits) error {
	return validateDOCXParagraphSource(body, blocks, m, limits, nil)
}

func validateDOCXParagraphSource(body []byte, blocks []model.Block, m ConversionManifest, limits sandbox.Limits, gaps *sourceGaps) error {
	const wordNS = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	decoder := xml.NewDecoder(bytes.NewReader(body))
	depth, paragraphDepth, textDepth, index, paragraphToken := 0, 0, 0, 0, 0
	var start int64
	var text strings.Builder
	documentSeen := false
	memberHash := digest(body)
	for ordinal := 0; ; ordinal++ {
		if ordinal >= m.Request.Budget.MaxItems {
			return resourceFailure("items")
		}
		before := decoder.InputOffset()
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return derivationFailure()
		}
		anchor := model.Span{ArtifactHash: memberHash, Space: model.DecodedMember, Member: docxBodyPath, Range: &model.ByteRange{Start: before, End: decoder.InputOffset()}}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if depth > limits.XMLDepth {
				return resourceFailure("depth")
			}
			if token.Name.Space != wordNS {
				gaps.add("UNSUPPORTED_XML_NAMESPACE", anchor)
				continue
			}
			switch token.Name.Local {
			case "document":
				if depth != 1 || documentSeen {
					return derivationFailure()
				}
				documentSeen = true
			case "body", "r":
			case "p":
				if paragraphDepth != 0 {
					return derivationFailure()
				}
				paragraphDepth, paragraphToken, start = depth, ordinal, before
				text.Reset()
			case "t":
				if paragraphDepth == 0 || textDepth != 0 {
					return derivationFailure()
				}
				textDepth = depth
			case "tab", "br", "cr":
				values := map[string]string{}
				for _, attr := range token.Attr {
					if _, seen := values[attr.Name.Local]; !seen {
						values[attr.Name.Local] = attr.Value
					}
				}
				if values["type"] != "" || values["clear"] != "" {
					gaps.add("BREAK_GEOMETRY_UNKNOWN", anchor)
				}
				if paragraphDepth == 0 {
					return derivationFailure()
				}
				if token.Name.Local == "tab" {
					text.WriteByte('\t')
				} else {
					text.WriteByte('\n')
				}
			default:
				gaps.add("UNSUPPORTED_DOCX_CONSTRUCT", anchor)
			}
		case xml.CharData:
			if textDepth > 0 {
				if int64(len(token)) > m.OutputBytes-int64(text.Len()) {
					return resourceFailure("output")
				}
				text.Write(token)
			} else if strings.TrimSpace(string(token)) != "" {
				gaps.add("UNSUPPORTED_DOCX_TEXT", anchor)
			}
		case xml.EndElement:
			if textDepth == depth {
				textDepth = 0
			}
			if paragraphDepth == depth {
				if index >= len(blocks) {
					return derivationFailure()
				}
				block := blocks[index]
				span := block.SourceSpans[1]
				if block.Kind != model.BlockParagraph || span.Range == nil || span.Range.Start != start || span.Range.End != decoder.InputOffset() || span.TokenOrdinal == nil || *span.TokenOrdinal != paragraphToken || span.Object != fmt.Sprintf("paragraph/%d", index) || block.RawText != text.String() {
					return derivationFailure()
				}
				index++
				paragraphDepth = 0
			}
			depth--
		case xml.Directive:
			return derivationFailure()
		}
		if int64(text.Len()) > m.OutputBytes {
			return resourceFailure("output")
		}
	}
	if depth != 0 || index != len(blocks) || !documentSeen {
		return derivationFailure()
	}
	return gaps.err()
}
