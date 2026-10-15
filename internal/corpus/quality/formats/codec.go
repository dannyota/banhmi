package formats

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"unicode/utf8"

	"danny.vn/banhmi/internal/corpus/model"
)

// Codec encodes and decodes one retained format with trusted local bounds.
// Decode rejects unknown fields, duplicate keys, trailing values and any bytes
// that differ from canonical re-encoding. Neither method reads referenced assets.
type Codec[T any] struct {
	limits    Limits
	validate  func(T) error
	marshal   func(T) ([]byte, error)
	unmarshal func([]byte, *T) error
}

// Encode returns canonical bytes and their lowercase SHA-256 digest.
func (c Codec[T]) Encode(value T) ([]byte, model.Digest, error) {
	if err := c.limits.Validate(); err != nil {
		return nil, "", err
	}
	if c.validate == nil {
		return nil, "", fmt.Errorf("unconfigured codec")
	}
	if err := validateTree(value, c.limits); err != nil {
		return nil, "", err
	}
	if err := c.validate(value); err != nil {
		return nil, "", err
	}
	marshal := c.marshal
	if marshal == nil {
		marshal = func(v T) ([]byte, error) { return json.Marshal(v) }
	}
	data, err := marshal(value)
	if err != nil {
		return nil, "", fmt.Errorf("encode format: %w", err)
	}
	if int64(len(data)) > c.limits.MaxJSONBytes {
		return nil, "", fmt.Errorf("format exceeds JSON byte bound")
	}
	if err := rootBounds(value, data, c.limits); err != nil {
		return nil, "", err
	}
	return data, Hash(data), nil
}

// Decode returns a validated value and the digest of its exact canonical bytes.
func (c Codec[T]) Decode(data []byte) (T, model.Digest, error) {
	var value T
	if err := c.limits.Validate(); err != nil {
		return value, "", err
	}
	if int64(len(data)) > c.limits.MaxJSONBytes {
		return value, "", fmt.Errorf("format exceeds JSON byte bound")
	}
	if err := uniqueJSON(data, c.limits.MaxRows); err != nil {
		return value, "", err
	}
	var err error
	if c.unmarshal != nil {
		err = c.unmarshal(data, &value)
	} else {
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		err = d.Decode(&value)
	}
	if err != nil {
		return value, "", fmt.Errorf("decode format: %w", err)
	}
	canonical, digest, err := c.Encode(value)
	if err != nil {
		return value, "", err
	}
	if !bytes.Equal(data, canonical) {
		return value, "", fmt.Errorf("noncanonical format bytes")
	}
	return value, digest, nil
}

// Hash hashes exact bytes without normalization or JSON interpretation.
func Hash(data []byte) model.Digest {
	sum := sha256.Sum256(data)
	return model.Digest(hex.EncodeToString(sum[:]))
}

// uniqueJSON rejects duplicate keys recursively before any struct decoding.
func uniqueJSON(data []byte, rows int64) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := jsonValue(d, 0, &rows); err != nil {
		return fmt.Errorf("JSON structure: %w", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func jsonValue(d *json.Decoder, depth int, rows *int64) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting exceeds bound")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid object key")
			}
			seen[name] = true
			if err := jsonValue(d, depth+1, rows); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if *rows == 0 {
				return fmt.Errorf("JSON arrays exceed row bound")
			}
			*rows -= 1
			if err := jsonValue(d, depth+1, rows); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}

func validateTree(value any, limits Limits) error {
	return validateTrees(limits, false, value)
}

// validateTrees shares all budgets and hash sizes across values, but each value
// has its own path namespace. Only frozen M1.12 collections may be nullable.
func validateTrees(limits Limits, nullable bool, values ...any) error {
	rows := limits.MaxRows
	stringBytes := limits.MaxJSONBytes
	assets := make(map[model.Digest]int64)
	paths := make(map[string]model.Digest)
	var total int64
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return walk(v.Elem())
		}
		if v.CanInterface() {
			switch x := v.Interface().(type) {
			case model.Digest:
				return x.Validate()
			case model.AssetRef:
				if err := x.Validate(); err != nil {
					return err
				}
				if old, ok := paths[x.Path]; ok && old != x.SHA256 {
					return fmt.Errorf("conflicting hashes for logical path")
				}
				paths[x.Path] = x.SHA256
				if size, ok := assets[x.SHA256]; ok {
					if size != x.ByteSize {
						return fmt.Errorf("conflicting sizes for hash")
					}
				} else {
					if int64(len(assets)) >= limits.MaxBlobs || x.ByteSize > limits.MaxObjectBytes-total {
						return fmt.Errorf("asset set exceeds blob or object byte bound")
					}
					assets[x.SHA256] = x.ByteSize
					total += x.ByteSize
				}
			case model.Span:
				if err := x.Validate(); err != nil {
					return err
				}
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			if (!nullable && v.IsNil()) || int64(v.Len()) > rows {
				return fmt.Errorf("null array or collection exceeds row bound")
			}
			rows -= int64(v.Len())
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i)); err != nil {
					return err
				}
			}
		case reflect.String:
			if int64(v.Len()) > stringBytes {
				return fmt.Errorf("decoded strings exceed JSON byte bound")
			}
			stringBytes -= int64(v.Len())
			if !utf8.ValidString(v.String()) {
				return fmt.Errorf("invalid UTF-8 string")
			}
		}
		return nil
	}
	for _, value := range values {
		clear(paths)
		if err := walk(reflect.ValueOf(value)); err != nil {
			return err
		}
	}
	return nil
}

func rootBounds(value any, data []byte, limits Limits) error {
	root := Hash(data)
	assets := map[model.Digest]int64{root: int64(len(data))}
	total := int64(len(data))
	if total > limits.MaxObjectBytes {
		return fmt.Errorf("root exceeds object byte bound")
	}
	var walk func(reflect.Value) error
	walk = func(v reflect.Value) error {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return nil
			}
			return walk(v.Elem())
		}
		if v.CanInterface() {
			if ref, ok := v.Interface().(model.AssetRef); ok {
				if ref == ReportSentinel() {
					return nil
				}
				if ref.SHA256 == root {
					return fmt.Errorf("root cannot reference itself")
				}
				if _, exists := assets[ref.SHA256]; !exists {
					if int64(len(assets)) >= limits.MaxBlobs || ref.ByteSize > limits.MaxObjectBytes-total {
						return fmt.Errorf("root and dependencies exceed blob or byte bound")
					}
					assets[ref.SHA256] = ref.ByteSize
					total += ref.ByteSize
				}
				return nil
			}
		}
		switch v.Kind() {
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				if err := walk(v.Field(i)); err != nil {
					return err
				}
			}
		case reflect.Slice:
			for i := 0; i < v.Len(); i++ {
				if err := walk(v.Index(i)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(reflect.ValueOf(value))
}
