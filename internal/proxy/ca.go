package proxy

import (
	"container/list"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gethuman-sh/human/errors"
)

const (
	caValidityYears   = 10
	leafValidityHours = 24
)

// LoadOrCreateCA loads an existing CA certificate and key from dir, or generates
// a new self-signed CA if none exists. Returns the parsed certificate, private key,
// and a tls.Certificate ready for signing leaf certs.
func LoadOrCreateCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, *tls.Certificate, error) {
	certPath := filepath.Join(dir, "ca.crt")
	keyPath := filepath.Join(dir, "ca.key")

	certPEM, certErr := os.ReadFile(certPath) // #nosec G304 -- dir is from ~/.human/
	keyPEM, keyErr := os.ReadFile(keyPath)    // #nosec G304 -- dir is from ~/.human/

	certExists := certErr == nil
	keyExists := keyErr == nil
	certMissing := os.IsNotExist(certErr)
	keyMissing := os.IsNotExist(keyErr)

	// Non-ENOENT read errors on either file are real failures and must
	// propagate so we never silently replace something the user trusts.
	if certErr != nil && !certMissing {
		return nil, nil, nil, errors.WrapWithDetails(certErr, "reading CA cert", "path", certPath)
	}
	if keyErr != nil && !keyMissing {
		return nil, nil, nil, errors.WrapWithDetails(keyErr, "reading CA key", "path", keyPath)
	}

	// Both files present: parse and return the existing CA.
	if certExists && keyExists {
		return parseCA(certPEM, keyPEM)
	}

	// Exactly one of the files is present. Refuse to continue — the user
	// either intentionally removed one and should be made aware, or this
	// is a partial restore we must not silently complete.
	if certExists != keyExists {
		return nil, nil, nil, errors.WithDetails(
			"CA cert and key must both exist or both be missing",
			"certPath", certPath, "certExists", certExists,
			"keyPath", keyPath, "keyExists", keyExists)
	}

	return generateCA(dir, certPath, keyPath)
}

// generateCA creates a new self-signed CA and writes it to disk.
func generateCA(dir, certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, *tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "generating CA key")
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "generating serial number")
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"human daemon"},
			CommonName:   "human proxy CA",
		},
		NotBefore:             now,
		NotAfter:              now.AddDate(caValidityYears, 0, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "creating CA certificate")
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "parsing CA certificate")
	}

	if err := writeCAFiles(dir, certPath, keyPath, certDER, key); err != nil {
		return nil, nil, nil, err
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        cert,
	}

	return cert, key, tlsCert, nil
}

// writeCAFiles persists the CA certificate and key as PEM files.
func writeCAFiles(dir, certPath, keyPath string, certDER []byte, key *ecdsa.PrivateKey) error {
	certPEMBlock := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return errors.WrapWithDetails(err, "marshalling CA key")
	}
	keyPEMBlock := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errors.WrapWithDetails(err, "creating CA directory", "dir", dir)
	}
	if err := os.WriteFile(certPath, certPEMBlock, 0o644); err != nil { // #nosec G306 -- CA cert must be readable for trust installation
		return errors.WrapWithDetails(err, "writing CA cert", "path", certPath)
	}
	if err := os.WriteFile(keyPath, keyPEMBlock, 0o600); err != nil {
		return errors.WrapWithDetails(err, "writing CA key", "path", keyPath)
	}
	return nil
}

// parseCA parses PEM-encoded certificate and key bytes into a CA.
func parseCA(certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey, *tls.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, nil, errors.WithDetails("failed to decode CA cert PEM")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "parsing CA cert")
	}

	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		return nil, nil, nil, errors.WithDetails("failed to decode CA key PEM")
	}

	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, nil, errors.WrapWithDetails(err, "parsing CA key")
	}

	tlsCert := &tls.Certificate{
		Certificate: [][]byte{block.Bytes},
		PrivateKey:  key,
		Leaf:        cert,
	}

	return cert, key, tlsCert, nil
}

// leafCacheCapacity caps the number of cached leaf certificates so a flood
// of unique SNIs cannot grow the cache without bound.
const leafCacheCapacity = 1024

// leafCacheEntry tracks a cached cert and its position in the LRU list.
type leafCacheEntry struct {
	hostname string
	cert     *tls.Certificate
	elem     *list.Element
}

// leafGenResult is the result of a single in-flight cert generation, used
// to fold concurrent callers requesting the same hostname onto one
// generation call (manual singleflight).
type leafGenResult struct {
	done chan struct{}
	cert *tls.Certificate
	err  error
}

// LeafCache generates and caches per-domain TLS certificates signed by a CA.
// Cache size is bounded; concurrent requests for the same hostname collapse
// onto a single generation call.
type LeafCache struct {
	CACert *x509.Certificate
	CAKey  *ecdsa.PrivateKey

	mu      sync.Mutex
	entries map[string]*leafCacheEntry
	lru     *list.List // front = most recently used
	pending map[string]*leafGenResult
}

// Get returns a cached leaf certificate for hostname, or generates a new one.
func (lc *LeafCache) Get(hostname string) (*tls.Certificate, error) {
	lc.mu.Lock()
	if lc.entries == nil {
		lc.entries = make(map[string]*leafCacheEntry)
		lc.lru = list.New()
		lc.pending = make(map[string]*leafGenResult)
	}
	if entry, ok := lc.entries[hostname]; ok {
		if entry.cert.Leaf != nil && time.Now().Before(entry.cert.Leaf.NotAfter) {
			lc.lru.MoveToFront(entry.elem)
			cert := entry.cert
			lc.mu.Unlock()
			return cert, nil
		}
		// Expired — remove and fall through to regenerate.
		lc.lru.Remove(entry.elem)
		delete(lc.entries, hostname)
	}

	// If another goroutine is already generating a cert for this hostname,
	// wait for it instead of doing duplicate work.
	if pending, ok := lc.pending[hostname]; ok {
		lc.mu.Unlock()
		<-pending.done
		return pending.cert, pending.err
	}

	gen := &leafGenResult{done: make(chan struct{})}
	lc.pending[hostname] = gen
	lc.mu.Unlock()

	cert, err := generateLeafCert(lc.CACert, lc.CAKey, hostname)

	lc.mu.Lock()
	delete(lc.pending, hostname)
	if err == nil {
		// Insert into the cache, evicting the least recently used entry
		// if we exceeded the capacity.
		entry := &leafCacheEntry{hostname: hostname, cert: cert}
		entry.elem = lc.lru.PushFront(hostname)
		lc.entries[hostname] = entry
		for lc.lru.Len() > leafCacheCapacity {
			oldest := lc.lru.Back()
			if oldest == nil {
				break
			}
			lc.lru.Remove(oldest)
			delete(lc.entries, oldest.Value.(string)) //nolint:errcheck // type guaranteed by store
		}
	}
	gen.cert = cert
	gen.err = err
	close(gen.done)
	lc.mu.Unlock()

	return cert, err
}

// generateLeafCert creates a short-lived TLS certificate for hostname, signed by the CA.
func generateLeafCert(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, hostname string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "generating leaf key", "hostname", hostname)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, errors.WrapWithDetails(err, "generating leaf serial", "hostname", hostname)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: hostname,
		},
		DNSNames:  []string{hostname},
		NotBefore: now.Add(-5 * time.Minute), // small clock skew tolerance
		NotAfter:  now.Add(leafValidityHours * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "creating leaf cert", "hostname", hostname)
	}

	leafCert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, errors.WrapWithDetails(err, "parsing leaf cert", "hostname", hostname)
	}

	return &tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        leafCert,
	}, nil
}
