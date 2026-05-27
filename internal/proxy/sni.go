package proxy

import (
	"fmt"
	"io"
	"net"

	"github.com/gethuman-sh/human/errors"
)

const (
	tlsRecordHandshake   = 0x16
	handshakeClientHello = 0x01
	extServerName        = 0x0000
	sniHostNameType      = 0x00
)

// PeekClientHello reads the TLS ClientHello from conn without consuming it.
// Returns the peeked bytes (to replay to upstream) and the extracted SNI hostname.
func PeekClientHello(conn net.Conn) (peeked []byte, serverName string, err error) {
	// Read 5-byte TLS record header.
	header := make([]byte, 5) //nolint:mnd // TLS record header is always 5 bytes
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, "", errors.WrapWithDetails(err, "reading TLS record header")
	}

	if header[0] != tlsRecordHandshake {
		return nil, "", errors.WithDetails("not a TLS handshake record", "recordType", fmt.Sprintf("0x%02x", header[0]))
	}

	recordLen := int(header[3])<<8 | int(header[4])
	if recordLen == 0 || recordLen > 16384 { //nolint:mnd // TLS max record size
		return nil, "", errors.WithDetails("invalid TLS record length", "length", recordLen)
	}

	body := make([]byte, recordLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, "", errors.WrapWithDetails(err, "reading TLS record body")
	}

	peeked = append(header, body...)

	serverName, err = parseClientHello(body)
	if err != nil {
		return peeked, "", err
	}

	return peeked, serverName, nil
}

// parseClientHello extracts the SNI hostname from a ClientHello handshake message.
func parseClientHello(data []byte) (string, error) {
	if len(data) < 1 || data[0] != handshakeClientHello {
		return "", errors.WithDetails("not a ClientHello", "handshakeType", fmt.Sprintf("0x%02x", safeFirst(data)))
	}

	// Skip: handshake type (1) + length (3) + client version (2) + random (32)
	pos := 1 + 3 + 2 + 32 //nolint:mnd // fixed ClientHello field sizes
	if pos >= len(data) {
		return "", errors.WithDetails("ClientHello too short for session ID")
	}

	// Skip session ID (length-prefixed, 1-byte length).
	sessionIDLen := int(data[pos])
	pos += 1 + sessionIDLen

	// Skip cipher suites (length-prefixed, 2-byte length).
	if pos+2 > len(data) {
		return "", errors.WithDetails("ClientHello too short for cipher suites")
	}
	cipherLen := int(data[pos])<<8 | int(data[pos+1])
	pos += 2 + cipherLen

	// Skip compression methods (length-prefixed, 1-byte length).
	if pos >= len(data) {
		return "", errors.WithDetails("ClientHello too short for compression methods")
	}
	compLen := int(data[pos])
	pos += 1 + compLen

	// Extensions (length-prefixed, 2-byte length).
	if pos+2 > len(data) {
		return "", nil // no extensions, no SNI
	}
	extLen := int(data[pos])<<8 | int(data[pos+1])
	pos += 2

	end := pos + extLen
	if end > len(data) {
		return "", errors.WithDetails("extensions length exceeds record")
	}

	for pos+4 <= end {
		extType := int(data[pos])<<8 | int(data[pos+1])
		extDataLen := int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4

		if pos+extDataLen > end {
			return "", errors.WithDetails("extension data exceeds record")
		}

		if extType == extServerName {
			return parseSNIExtension(data[pos : pos+extDataLen])
		}

		pos += extDataLen
	}

	return "", nil // no SNI extension found
}

// parseSNIExtension extracts the hostname from the server_name extension data.
func parseSNIExtension(data []byte) (string, error) {
	if len(data) < 2 { //nolint:mnd // server name list length
		return "", errors.WithDetails("SNI extension too short")
	}

	// Skip server name list length (2 bytes).
	listLen := int(data[0])<<8 | int(data[1])
	data = data[2:]

	if len(data) < listLen || listLen == 0 {
		return "", errors.WithDetails("SNI list length mismatch")
	}

	// Parse entries.
	pos := 0
	for pos+3 <= listLen { //nolint:mnd // name type (1) + name length (2)
		nameType := data[pos]
		nameLen := int(data[pos+1])<<8 | int(data[pos+2])
		pos += 3

		if pos+nameLen > listLen || pos+nameLen > len(data) {
			return "", errors.WithDetails("SNI name length exceeds list")
		}

		if nameType == sniHostNameType {
			return string(data[pos : pos+nameLen]), nil
		}

		pos += nameLen
	}

	return "", nil
}

func safeFirst(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}
