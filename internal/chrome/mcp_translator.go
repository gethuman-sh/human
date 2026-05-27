package chrome

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"sync/atomic"

	"github.com/rs/zerolog"

	"github.com/gethuman-sh/human/errors"
)

// mcpBufSize is the max line size for reading JSON-RPC responses from the
// MCP subprocess. Must be large enough for tool responses that carry
// screenshots or full accessibility trees.
const mcpBufSize = 4 * 1024 * 1024 // 4 MB

// McpTranslator bridges a net.Conn speaking the native host socket protocol
// (4-byte LE framing) to a claude --claude-in-chrome-mcp subprocess speaking
// JSON-RPC over stdio. It translates only the envelope — payloads are opaque.
type McpTranslator struct {
	// ClaudePath is the absolute path to the claude binary.
	ClaudePath string
	Logger     zerolog.Logger
}

// Serve handles one bridge connection. It spawns a fresh MCP subprocess,
// performs the JSON-RPC init handshake, then runs the bidirectional
// translation loop until conn closes or the subprocess exits.
func (t *McpTranslator) Serve(ctx context.Context, conn net.Conn) error {
	if conn == nil {
		return errors.WithDetails("nil connection")
	}
	if t.ClaudePath == "" {
		return errors.WithDetails("claude binary path not configured")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.ClaudePath, "--claude-in-chrome-mcp") // #nosec G204 -- path from exec.LookPath in cmd_daemon.go
	// Only pass safe env vars — exclude tracker tokens and other credentials.
	cmd.Env = filteredEnv("PATH", "HOME", "USER", "TERM", "SHELL", "LANG",
		"HUMAN_CHROME_ADDR", "HUMAN_DAEMON_ADDR", "HUMAN_DAEMON_TOKEN")
	cmd.Env = append(cmd.Env, "CLAUDE_CHROME_PERMISSION_MODE=skip_all_permission_checks")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return errors.WrapWithDetails(err, "creating stdin pipe")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return errors.WrapWithDetails(err, "creating stdout pipe")
	}

	// Discard stderr — the MCP subprocess logs internally.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return errors.WrapWithDetails(err, "starting MCP subprocess",
			"path", t.ClaudePath)
	}
	t.Logger.Info().Int("pid", cmd.Process.Pid).Msg("started MCP subprocess")

	// Scanner for newline-delimited JSON-RPC from subprocess stdout.
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, mcpBufSize), mcpBufSize)

	if err := t.initHandshake(stdin, scanner); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.WrapWithDetails(err, "MCP init handshake failed")
	}

	var nextID atomic.Int64
	nextID.Store(1)

	errCh := make(chan error, 2) //nolint:mnd // two directions

	// Goroutine A: conn → subprocess stdin (inbound).
	go func() {
		errCh <- t.inbound(conn, stdin, &nextID)
		_ = stdin.Close()
	}()

	// Goroutine B: subprocess stdout → conn (outbound).
	go func() {
		errCh <- t.outbound(scanner, conn)
	}()

	firstErr := <-errCh
	cancel()
	// Close conn so the other goroutine unblocks if stuck on I/O.
	_ = conn.Close()
	secondErr := <-errCh

	// Surface the first non-nil goroutine error at Warn. Silent errors
	// here previously hid protocol-level problems (malformed frames,
	// closed subprocess stdout) so a broken bridge looked healthy.
	if firstErr != nil {
		t.Logger.Warn().Err(firstErr).Msg("MCP bridge goroutine error")
	}
	if secondErr != nil {
		t.Logger.Warn().Err(secondErr).Msg("MCP bridge goroutine error")
	}

	if waitErr := cmd.Wait(); waitErr != nil {
		t.Logger.Warn().Err(waitErr).Msg("MCP subprocess exited non-zero")
	}

	return nil
}

