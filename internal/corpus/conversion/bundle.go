//go:build linux

package conversion

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"

	"danny.vn/banhmi/internal/corpus/conversion/sandbox"
	"danny.vn/banhmi/internal/corpus/model"
)

// inputBundle is the bundle-v1 wire schema. Request is canonical manifest JSON.
// Convert mode carries canonical standard-base64 Original and Recipe strings and
// no Outputs. Validate mode carries Original, Request and sorted Outputs with
// their exact references and canonical standard-base64 bytes. Recipe is empty
// in validate mode. Both modes use exactly one input.json and sandbox.TypeJSON.
type inputBundle struct {
	Version  string
	Mode     string
	Request  json.RawMessage
	Original string
	Recipe   string
	Outputs  []bundledOutput
}

type bundledOutput struct {
	Ref  model.AssetRef
	Data string
}

// BundleInputBytes returns a conservative input ceiling for both JSON modes.
// Configure the supervisor with at least this InputBytes value before construction.
// Request and recipe each allow 64 KiB. Output metadata allows 1 MiB plus
// four padding bytes per object.
// The original retains its decoded MaxInputBytes limit independently of transport.
func BundleInputBytes(m ConversionManifest) (int64, error) {
	if m.validate() != nil || m.Request.Budget.MaxInputBytes > math.MaxInt64/8 || m.OutputBytes > math.MaxInt64/8 || int64(m.Request.Budget.MaxItems) > math.MaxInt64/4096 {
		return 0, fmt.Errorf("bundle: INVALID_LIMITS")
	}
	encode := func(n int64) int64 { return ((n + 2) / 3) * 4 }
	return encode(m.Request.Budget.MaxInputBytes) + max(encode(64<<10), encode(m.OutputBytes)+(1<<20)+int64(m.Request.Budget.MaxItems)*4) + (128 << 10), nil
}

// opaqueOriginal copies only the transport string. Original-byte decoding and
// hash/length verification belong exclusively to the validation child.
func opaqueOriginal(inputDir string, m ConversionManifest) (string, error) {
	limit, err := BundleInputBytes(m)
	if err != nil {
		return "", err
	}
	root, err := os.OpenRoot(inputDir)
	if err != nil {
		return "", fmt.Errorf("validate: INPUT_UNAVAILABLE")
	}
	defer root.Close()
	info, err := root.Lstat("input.json")
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return "", fmt.Errorf("validate: INPUT_INVALID")
	}
	file, err := root.Open("input.json")
	if err != nil {
		return "", fmt.Errorf("validate: INPUT_UNAVAILABLE")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, limit+1))
	closeErr := file.Close()
	var bundle inputBundle
	if readErr != nil || closeErr != nil || int64(len(data)) > limit || decodeJSON(data, &bundle) != nil || bundle.Version != "bundle-v1" || bundle.Mode != "convert" {
		return "", fmt.Errorf("validate: INPUT_INVALID")
	}
	return bundle.Original, nil
}

func decodeBase64(encoded string, limit int64) ([]byte, error) {
	if limit < 0 || int64(len(encoded)) > ((limit+2)/3)*4 {
		return nil, resourceFailure("input")
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(data) != encoded {
		return nil, fmt.Errorf("bundle: BASE64_INVALID")
	}
	if int64(len(data)) > limit {
		return nil, resourceFailure("input")
	}
	return data, nil
}

func readBundle(req sandbox.ChildRequest) (inputBundle, ConversionManifest, error) {
	var bundle inputBundle
	var m ConversionManifest
	if req.Limits.Validate() != nil || req.InputType != sandbox.TypeJSON {
		return bundle, m, fmt.Errorf("bundle: INVALID_LIMITS_OR_INPUT_TYPE")
	}
	root, err := os.OpenRoot(req.InputDir)
	if err != nil {
		return bundle, m, fmt.Errorf("bundle: INPUT_UNAVAILABLE")
	}
	defer root.Close()
	entries, err := os.ReadDir(req.InputDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "input.json" {
		return bundle, m, fmt.Errorf("bundle: INPUT_MEMBERS_INVALID")
	}
	info, err := root.Lstat("input.json")
	if err != nil || !info.Mode().IsRegular() {
		return bundle, m, fmt.Errorf("bundle: INPUT_INVALID")
	}
	if info.Size() > req.Limits.InputBytes {
		return bundle, m, resourceFailure("input")
	}
	file, err := root.Open("input.json")
	if err != nil {
		return bundle, m, fmt.Errorf("bundle: INPUT_UNAVAILABLE")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, req.Limits.InputBytes+1))
	closeErr := file.Close()
	if int64(len(data)) > req.Limits.InputBytes {
		return bundle, m, resourceFailure("input")
	}
	if readErr != nil || closeErr != nil || decodeJSON(data, &bundle) != nil || bundle.Version != "bundle-v1" || len(bundle.Request) > 64<<10 || decodeJSON(bundle.Request, &m) != nil || m.validate() != nil {
		return bundle, m, fmt.Errorf("bundle: INVALID_REQUEST")
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(canonical, bundle.Request) {
		return bundle, m, fmt.Errorf("bundle: NONCANONICAL_REQUEST")
	}
	if m.OutputBytes > req.Limits.OutputBytes {
		return bundle, m, resourceFailure("output")
	}
	if m.Request.Budget.MaxElapsed > req.Limits.WallTime {
		return bundle, m, &sandbox.Failure{Cause: sandbox.Timeout}
	}
	return bundle, m, nil
}
