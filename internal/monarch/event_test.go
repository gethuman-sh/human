package monarch

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewDaemonID_format(t *testing.T) {
	re := regexp.MustCompile(`^daemon-[0-9a-f]{8}$`)

	id1, err := NewDaemonID()
	require.NoError(t, err)
	assert.Regexp(t, re, id1)

	id2, err := NewDaemonID()
	require.NoError(t, err)
	assert.Regexp(t, re, id2)

	assert.NotEqual(t, id1, id2, "two ids must differ")
}