// initHandshake performs the MCP initialize → response → initialized exchange.
func (t *McpTranslator) initHandshake(stdin io.Writer, scanner *bufio.Scanner) error {
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"method":  "initialize",
		"id":      0,
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "human-daemon",
				"version": "1.0",
			},
		},
	}
	if err := writeJSONLine(stdin, initReq); err != nil {
		return errors.WrapWithDetails(err, "sending initialize request")
	}

	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return errors.WrapWithDetails(err, "reading MCP initialize response")
		}
		return errors.WithDetails("no response to initialize request")
	}

	var resp map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return errors.WrapWithDetails(err, "parsing initialize response")
	}
	if _, ok := resp["error"]; ok {
		return errors.WithDetails("MCP subprocess returned error on init",
			"error", string(scanner.Bytes()))
	}

	notification := map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	}
	if err := writeJSONLine(stdin, notification); err != nil {
		return errors.WrapWithDetails(err, "sending initialized notification")
	}

	t.Logger.Info().Msg("MCP init handshake complete")
	return nil
}

// inbound reads 4-byte LE frames from conn, translates to JSON-RPC, writes to stdin.
func (t *McpTranslator) inbound(conn net.Conn, stdin io.Writer, nextID *atomic.Int64) error {
	for {
		frame, err := ReadMessage(conn)
		if err != nil {
			return err // conn closed or read error
		}

		var msg map[string]any
		if err := json.Unmarshal(frame, &msg); err != nil {
			t.Logger.Warn().Err(err).Msg("skipping malformed inbound frame")
			continue
		}

		rpc, err := socketToJSONRPC(msg, nextID.Add(1))
		if err != nil {
			t.Logger.Warn().Err(err).Msg("skipping untranslatable inbound frame")
			continue
		}

		if err := writeJSONLine(stdin, rpc); err != nil {
			return err
		}
	}
}

// outbound reads JSON-RPC lines from scanner, translates to 4-byte LE frames, writes to conn.
func (t *McpTranslator) outbound(scanner *bufio.Scanner, conn net.Conn) error {
	for scanner.Scan() {
		var msg map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			t.Logger.Warn().Err(err).Msg("skipping malformed outbound line")
			continue
		}

		frame := jsonRPCToSocket(msg)

		data, err := json.Marshal(frame)
		if err != nil {
			t.Logger.Warn().Err(err).Msg("skipping unmarshalable outbound message")
			continue
		}

		if err := WriteMessage(conn, data); err != nil {
			return err
		}
	}

	return scanner.Err()
}

// socketToJSONRPC translates a native host socket message to a JSON-RPC tools/call.
//
// Input:  {"method":"execute_tool","params":{"client_id":"...","tool":"T","args":{...}}}
// Output: {"jsonrpc":"2.0","method":"tools/call","id":N,"params":{"name":"T","arguments":{...}}}
func socketToJSONRPC(msg map[string]any, id int64) (map[string]any, error) {
	params, _ := msg["params"].(map[string]any)
	if params == nil {
		return nil, errors.WithDetails("missing params in socket message")
	}

	toolName, _ := params["tool"].(string)
	if toolName == "" {
		return nil, errors.WithDetails("missing params.tool in socket message")
	}

	args := params["args"]
	if args == nil {
		args = map[string]any{}
	}

	return map[string]any{
		"jsonrpc": "2.0",
		"method":  "tools/call",
		"id":      id,
		"params": map[string]any{
			"name":      toolName,
			"arguments": args,
		},
	}, nil
}

// jsonRPCToSocket translates a JSON-RPC response or notification to a socket frame payload.
// Strips "jsonrpc" and "id" keys; passes everything else through.
func jsonRPCToSocket(msg map[string]any) map[string]any {
	out := make(map[string]any, len(msg))
	for k, v := range msg {
		if k == "jsonrpc" || k == "id" {
			continue
		}
		out[k] = v
	}
	return out
}

// writeJSONLine marshals v as JSON and writes it as a newline-terminated line.
func writeJSONLine(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return errors.WrapWithDetails(err, "marshaling JSON-RPC message")
	}
	data = append(data, '\n')
	_, err = w.Write(data)
	if err != nil {
		return errors.WrapWithDetails(err, "writing JSON-RPC message")
	}
	return nil
}

// filteredEnv returns a copy of os.Environ containing only the named keys.
func filteredEnv(keys ...string) []string {
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	var out []string
	for _, entry := range os.Environ() {
		for i := range entry {
			if entry[i] == '=' {
				if allowed[entry[:i]] {
					out = append(out, entry)
				}
				break
			}
		}
	}
	return out
}
