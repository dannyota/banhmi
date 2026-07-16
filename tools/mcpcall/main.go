// Command mcpcall is a Unix-socket bridge for the banhmi MCP server.
//
// It has two modes:
//
//	Server mode — starts the MCP subprocess, performs the JSON-RPC
//	initialize handshake, and accepts tool-call requests over a Unix
//	domain socket (one at a time — the MCP process is single-threaded):
//
//	    go run ./tools/mcpcall -serve [-jurisdiction vn] [-socket /path]
//
//	Client mode — connects to a running server, sends one tool call,
//	prints the result to stdout, and exits:
//
//	    go run ./tools/mcpcall [-jurisdiction vn] [-socket /path] search '{"query":"..."}'
//
// The tool spawns `go run -tags onnx ./cmd/mcp` with the correct ONNX
// environment variables so the caller doesn't need to remember them.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func main() {
	serve := flag.Bool("serve", false, "run in server mode (start MCP subprocess + socket listener)")
	jurisdiction := flag.String("jurisdiction", "vn", "jurisdiction code (vn, my, id)")
	socketPath := flag.String("socket", "", "Unix socket path (default /tmp/mcpcall-{jurisdiction}.sock)")
	timeout := flag.Duration("timeout", 120*time.Second, "client-mode timeout for the tool call")
	flag.Parse()

	if *socketPath == "" {
		*socketPath = fmt.Sprintf("/tmp/mcpcall-%s.sock", *jurisdiction)
	}

	if *serve {
		if err := runServer(*jurisdiction, *socketPath); err != nil {
			slog.Error("server exited", "error", err)
			os.Exit(1)
		}
		return
	}

	// Client mode.
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: mcpcall [-serve] [-jurisdiction vn] [-socket path] <tool> ['<json-args>']")
		os.Exit(1)
	}
	tool := args[0]
	rawArgs := "{}"
	if len(args) > 1 {
		rawArgs = args[1]
	}

	if err := runClient(*socketPath, tool, rawArgs, *timeout); err != nil {
		slog.Error("call failed", "error", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Server mode
// ---------------------------------------------------------------------------

func runServer(jurisdiction, socketPath string) error {
	// Clean up stale socket if nothing is listening.
	if err := cleanStaleSocket(socketPath); err != nil {
		return err
	}

	// Start the MCP subprocess.
	proc, err := startMCP(jurisdiction)
	if err != nil {
		return fmt.Errorf("start MCP: %w", err)
	}
	defer proc.close()

	slog.Info("MCP subprocess started, performing initialize handshake")

	if err := proc.initialize(); err != nil {
		return fmt.Errorf("MCP initialize: %w", err)
	}

	slog.Info("MCP handshake complete")

	// Listen on Unix socket.
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", socketPath, err)
	}
	defer func() {
		ln.Close()
		os.Remove(socketPath)
	}()

	slog.Info("listening", "socket", socketPath, "jurisdiction", jurisdiction)

	// Graceful shutdown on signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Monitor MCP subprocess health.
	deadCh := make(chan error, 1)
	go func() {
		deadCh <- proc.cmd.Wait()
	}()

	// Accept loop (one connection at a time).
	connCh := make(chan net.Conn, 1)
	errCh := make(chan error, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				errCh <- err
				return
			}
			connCh <- c
		}
	}()

	for {
		select {
		case sig := <-sigCh:
			slog.Info("received signal, shutting down", "signal", sig)
			return nil

		case err := <-deadCh:
			return fmt.Errorf("MCP subprocess exited unexpectedly: %w", err)

		case err := <-errCh:
			// Accept error after listener close is expected during shutdown.
			if opErr, ok := err.(*net.OpError); ok && opErr.Err.Error() == "use of closed network connection" {
				return nil
			}
			return fmt.Errorf("accept: %w", err)

		case conn := <-connCh:
			handleConn(conn, proc)
		}
	}
}

// handleConn reads one tool-call request from the connection, forwards it to
// the MCP subprocess, and writes the result back.
func handleConn(conn net.Conn, proc *mcpProc) {
	defer conn.Close()

	// Read the request: {"tool":"...","args":{...}}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	if !scanner.Scan() {
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		slog.Warn("read client request", "error", err)
		writeError(conn, "read request: "+err.Error())
		return
	}

	var req struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		slog.Warn("parse client request", "error", err)
		writeError(conn, "parse request: "+err.Error())
		return
	}
	if req.Args == nil {
		req.Args = json.RawMessage("{}")
	}

	slog.Info("tool call", "tool", req.Tool)

	result, err := proc.callTool(req.Tool, req.Args)
	if err != nil {
		slog.Error("tool call", "tool", req.Tool, "error", err)
		writeError(conn, err.Error())
		return
	}

	conn.Write(result)
	conn.Write([]byte("\n"))
}

func writeError(conn net.Conn, msg string) {
	resp, _ := json.Marshal(map[string]string{"error": msg})
	conn.Write(resp)
	conn.Write([]byte("\n"))
}

// ---------------------------------------------------------------------------
// MCP subprocess management
// ---------------------------------------------------------------------------

type mcpProc struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader *bufio.Scanner
	mu     sync.Mutex
	nextID atomic.Int64
}

