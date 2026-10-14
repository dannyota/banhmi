package conversion

// NormalizationRuleNFCCRLF names the reversible NFC and CRLF ledger rule.
const NormalizationRuleNFCCRLF = "nfc-crlf"

// NormalizationVersionNFCCRLF is the version of NormalizationRuleNFCCRLF.
const NormalizationVersionNFCCRLF = "1"

// TransformSourceDecode maps source anchors to decoded raw text.
const TransformSourceDecode = "source-decode-v1"

// TransformIdentity maps raw text to identical normalized text.
const TransformIdentity = "identity-v1"

// TransformNFCCRLF maps raw text to NFC text with CRLF replaced by LF.
const TransformNFCCRLF = "nfc-crlf-v1"
