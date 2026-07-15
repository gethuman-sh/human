package devcontainer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCA generates a real self-signed certificate and writes it as PEM to
// dir/ca.crt, returning the file path. Tests that assert the CA mount is
// present need a genuinely PEM-parseable certificate now that the mount is
// gated on IsValidCACertFile.
func writeTestCA(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "human test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIsValidCACertFile(t *testing.T) {
	dir := t.TempDir()

	validPath := writeTestCA(t, dir)

	emptyPath := filepath.Join(dir, "empty.crt")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	nonPEMPath := filepath.Join(dir, "notpem.crt")
	if err := os.WriteFile(nonPEMPath, []byte("cert-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	dirPath := filepath.Join(dir, "cadir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"valid PEM", validPath, true},
		{"directory", dirPath, false},
		{"empty file", emptyPath, false},
		{"non-PEM", nonPEMPath, false},
		{"missing", filepath.Join(dir, "does-not-exist.crt"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidCACertFile(tc.path); got != tc.want {
				t.Errorf("IsValidCACertFile(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
