package extract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMarkItDownConvertDataUsesTempFileAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import json
import pathlib
import sys
path = pathlib.Path(sys.argv[1])
print(json.dumps({"markdown": path.read_text(encoding="utf-8") + "|" + path.suffix + "|" + str(path.exists()), "title": str(path)}))
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	client := NewMarkItDownClient("python3", script)
	res, err := client.ConvertData(context.Background(), []byte("body"), "html")
	if err != nil {
		t.Fatalf("ConvertData: %v", err)
	}
	if res.Markdown != "body|.html|True" {
		t.Fatalf("markdown = %q, want temp file content/suffix", res.Markdown)
	}
	if _, err := os.Stat(res.Title); !os.IsNotExist(err) {
		t.Fatalf("temp file still exists or stat failed unexpectedly: %v", err)
	}
}

func TestMarkItDownConvertPathReturnsTerseError(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake_markitdown.py")
	if err := os.WriteFile(script, []byte(`#!/usr/bin/env python3
import sys
print("first line", file=sys.stderr)
print("second line", file=sys.stderr)
raise SystemExit(1)
`), 0o700); err != nil {
		t.Fatalf("write fake script: %v", err)
	}

	client := NewMarkItDownClient("python3", script)
	_, err := client.ConvertPath(context.Background(), filepath.Join(dir, "missing.docx"))
	if err == nil {
		t.Fatal("ConvertPath returned nil error")
	}
	if !strings.Contains(err.Error(), "first line") {
		t.Fatalf("error = %q, want first stderr line", err)
	}
	if strings.Contains(err.Error(), "second line") {
		t.Fatalf("error = %q, want only first stderr line", err)
	}
}