func startMCP(jurisdiction string) (*mcpProc, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home dir: %w", err)
	}

	libDir := filepath.Join(home, ".local", "lib")
	cacheDir := filepath.Join(home, ".cache", "banhmi", "qwen3-embedding")

	cmd := exec.Command("go", "run", "-tags", "onnx", "./cmd/mcp", "-config", "config/config.yaml")
	cmd.Dir = findRepoRoot()
	cmd.Stderr = os.Stderr

	// Build environment: inherit current env, then overlay ONNX + jurisdiction vars.
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=1",
		"CGO_LDFLAGS=-L"+libDir,
		"LD_LIBRARY_PATH="+libDir,
		"BANHMI_EMBED_QUERY=onnx",
		"BANHMI_ONNX_MODEL="+filepath.Join(cacheDir, "model_fp16.onnx"),
		"BANHMI_ONNX_TOKENIZER="+filepath.Join(cacheDir, "tokenizer.json"),
		"BANHMI_ONNX_LIB="+filepath.Join(libDir, "libonnxruntime.so"),
		"BANHMI_JURISDICTION="+jurisdiction,
		"BANHMI_DATABASE_PASSWORD=banhmi",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)

	p := &mcpProc{
		cmd:    cmd,
		stdin:  stdin,
		reader: scanner,
	}
	p.nextID.Store(1)
	return p, nil
}

func (p *mcpProc) close() {
	p.stdin.Close()
	// Give subprocess a moment, then kill.
	done := make(chan struct{})
	go func() {
		p.cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.cmd.Process.Kill()
	}
}

// initialize performs the MCP JSON-RPC initialize/initialized handshake.
func (p *mcpProc) initialize() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	id := p.nextID.Add(1) - 1

	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "mcpcall",
				"version": "1.0.0",
			},
		},
	}

	if err := p.send(initReq); err != nil {
		return fmt.Errorf("send initialize: %w", err)
	}

	// Read response — may need to skip notification lines until we get our id.
	if _, err := p.readResponse(id); err != nil {
		return fmt.Errorf("read initialize response: %w", err)
	}

	// Send initialized notification (no id — it's a notification).
	notif := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	}
	if err := p.send(notif); err != nil {
		return fmt.Errorf("send initialized notification: %w", err)
	}

	return nil
}

// callTool sends a tools/call JSON-RPC request and returns the first text
// content block from the result.
func (p *mcpProc) callTool(tool string, args json.RawMessage) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	id := p.nextID.Add(1) - 1

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": args,
		},
	}

	if err := p.send(req); err != nil {
		return nil, fmt.Errorf("send tools/call: %w", err)
	}

	resp, err := p.readResponse(id)
	if err != nil {
		return nil, fmt.Errorf("read tools/call response: %w", err)
	}

	// Check for JSON-RPC error.
	if errField, ok := resp["error"]; ok {
		errBytes, _ := json.Marshal(errField)
		return nil, fmt.Errorf("JSON-RPC error: %s", errBytes)
	}

	// Extract result.content[0].text
	result, ok := resp["result"]
	if !ok {
		return nil, fmt.Errorf("response missing 'result' field")
	}
	resultMap, ok := result.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("result is not an object")
	}

	contentArr, ok := resultMap["content"].([]any)
	if !ok || len(contentArr) == 0 {
		return nil, fmt.Errorf("result has no content blocks")
	}

	first, ok := contentArr[0].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("first content block is not an object")
	}

	text, ok := first["text"].(string)
	if !ok {
		return nil, fmt.Errorf("first content block has no text field")
	}

	return []byte(text), nil
}

func (p *mcpProc) send(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	_, err = p.stdin.Write(data)
	return err
}

// readResponse reads lines from the MCP subprocess until it finds a JSON-RPC
// response with the given id. Notifications (lines without an id or with a
// different id) are skipped.
func (p *mcpProc) readResponse(id int64) (map[string]any, error) {
	for {
		if !p.reader.Scan() {
			err := p.reader.Err()
			if err == nil {
				err = io.EOF
			}
			return nil, fmt.Errorf("read from MCP: %w", err)
		}

		line := p.reader.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			slog.Warn("skip unparseable MCP output", "line", string(line))
			continue
		}

		// Skip notifications (no id field).
		msgID, hasID := msg["id"]
		if !hasID {
			continue
		}

		// JSON numbers unmarshal as float64.
		msgIDFloat, ok := msgID.(float64)
		if !ok {
			continue
		}
		if int64(msgIDFloat) == id {
			return msg, nil
		}

		slog.Warn("skip response with unexpected id", "expected", id, "got", msgID)
	}
}

// findRepoRoot walks up from the executable (or cwd) to find the repo root
// containing go.mod.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

// cleanStaleSocket removes a leftover socket file if nothing is listening on
// it. Returns an error if a live server is detected.
func cleanStaleSocket(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}

	conn, err := net.DialTimeout("unix", path, 2*time.Second)
	if err != nil {
		// Nothing listening — stale socket.
		slog.Info("removing stale socket", "path", path)
		return os.Remove(path)
	}
	conn.Close()
	return fmt.Errorf("server already running on %s", path)
}

// ---------------------------------------------------------------------------
// Client mode
// ---------------------------------------------------------------------------

func runClient(socketPath, tool, rawArgs string, timeout time.Duration) error {
	// Validate that rawArgs is valid JSON.
	if !json.Valid([]byte(rawArgs)) {
		return fmt.Errorf("invalid JSON args: %s", rawArgs)
	}

	conn, err := net.DialTimeout("unix", socketPath, 5*time.Second)
	if err != nil {
		return fmt.Errorf("connect to %s: %w", socketPath, err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(timeout))

	req := map[string]any{
		"tool": tool,
		"args": json.RawMessage(rawArgs),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	data = append(data, '\n')

	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("write request: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
	if !scanner.Scan() {
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		return fmt.Errorf("read response: %w", err)
	}

	respBytes := scanner.Bytes()

	// Check if the response is an error from the server.
	var errResp struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(respBytes, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("server error: %s", errResp.Error)
	}

	// Print result to stdout.
	fmt.Println(string(respBytes))
	return nil
}
