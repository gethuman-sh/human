package vault

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Sealing must be invisible to every caller: the secret that goes in is the
// secret that comes out. Asserted on behaviour, never on whether locked memory
// was actually obtained — CI hosts and containers routinely refuse it, and a
// test written the other way would be red there and green here.
func TestSealed_roundTripsTheSecret(t *testing.T) {
	s := seal("shortcut-token")

	val, ok := s.open()

	require.True(t, ok)
	assert.Equal(t, "shortcut-token", val)
}

// Opening is repeatable: the cache is read once per resolve for a whole TTL,
// so a value readable only once would break on the second call.
func TestSealed_opensRepeatedly(t *testing.T) {
	s := seal("token")

	for range 5 {
		val, ok := s.open()
		require.True(t, ok)
		assert.Equal(t, "token", val)
	}
}

// A long secret is not truncated by the page-based allocation underneath.
func TestSealed_handlesALongSecret(t *testing.T) {
	long := strings.Repeat("k", 9000)

	val, ok := seal(long).open()

	require.True(t, ok)
	assert.Equal(t, long, val)
}

// The fallback path — what a host that refuses locked memory falls back to —
// must serve the value rather than an empty string.
func TestSealed_plainFallbackStillServes(t *testing.T) {
	val, ok := sealed{plain: "token"}.open()

	require.True(t, ok)
	assert.Equal(t, "token", val)
}

// An empty sealed value reports absence rather than handing back "", which
// would fail somewhere less obvious than at the read.
func TestSealed_emptyReportsAbsence(t *testing.T) {
	_, ok := sealed{}.open()

	assert.False(t, ok)
}

// End to end through the resolver: sealing changes nothing a caller can see,
// including the one-approval-per-TTL contract that motivated the cache.
func TestResolver_sealedCacheKeepsTheOneApprovalContract(t *testing.T) {
	calls := 0
	provider := &fakeProvider{
		canResolve: func(string) bool { return true },
		resolve: func(string) (string, error) {
			calls++
			return "sealed-token", nil
		},
	}
	r := NewResolver(provider)
	r.ttl = time.Minute

	for range 3 {
		val, err := r.Resolve("1pw://vault/item/field")
		require.NoError(t, err)
		assert.Equal(t, "sealed-token", val)
	}

	assert.Equal(t, 1, calls)
}
