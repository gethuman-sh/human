package devcontainer

import (
	"crypto/x509"
	"encoding/pem"
	"os"
)

// IsValidCACertFile reports whether path is a regular, non-empty file whose
// contents decode as a PEM-encoded X.509 certificate. Docker auto-creates a
// missing bind source as an empty directory; mounting that (or an empty /
// non-PEM file) makes Node's NODE_EXTRA_CA_CERTS parse fail, so we refuse to
// emit a bind for anything that is not a real certificate.
func IsValidCACertFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is the well-known ~/.human/ca.crt
	if err != nil {
		return false
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return false
	}
	_, err = x509.ParseCertificate(block.Bytes)
	return err == nil
}
