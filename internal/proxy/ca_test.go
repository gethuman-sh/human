package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateCA_generatesNew(t *testing.T) {
	dir := t.TempDir()

	cert, key, tlsCert, err := LoadOrCreateCA(dir)
	require.NoError(t, err)
	require.NotNil(t, cert)
	require.NotNil(t, key)
	require.NotNil(t, tlsCert)

	assert.True(t, cert.IsCA)
	assert.Equal(t, "human proxy CA", cert.Subject.CommonName)
	assert.Equal(t, []string{"human daemon"}, cert.Subject.Organization)
	assert.True(t, cert.NotAfter.After(time.Now().AddDate(9, 0, 0)))

	// Files written.
	_, err = os.Stat(filepath.Join(dir, "ca.crt"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "ca.key"))
	assert.NoError(t, err)

	// Key file has restricted permissions.
	info, err := os.Stat(filepath.Join(dir, "ca.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestLoadOrCreateCA_loadsExisting(t *testing.T) {
	dir := t.TempDir()

	// Generate once.
	cert1, _, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)

	// Load again — should return same cert.
	cert2, _, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)

	assert.Equal(t, cert1.SerialNumber, cert2.SerialNumber)
	assert.Equal(t, cert1.Subject.CommonName, cert2.Subject.CommonName)
}

func TestLeafCache_Get(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)

	cache := &LeafCache{CACert: caCert, CAKey: caKey}

	leaf, err := cache.Get("api.anthropic.com")
	require.NoError(t, err)
	require.NotNil(t, leaf)
	require.NotNil(t, leaf.Leaf)

	assert.Equal(t, "api.anthropic.com", leaf.Leaf.Subject.CommonName)
	assert.Contains(t, leaf.Leaf.DNSNames, "api.anthropic.com")
	assert.True(t, leaf.Leaf.NotAfter.After(time.Now().Add(23*time.Hour)))

	// Verify signed by CA.
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	_, err = leaf.Leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	assert.NoError(t, err)
}

func TestLeafCache_cacheHit(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)

	cache := &LeafCache{CACert: caCert, CAKey: caKey}

	leaf1, err := cache.Get("example.com")
	require.NoError(t, err)

	leaf2, err := cache.Get("example.com")
	require.NoError(t, err)

	// Same pointer — cached.
	assert.Equal(t, leaf1, leaf2)
}

func TestLeafCache_differentDomains(t *testing.T) {
	dir := t.TempDir()
	caCert, caKey, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)

	cache := &LeafCache{CACert: caCert, CAKey: caKey}

	leaf1, err := cache.Get("a.example.com")
	require.NoError(t, err)

	leaf2, err := cache.Get("b.example.com")
	require.NoError(t, err)

	assert.NotEqual(t, leaf1.Leaf.SerialNumber, leaf2.Leaf.SerialNumber)
}

func TestLoadOrCreateCA_refusesWhenOnlyCertExists(t *testing.T) {
	dir := t.TempDir()

	// Seed only ca.crt — leave ca.key missing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("dummy"), 0o600))

	_, _, _, err := LoadOrCreateCA(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both exist or both be missing")
}

func TestLoadOrCreateCA_refusesWhenOnlyKeyExists(t *testing.T) {
	dir := t.TempDir()

	// Seed only ca.key — leave ca.crt missing.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.key"), []byte("dummy"), 0o600))

	_, _, _, err := LoadOrCreateCA(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "both exist or both be missing")
}

// TestLeafCache_capacityBound asserts that flooding the cache with more
// distinct hostnames than the configured capacity does not grow it
// without bound — older entries are evicted.
func TestLeafCache_capacityBound(t *testing.T) {
	dir := t.TempDir()
	cert, key, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)
	cache := &LeafCache{CACert: cert, CAKey: key}

	// Generate cap+50 unique entries.
	total := leafCacheCapacity + 50
	for i := range total {
		_, err := cache.Get(fmt.Sprintf("host-%d.example.com", i))
		require.NoError(t, err)
	}

	cache.mu.Lock()
	defer cache.mu.Unlock()
	assert.LessOrEqual(t, cache.lru.Len(), leafCacheCapacity)
	assert.LessOrEqual(t, len(cache.entries), leafCacheCapacity)
}

// TestLeafCache_concurrentCollapsesGenerations asserts that when many
// goroutines request the same hostname concurrently, the underlying
// generator is invoked exactly once.
func TestLeafCache_concurrentCollapsesGenerations(t *testing.T) {
	dir := t.TempDir()
	cert, key, _, err := LoadOrCreateCA(dir)
	require.NoError(t, err)
	cache := &LeafCache{CACert: cert, CAKey: key}

	var wg sync.WaitGroup
	const goroutines = 50
	results := make([]*tls.Certificate, goroutines)
	for i := range goroutines {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			c, err := cache.Get("burst.example.com")
			require.NoError(t, err)
			results[idx] = c
		}(i)
	}
	wg.Wait()

	// All goroutines must observe the same cached certificate.
	for i := 1; i < goroutines; i++ {
		assert.Same(t, results[0], results[i], "all callers should receive the same cert")
	}
}
