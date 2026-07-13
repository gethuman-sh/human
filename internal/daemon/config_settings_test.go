package daemon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/internal/settings"
)

const settingsFixture = `# keep this comment
project: human-cli

linears:
  - name: work
    token: 1pw://Development/Linear Token/token
    projects: [HUM]

shortcuts:
  - name: human
    token: sc-plaintext-secret
`

func settingsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".humanconfig.yaml"), []byte(settingsFixture), 0o644))
	return dir
}

func decodeDoc(t *testing.T, resp Response) settings.Doc {
	t.Helper()
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)
	var doc settings.Doc
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &doc))
	return doc
}

func docValue(t *testing.T, doc settings.Doc, path string) settings.Value {
	t.Helper()
	for _, v := range doc.Values {
		if v.Path == path {
			return v
		}
	}
	t.Fatalf("no value with path %q", path)
	return settings.Value{}
}

func TestServer_configGetMasksSecrets(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	dir := settingsDir(t)

	resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleConfigGet(conn, dir) })
	doc := decodeDoc(t, resp)

	assert.Equal(t, settings.Masked, docValue(t, doc, "shortcuts.human.token").Value)
	assert.Equal(t, "1pw://Development/Linear Token/token", docValue(t, doc, "linears.work.token").Value)
	assert.Equal(t, "human-cli", docValue(t, doc, "project").Value)
	// The raw literal secret must not appear anywhere in the payload.
	assert.NotContains(t, resp.Stdout, "sc-plaintext-secret")
}

func TestServer_configGetEmptyDir(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	dir := t.TempDir()

	resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleConfigGet(conn, dir) })
	doc := decodeDoc(t, resp)
	assert.False(t, doc.Exists)
}

func TestServer_configSetWritesAndReturnsSnapshot(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	dir := settingsDir(t)

	req, err := json.Marshal(SetConfigRequest{Path: "linears.work.projects", Value: json.RawMessage(`["HUM","OPS"]`)})
	require.NoError(t, err)
	resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleConfigSet(conn, []string{string(req)}, dir) })
	doc := decodeDoc(t, resp)

	assert.Equal(t, []any{"HUM", "OPS"}, docValue(t, doc, "linears.work.projects").Value)

	// Comments and untouched values survive on disk.
	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "# keep this comment")
	assert.Contains(t, string(data), "sc-plaintext-secret")
}

func TestServer_configSetRejectsMaskedSentinel(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	dir := settingsDir(t)

	req, err := json.Marshal(SetConfigRequest{Path: "shortcuts.human.token", Value: json.RawMessage(`"` + settings.Masked + `"`)})
	require.NoError(t, err)
	resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleConfigSet(conn, []string{string(req)}, dir) })

	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "masked")
	data, err := os.ReadFile(filepath.Join(dir, ".humanconfig.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "sc-plaintext-secret")
}

func TestServer_configSetBadInput(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	dir := settingsDir(t)

	cases := map[string][]string{
		"no arg":       {},
		"invalid json": {"{not json"},
		"bad path":     {`{"path":"nosuch.section","value":"x"}`},
		"bad value":    {`{"path":"linears.work.safe","value":"yes"}`},
	}
	for name, args := range cases {
		resp := captureHandlerResponse(t, func(conn net.Conn) { srv.handleConfigSet(conn, args, dir) })
		assert.Equal(t, 1, resp.ExitCode, "case %s", name)
		assert.NotEmpty(t, resp.Stderr, "case %s", name)
	}
}

func TestServer_configRoutesRegistered(t *testing.T) {
	srv := &Server{Logger: zerolog.Nop()}
	dir := settingsDir(t)

	serverConn, clientConn := net.Pipe()
	done := make(chan bool, 1)
	go func() {
		defer func() { _ = serverConn.Close() }()
		done <- srv.routeSimpleCommand(serverConn, []string{"config-get"}, dir, 0)
	}()
	// Drain the response so the handler's write does not block the pipe.
	buf := make([]byte, 64*1024)
	_, _ = clientConn.Read(buf)
	_ = clientConn.Close()
	assert.True(t, <-done, "config-get must be routed")
}
