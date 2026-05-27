package chrome

import (
	"encoding/binary"
	"io"

	"github.com/gethuman-sh/human/errors"
)

// MaxMessageSize is Chrome's native messaging limit (1 MB).
const MaxMessageSize = 1024 * 1024

// ReadMessage reads a Chrome native messaging frame: 4-byte LE length + JSON payload.
func ReadMessage(r io.Reader) ([]byte, error) {
	var length uint32
	if err := binary.Read(r, binary.LittleEndian, &length); err != nil {
		return nil, errors.WrapWithDetails(err, "reading message length")
	}

	if length > MaxMessageSize {
		return nil, errors.WithDetails("message too large: %d bytes", "size", int(length))
	}

	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, errors.WrapWithDetails(err, "reading message body")
	}

	return buf, nil
}

// WriteMessage writes a Chrome native messaging frame: 4-byte LE length + payload.
func WriteMessage(w io.Writer, data []byte) error {
	if len(data) > MaxMessageSize {
		return errors.WithDetails("message too large: %d bytes", "size", len(data))
	}

	length := uint32(len(data)) // #nosec G115 -- guarded by MaxMessageSize check above
	if err := binary.Write(w, binary.LittleEndian, length); err != nil {
		return errors.WrapWithDetails(err, "writing message length")
	}

	if _, err := w.Write(data); err != nil {
		return errors.WrapWithDetails(err, "writing message body")
	}

	return nil
}
