package chrome

import (
	"encoding/json"
	"io"

	"github.com/gethuman-sh/human/errors"
)

// maxHandshakeLine bounds the auth/ack handshake line so a misbehaving peer
// cannot stream unbounded bytes before the newline.
const maxHandshakeLine = 64 * 1024

// readHandshakeLine reads a single newline-terminated line one byte at a time.
// Unlike bufio.Scanner it never buffers past the newline, so bytes belonging to
// the subsequent stream stay on the connection for the long-lived session that
// follows the handshake.
func readHandshakeLine(r io.Reader) ([]byte, error) {
	buf := make([]byte, 0, 256)
	b := make([]byte, 1)
	for {
		n, err := r.Read(b)
		if n > 0 {
			if b[0] == '\n' {
				return buf, nil
			}
			buf = append(buf, b[0])
			if len(buf) > maxHandshakeLine {
				return nil, errors.WithDetails("handshake line exceeds limit", "limit", maxHandshakeLine)
			}
		}
		if err != nil {
			if err == io.EOF && len(buf) > 0 {
				return buf, nil
			}
			return nil, err
		}
	}
}

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

// readProxyAck reads a ProxyAck JSON line from r without over-reading past the
// line, so any stream that follows the ack stays on the connection.
func readProxyAck(r io.Reader) (ProxyAck, error) {
	line, err := readHandshakeLine(r)
	if err != nil {
		return ProxyAck{}, errors.WrapWithDetails(err, "reading from chrome proxy")
	}
	var ack ProxyAck
	if err := json.Unmarshal(line, &ack); err != nil {
		return ProxyAck{}, errors.WrapWithDetails(err, "parsing proxy ack")
	}
	return ack, nil
}
