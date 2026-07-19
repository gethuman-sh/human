package proxy

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubDecider returns a fixed answer for all hostnames.
type stubDecider struct {
	allowed bool
}

func (s *stubDecider) Allowed(_ string) bool { return s.allowed }

func TestInteractiveDecider_baseAllowed(t *testing.T) {
	promptCalled := false
	d := NewInteractiveDecider(&stubDecider{allowed: true}, func(_ string) (bool, error) {
		promptCalled = true
		return false, nil
	})

	assert.True(t, d.Allowed("example.com"))
	assert.False(t, promptCalled, "prompt should not be called when base allows")
}

func TestInteractiveDecider_promptAllowed(t *testing.T) {
	d := NewInteractiveDecider(&stubDecider{allowed: false}, func(_ string) (bool, error) {
		return true, nil
	})

	assert.True(t, d.Allowed("example.com"))
}

func TestInteractiveDecider_promptDenied(t *testing.T) {
	d := NewInteractiveDecider(&stubDecider{allowed: false}, func(_ string) (bool, error) {
		return false, nil
	})

	assert.False(t, d.Allowed("example.com"))
}

func TestInteractiveDecider_cacheHit(t *testing.T) {
	var calls atomic.Int32
	d := NewInteractiveDecider(&stubDecider{allowed: false}, func(_ string) (bool, error) {
		calls.Add(1)
		return true, nil
	})

	assert.True(t, d.Allowed("example.com"))
	assert.True(t, d.Allowed("example.com"))
	assert.Equal(t, int32(1), calls.Load(), "prompt should be called only once")
}

func TestInteractiveDecider_concurrentSameDomain(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{})
	proceed := make(chan struct{})

	d := NewInteractiveDecider(&stubDecider{allowed: false}, func(_ string) (bool, error) {
		calls.Add(1)
		close(started)
		<-proceed
		return true, nil
	})

	var wg sync.WaitGroup
	results := make([]bool, 2)

	wg.Go(func() {
		results[0] = d.Allowed("example.com")
	})

	<-started // first goroutine is inside prompt

	wg.Go(func() {
		results[1] = d.Allowed("example.com")
	})

	// Let the second goroutine reach the pending wait.
	// A small sleep is acceptable here since we need the goroutine to enter Wait().
	// The correctness doesn't depend on timing — worst case the second goroutine
	// hasn't entered Wait() yet and the test still passes.
	close(proceed)
	wg.Wait()

	assert.True(t, results[0])
	assert.True(t, results[1])
	assert.Equal(t, int32(1), calls.Load(), "prompt should be called only once")
}

func TestInteractiveDecider_promptError(t *testing.T) {
	d := NewInteractiveDecider(&stubDecider{allowed: false}, func(_ string) (bool, error) {
		return true, io.ErrUnexpectedEOF
	})

	assert.False(t, d.Allowed("example.com"), "should default to deny on prompt error")
}

func TestInteractiveDecider_differentDomains(t *testing.T) {
	prompted := make(map[string]bool)
	var mu sync.Mutex

	d := NewInteractiveDecider(&stubDecider{allowed: false}, func(h string) (bool, error) {
		mu.Lock()
		prompted[h] = true
		mu.Unlock()
		return h == "allow.com", nil
	})

	assert.True(t, d.Allowed("allow.com"))
	assert.False(t, d.Allowed("deny.com"))
	assert.True(t, prompted["allow.com"])
	assert.True(t, prompted["deny.com"])
}

func TestTerminalPrompt_yes(t *testing.T) {
	in := strings.NewReader("y\n")
	out := &bytes.Buffer{}
	prompt := NewTerminalPrompt(in, out)

	allowed, err := prompt("example.com")
	require.NoError(t, err)
	assert.True(t, allowed)
	assert.Contains(t, out.String(), "Allow connections to example.com?")
}

func TestTerminalPrompt_yesLong(t *testing.T) {
	in := strings.NewReader("yes\n")
	out := &bytes.Buffer{}
	prompt := NewTerminalPrompt(in, out)

	allowed, err := prompt("example.com")
	require.NoError(t, err)
	assert.True(t, allowed)
}

func TestTerminalPrompt_no(t *testing.T) {
	in := strings.NewReader("n\n")
	out := &bytes.Buffer{}
	prompt := NewTerminalPrompt(in, out)

	allowed, err := prompt("example.com")
	require.NoError(t, err)
	assert.False(t, allowed)
}

func TestTerminalPrompt_defaultNo(t *testing.T) {
	in := strings.NewReader("\n")
	out := &bytes.Buffer{}
	prompt := NewTerminalPrompt(in, out)

	allowed, err := prompt("example.com")
	require.NoError(t, err)
	assert.False(t, allowed, "empty input should default to deny")
}

func TestTerminalPrompt_eof(t *testing.T) {
	in := strings.NewReader("")
	out := &bytes.Buffer{}
	prompt := NewTerminalPrompt(in, out)

	_, err := prompt("example.com")
	assert.ErrorIs(t, err, io.EOF)
}
