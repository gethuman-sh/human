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

func startSecurityServer(t *testing.T, token string, creator func(SecurityCreateRequest) (SecurityCreateResponse, error)) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv := &Server{
		Addr:            "127.0.0.1:0",
		Token:           token,
		CmdFactory:      echoCmd,
		Logger:          zerolog.Nop(),
		SecurityCreator: creator,
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

func TestHandleSecurityCreateNilCreator(t *testing.T) {
	token := "tok"
	addr := startSecurityServer(t, token, nil)
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"security-create", "{}"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "security creation not available")
}

func TestHandleSecurityCreateValid(t *testing.T) {
	token := "tok"
	var gotReq SecurityCreateRequest
	addr := startSecurityServer(t, token, func(req SecurityCreateRequest) (SecurityCreateResponse, error) {
		gotReq = req
		return SecurityCreateResponse{Key: "SC-2", URL: "https://tracker.example/SC-2"}, nil
	})

	body, _ := json.Marshal(SecurityCreateRequest{Title: "Auth token leaks in logs", Description: "PII exposed on error path"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"security-create", string(body)}})
	require.Equal(t, 0, resp.ExitCode, "stderr: %s", resp.Stderr)

	var out SecurityCreateResponse
	require.NoError(t, json.Unmarshal([]byte(resp.Stdout), &out))
	assert.Equal(t, "SC-2", out.Key)
	assert.Equal(t, "https://tracker.example/SC-2", out.URL)

	// The one-JSON-arg protocol exists so free text survives arg splitting —
	// the creator must see the multi-word title and description verbatim.
	assert.Equal(t, "Auth token leaks in logs", gotReq.Title)
	assert.Equal(t, "PII exposed on error path", gotReq.Description)
}

func TestHandleSecurityCreateCreatorError(t *testing.T) {
	token := "tok"
	addr := startSecurityServer(t, token, func(SecurityCreateRequest) (SecurityCreateResponse, error) {
		return SecurityCreateResponse{}, errors.WithDetails("tracker unavailable")
	})

	body, _ := json.Marshal(SecurityCreateRequest{Title: "Auth token leaks in logs"})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"security-create", string(body)}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "tracker unavailable")
}

func TestHandleSecurityCreateBadJSON(t *testing.T) {
	token := "tok"
	addr := startSecurityServer(t, token, func(SecurityCreateRequest) (SecurityCreateResponse, error) {
		return SecurityCreateResponse{Key: "SC-2"}, nil
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"security-create", "not json"}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "invalid security-create request")
}

func TestHandleSecurityCreateEmptyTitle(t *testing.T) {
	token := "tok"
	addr := startSecurityServer(t, token, func(SecurityCreateRequest) (SecurityCreateResponse, error) {
		return SecurityCreateResponse{Key: "SC-2"}, nil
	})
	resp := sendRequest(t, addr, Request{Token: token, Args: []string{"security-create", `{"title":"   "}`}})
	assert.Equal(t, 1, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "security title must not be empty")
}

func TestHandleSecurityCreateWrongArgCount(t *testing.T) {
	token := "tok"
	addr := startSecurityServer(t, token, func(SecurityCreateRequest) (SecurityCreateResponse, error) {
		return SecurityCreateResponse{Key: "SC-2"}, nil
	})
	for name, args := range map[string][]string{
		"no arg":   {"security-create"},
		"two args": {"security-create", "{}", "{}"},
	} {
		resp := sendRequest(t, addr, Request{Token: token, Args: args})
		assert.Equal(t, 1, resp.ExitCode, "case %s", name)
		assert.Contains(t, resp.Stderr, "security-create requires one JSON arg", "case %s", name)
	}
}

func TestValidateSecurityCreate(t *testing.T) {
	tests := []struct {
		name    string
		req     SecurityCreateRequest
		wantErr bool
	}{
		{"valid title", SecurityCreateRequest{Title: "Auth token leaks"}, false},
		{"title with description", SecurityCreateRequest{Title: "T", Description: "D"}, false},
		{"empty title", SecurityCreateRequest{}, true},
		{"whitespace title", SecurityCreateRequest{Title: " \t\n "}, true},
		{"description alone is not enough", SecurityCreateRequest{Description: "D"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSecurityCreate(tt.req)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "security title must not be empty")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
