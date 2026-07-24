package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startCreateMocksServer(t *testing.T, token string, creator func(CreateMocksRequest) error) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:           "127.0.0.1:0",
		Token:          token,
		CmdFactory:     echoCmd,
		Logger:         zerolog.Nop(),
		MockupsCreator: creator,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr
}

func TestHandleCreateMocksNilCreator(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "mock creation not available")
}

func TestHandleCreateMocksValid(t *testing.T) {
	token := "tok"
	var got CreateMocksRequest
	addr := startCreateMocksServer(t, token, func(req CreateMocksRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(CreateMocksRequest{PMKey: "SC-1", PMTitle: "Dark mode", Description: "ctx"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "SC-1", got.PMKey)
	assert.Equal(t, "Dark mode", got.PMTitle)
	assert.Equal(t, "ctx", got.Description)
}

func TestHandleCreateMocksBadArg(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, func(CreateMocksRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid create-mocks request")
}

func TestHandleCreateMocksMissingArg(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, func(CreateMocksRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "requires one JSON arg")
}

func TestHandleCreateMocksCreatorError(t *testing.T) {
	token := "tok"
	addr := startCreateMocksServer(t, token, func(CreateMocksRequest) error {
		return errors.New("launch failed")
	})
	body, _ := json.Marshal(CreateMocksRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-mocks", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "launch failed")
}

// The create-mocks route must NOT trip the interactive destructive-confirm
// gate; the context-menu click is the consent, matching features-generate.
func TestDetectDestructiveBypassesCreateMocks(t *testing.T) {
	_, ok := detectDestructive([]string{"create-mocks", `{"pm_key":"SC-1"}`})
	assert.False(t, ok)
}

func startCreateVariationsServer(t *testing.T, token string, creator func(CreateVariationsRequest) error) string {
	t.Helper()
	return startRouteServer(t, &Server{Token: token, VariationsCreator: creator})
}

func startChooseMockupServer(t *testing.T, token string, chooser func(ChooseMockupRequest) error) string {
	t.Helper()
	return startRouteServer(t, &Server{Token: token, MockupChooser: chooser})
}

func startPruneMockupServer(t *testing.T, token string, pruner func(PruneMockupRequest) error) string {
	t.Helper()
	return startRouteServer(t, &Server{Token: token, MockupPruner: pruner})
}

// startRouteServer boots a Server with the caller's injected fields already set
// and waits until it accepts connections, factoring the boilerplate the mockup
// route tests share.
func startRouteServer(t *testing.T, srv *Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv.Addr = "127.0.0.1:0"
	srv.CmdFactory = echoCmd
	srv.Logger = zerolog.Nop()
	ln, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	addr := ln.Addr().String()
	_ = ln.Close()
	srv.Addr = addr
	go func() { _ = srv.ListenAndServe(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, derr := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if derr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(cancel)
	return addr
}

func TestHandleCreateVariationsNilCreator(t *testing.T) {
	token := "tok"
	addr := startCreateVariationsServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-variations", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "variation creation not available")
}

func TestHandleCreateVariationsValid(t *testing.T) {
	token := "tok"
	var got CreateVariationsRequest
	addr := startCreateVariationsServer(t, token, func(req CreateVariationsRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(CreateVariationsRequest{
		PMKey: "SC-1", Feature: "Dark mode", ParentSlug: "sc-1", ParentFile: "03-foo.html", Instructions: "make it blue",
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-variations", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "sc-1", got.ParentSlug)
	assert.Equal(t, "make it blue", got.Instructions)
}

func TestHandleCreateVariationsBadArg(t *testing.T) {
	token := "tok"
	addr := startCreateVariationsServer(t, token, func(CreateVariationsRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-variations", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid create-variations request")
}

func TestHandleCreateVariationsMissingArg(t *testing.T) {
	token := "tok"
	addr := startCreateVariationsServer(t, token, func(CreateVariationsRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-variations"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "requires one JSON arg")
}

func TestHandleCreateVariationsCreatorError(t *testing.T) {
	token := "tok"
	addr := startCreateVariationsServer(t, token, func(CreateVariationsRequest) error {
		return errors.New("launch failed")
	})
	body, _ := json.Marshal(CreateVariationsRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"create-variations", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "launch failed")
}

func TestHandleChooseMockupNilChooser(t *testing.T) {
	token := "tok"
	addr := startChooseMockupServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"choose-mockup", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "mockup selection not available")
}

func TestHandleChooseMockupValid(t *testing.T) {
	token := "tok"
	var got ChooseMockupRequest
	addr := startChooseMockupServer(t, token, func(req ChooseMockupRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(ChooseMockupRequest{PMKey: "SC-1", Slug: "sc-1-o3-v1", File: "02.html"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"choose-mockup", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "sc-1-o3-v1", got.Slug)
	assert.Equal(t, "02.html", got.File)
}

func TestHandleChooseMockupBadArg(t *testing.T) {
	token := "tok"
	addr := startChooseMockupServer(t, token, func(ChooseMockupRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"choose-mockup", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid choose-mockup request")
}

func TestHandleChooseMockupMissingArg(t *testing.T) {
	token := "tok"
	addr := startChooseMockupServer(t, token, func(ChooseMockupRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"choose-mockup"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "requires one JSON arg")
}

func TestHandleChooseMockupChooserError(t *testing.T) {
	token := "tok"
	addr := startChooseMockupServer(t, token, func(ChooseMockupRequest) error {
		return errors.New("choose failed")
	})
	body, _ := json.Marshal(ChooseMockupRequest{PMKey: "SC-1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"choose-mockup", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "choose failed")
}

func TestHandlePruneMockupNilPruner(t *testing.T) {
	token := "tok"
	addr := startPruneMockupServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"prune-mockup", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "mockup pruning not available")
}

func TestHandlePruneMockupValid(t *testing.T) {
	token := "tok"
	var got PruneMockupRequest
	addr := startPruneMockupServer(t, token, func(req PruneMockupRequest) error {
		got = req
		return nil
	})
	body, _ := json.Marshal(PruneMockupRequest{PMKey: "SC-1", Slug: "sc-1-o3-v1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"prune-mockup", string(body)}})
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "ok\n", resp.Stdout)
	assert.Equal(t, "sc-1-o3-v1", got.Slug)
}

func TestHandlePruneMockupBadArg(t *testing.T) {
	token := "tok"
	addr := startPruneMockupServer(t, token, func(PruneMockupRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"prune-mockup", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid prune-mockup request")
}

func TestHandlePruneMockupMissingArg(t *testing.T) {
	token := "tok"
	addr := startPruneMockupServer(t, token, func(PruneMockupRequest) error { return nil })
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"prune-mockup"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "requires one JSON arg")
}

func TestHandlePruneMockupPrunerError(t *testing.T) {
	token := "tok"
	addr := startPruneMockupServer(t, token, func(PruneMockupRequest) error {
		return errors.New("prune failed")
	})
	body, _ := json.Marshal(PruneMockupRequest{PMKey: "SC-1", Slug: "sc-1-o3-v1"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"prune-mockup", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "prune failed")
}

// The three new mockup routes are non-destructive host-state edits — the click
// is consent — so they must bypass the interactive destructive-confirm gate.
func TestDetectDestructiveBypassesCreateVariations(t *testing.T) {
	_, ok := detectDestructive([]string{"create-variations", `{"pm_key":"SC-1"}`})
	assert.False(t, ok)
}

func TestDetectDestructiveBypassesChooseMockup(t *testing.T) {
	_, ok := detectDestructive([]string{"choose-mockup", `{"pm_key":"SC-1"}`})
	assert.False(t, ok)
}

func TestDetectDestructiveBypassesPruneMockup(t *testing.T) {
	_, ok := detectDestructive([]string{"prune-mockup", `{"pm_key":"SC-1"}`})
	assert.False(t, ok)
}
