package vault

import (
	"github.com/awnumar/memguard"
	"github.com/rs/zerolog/log"
)

// sealed holds a cached secret away from the ordinary Go heap.
//
// The cached copy is the one worth protecting: it is the only plaintext that
// outlives a single call, sitting in the daemon for the whole TTL, and it is
// therefore what a core file or a swap page would capture. memguard keeps it
// encrypted at rest, decrypting into locked, guard-paged memory that is
// excluded from core dumps only for as long as a read takes.
//
// What this deliberately does NOT cover is the value handed back to callers.
// Resolve returns a string, and every consumer — the config plumbing for seven
// trackers — takes strings, so the returned plaintext lands on the heap like
// any other. Threading []byte through that path is a far larger change, and
// half-doing it would buy the appearance of protection rather than protection.
type sealed struct {
	// enclave holds the value when memguard could lock memory for it.
	enclave *memguard.Enclave
	// plain holds it when memguard could not, so a host that refuses locked
	// memory still resolves secrets. See seal.
	plain string
}

// seal protects val, falling back to holding it plainly when the platform will
// not give us locked memory.
//
// memguard panics when it cannot lock — a low RLIMIT_MEMLOCK, which is ordinary
// inside containers and on some hosts — and a daemon that dies on startup
// because of a memory limit would be a far worse outcome than a cached secret
// living on the heap, which is exactly where it lived before this existed.
// Recovering is safe by memguard's own contract: its panic purges the session
// and rekeys first.
//
// The purge is worth knowing about: it destroys every enclave, not just the one
// that failed. Other cached secrets are lost, and simply resolve again.
func seal(val string) (s sealed) {
	defer func() {
		if r := recover(); r != nil {
			log.Warn().Interface("cause", r).
				Msg("vault: could not lock memory for the cached secret; holding it on the heap instead")
			s = sealed{plain: val}
		}
	}()
	// NewEnclave takes ownership of the slice and wipes it, so the copy made
	// here does not outlive the call.
	return sealed{enclave: memguard.NewEnclave([]byte(val))}
}

// open returns the plaintext. The locked buffer it decrypts into is destroyed
// before returning, so the protected copy exists only for this call.
func (s sealed) open() (string, bool) {
	if s.enclave == nil {
		return s.plain, s.plain != ""
	}
	buf, err := s.enclave.Open()
	if err != nil {
		// An enclave that cannot be opened is not a value we may serve: failing
		// closed here sends the caller to the provider, which is correct, rather
		// than handing back an empty secret that would fail obscurely later.
		log.Warn().Err(err).Msg("vault: cached secret could not be unsealed; resolving it again")
		return "", false
	}
	defer buf.Destroy()
	return string(buf.Bytes()), true
}
