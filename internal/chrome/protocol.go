package chrome

import (
	"bufio"
	"encoding/json"
	"io"

	"github.com/gethuman-sh/human/errors"
)

// proxyRequest is the auth handshake sent by the client to the chrome proxy server.
type proxyRequest struct {
	Token   string `json:"token"`
	Version string `json:"version,omitempty"`
}

// ProxyAck is sent by the chrome proxy server to acknowledge a connection request.
type ProxyAck struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// sendProxyRequest writes a proxyRequest as a JSON line to w.
func sendProxyRequest(w io.Writer, token, version string) error {
	req := proxyRequest{Token: token, Version: version}
	if err := json.NewEncoder(w).Encode(req); err != nil {
		return errors.WrapWithDetails(err, "sending proxy request")
	}
	return nil
}

// readProxyAck reads a ProxyAck JSON line from r.
func readProxyAck(r io.Reader) (ProxyAck, error) {
	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return ProxyAck{}, errors.WrapWithDetails(err, "reading from chrome proxy")
		}
		return ProxyAck{}, errors.WithDetails("no response from chrome proxy")
	}
	var ack ProxyAck
	if err := json.Unmarshal(scanner.Bytes(), &ack); err != nil {
		return ProxyAck{}, errors.WrapWithDetails(err, "parsing proxy ack")
	}
	return ack, nil
}
