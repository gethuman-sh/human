package gui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProxyLogMode_RoundTrip(t *testing.T) {
	lm := ProxyLogMode{}
	original := lm.Get()
	defer func() { _ = lm.Set(original) }()

	require.NoError(t, lm.Set("full"))
	assert.Equal(t, "full", lm.Get())

	require.NoError(t, lm.Set("meta"))
	assert.Equal(t, "meta", lm.Get())

	require.NoError(t, lm.Set("off"))
	assert.Equal(t, "off", lm.Get())
}

func TestProxyLogMode_Invalid(t *testing.T) {
	assert.Error(t, ProxyLogMode{}.Set("bogus"))
}
