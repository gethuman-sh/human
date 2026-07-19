package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJitteredInterval_withinBounds(t *testing.T) {
	const d = 2 * time.Minute
	const fraction = 0.5
	lo := time.Duration(float64(d) * (1 - fraction))
	hi := time.Duration(float64(d) * (1 + fraction))
	for i := 0; i < 1000; i++ {
		j := jitteredInterval(d, fraction)
		assert.GreaterOrEqual(t, j, lo)
		assert.LessOrEqual(t, j, hi)
	}
}

func TestJitteredInterval_zeroFraction(t *testing.T) {
	d := 2 * time.Minute
	assert.Equal(t, d, jitteredInterval(d, 0))
	assert.Equal(t, d, jitteredInterval(d, -1))
}

func TestJitteredInterval_neverNegative(t *testing.T) {
	// A fraction > 1 can push the raw perturbation below zero; the floor keeps
	// the returned wait non-negative so time.After never panics.
	for i := 0; i < 1000; i++ {
		assert.GreaterOrEqual(t, jitteredInterval(time.Second, 2.0), time.Duration(0))
	}
}
