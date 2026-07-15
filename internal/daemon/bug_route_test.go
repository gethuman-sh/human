package daemon

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gethuman-sh/human/errors"
)

func startBugServer(t *testing.T, token string, creator func(BugCreateRequest) (BugCreateResponse, error)) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:       "127.0.0.1:0",
		Token:      token,
		CmdFactory: echoCmd,
		Logger:     zerolog.Nop(),
		BugCreator: creator,
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

func TestHandleBugCreateNilCreator(t *testing.T) {
	token := "tok"
	addr := startBugServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"bug-create", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "bug creation not available")
}

func TestHandleBugCreateValid(t *testing.T) {
	token := "tok"
	var gotReq BugCreateRequest
	addr := startBugServer(t, token, func(req BugCreateRequest) (BugCreateResponse, error) {
		gotReq = req
		return BugCreateResponse{Key: "SC-1", URL: "https://tracker.example/SC-1"}, nil
	})

	body, _ := json.Marshal(BugCreateRequest{Title: "Board loses cards", Description: "Cards vanish on refresh"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"bug-create", string(body)}})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)

	var out BugCreateResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &out))
	assert.Equal(t, "SC-1", out.Key)
	assert.Equal(t, "https://tracker.example/SC-1", out.URL)

	// The one-JSON-arg protocol exists so free text survives arg splitting —
	// the creator must see the multi-word title and description verbatim.
	assert.Equal(t, "Board loses cards", gotReq.Title)
	assert.Equal(t, "Cards vanish on refresh", gotReq.Description)
}

func TestHandleBugCreateCreatorError(t *testing.T) {
	token := "tok"
	addr := startBugServer(t, token, func(BugCreateRequest) (BugCreateResponse, error) {
		return BugCreateResponse{}, errors.WithDetails("tracker unavailable")
	})

	body, _ := json.Marshal(BugCreateRequest{Title: "Board loses cards"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"bug-create", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "tracker unavailable")
}

func TestHandleBugCreateBadJSON(t *testing.T) {
	token := "tok"
	addr := startBugServer(t, token, func(BugCreateRequest) (BugCreateResponse, error) {
		return BugCreateResponse{Key: "SC-1"}, nil
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"bug-create", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid bug-create request")
}

func TestHandleBugCreateEmptyTitle(t *testing.T) {
	token := "tok"
	addr := startBugServer(t, token, func(BugCreateRequest) (BugCreateResponse, error) {
		return BugCreateResponse{Key: "SC-1"}, nil
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"bug-create", `{"title":"   "}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "bug title must not be empty")
}

func TestHandleBugCreateWrongArgCount(t *testing.T) {
	token := "tok"
	addr := startBugServer(t, token, func(BugCreateRequest) (BugCreateResponse, error) {
		return BugCreateResponse{Key: "SC-1"}, nil
	})
	for name, args := range map[string][]string{
		"no arg":   {"bug-create"},
		"two args": {"bug-create", "{}", "{}"},
	} {
		resp := sendRequest(t, addr, Request{Token: token, Args: args})
		assert.Equal(t, 1, resp.ExitCode, "case %s", name)
		assert.Contains(t, resp.Stderr, "bug-create requires one JSON arg", "case %s", name)
	}
}

func TestValidateBugCreate(t *testing.T) {
	tests := []struct {
		name    string
		req     BugCreateRequest
		wantErr bool
	}{
		{"valid title", BugCreateRequest{Title: "Board loses cards"}, false},
		{"title with description", BugCreateRequest{Title: "T", Description: "D"}, false},
		{"empty title", BugCreateRequest{}, true},
		// Whitespace-only titles would create an unnameable ticket, so
		// trimming decides emptiness.
		{"whitespace title", BugCreateRequest{Title: " \t\n "}, true},
		{"description alone is not enough", BugCreateRequest{Description: "D"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBugCreate(tt.req)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "bug title must not be empty")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
