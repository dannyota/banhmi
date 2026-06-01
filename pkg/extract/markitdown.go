package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultMarkItDownCommand = "python3"
	defaultMarkItDownTimeout = 240 * time.Second
)

// MarkItDownClient runs the local Python MarkItDown helper. MarkItDown is part
// of the banhmi app runtime image, not a separate HTTP sidecar.
type MarkItDownClient struct {
	command string
	script  string
	timeout time.Duration
}

// NewMarkItDownClient returns a local MarkItDown runner. Empty values use the
// standard app-container defaults and the repo-local helper for host runs.
func NewMarkItDownClient(command, script string) *MarkItDownClient {
	command = strings.TrimSpace(command)
	if command == "" {
		command = defaultMarkItDownCommand
	}
	return &MarkItDownClient{
		command: command,
		script:  strings.TrimSpace(script),
		timeout: defaultMarkItDownTimeout,
	}
}

// ConvertResult is the JSON body returned by the local helper.
type ConvertResult struct {
	Markdown string `json:"markdown"`
	Title    string `json:"title"`
}

// ConvertData converts in-memory document bytes to Markdown by writing a
// short-lived temp file and passing it to the local helper. DOCX, HTML, and
// born-digital PDF go straight to MarkItDown; legacy DOC is rendered to PDF by
// LibreOffice first.
func (c *MarkItDownClient) ConvertData(ctx context.Context, data []byte, ext string) (*ConvertResult, error) {
	ext = strings.TrimSpace(ext)
	if ext == "" {
		return nil, fmt.Errorf("markitdown convert data: extension is required")
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	tmp, err := os.CreateTemp("", "banhmi-markitdown-*"+ext)
	if err != nil {
		return nil, fmt.Errorf("create markitdown temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write markitdown temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close markitdown temp file: %w", err)
	}
	return c.ConvertPath(ctx, tmpPath)
}

// ConvertPath converts a local file by absolute or relative path. The helper
// selects the MarkItDown converter from the suffix.
func (c *MarkItDownClient) ConvertPath(ctx context.Context, path string) (*ConvertResult, error) {
	script, err := c.scriptPath()
	if err != nil {
		return nil, err
	}

	runCtx := ctx
	cancel := func() {}
	if _, ok := ctx.Deadline(); !ok {
		runCtx, cancel = context.WithTimeout(ctx, c.timeout)
	}
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(runCtx, c.command, script, path)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("markitdown convert: %w: %s", err, firstLine(msg))
		}
		return nil, fmt.Errorf("markitdown convert: %w", err)
	}

	var res ConvertResult
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("decode markitdown output: %w", err)
	}
	return &res, nil
}

func (c *MarkItDownClient) scriptPath() (string, error) {
	if c.script != "" {
		return c.script, nil
	}
	candidates := []string{
		os.Getenv("BANHMI_MARKITDOWN_SCRIPT"),
		"/opt/banhmi/markitdown_convert.py",
		filepath.Join("tools", "markitdown_convert.py"),
	}
	for _, p := range candidates {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("markitdown helper script not found")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
