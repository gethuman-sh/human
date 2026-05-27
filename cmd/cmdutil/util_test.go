package cmdutil

import (
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
)

func TestTruncateRunes(t *testing.T) {
	t.Run("short string returned unchanged", func(t *testing.T) {
		assert.Equal(t, "hello", TruncateRunes("hello", 10))
	})

	t.Run("exact length returned unchanged", func(t *testing.T) {
		assert.Equal(t, "hello", TruncateRunes("hello", 5))
	})

	t.Run("ascii truncated with ellipsis", func(t *testing.T) {
		assert.Equal(t, "hel...", TruncateRunes("hello world", 3))
	})

	t.Run("never splits a multi-byte rune", func(t *testing.T) {
		// 5 emoji (4 bytes each); a byte slice at 3 would split rune 1.
		got := TruncateRunes("😀😀😀😀😀", 3)
		assert.Equal(t, "😀😀😀...", got)
		// Truncated portion must remain valid UTF-8 (no replacement glyphs).
		assert.True(t, utf8.ValidString(got))
	})

	t.Run("negative max treated as zero", func(t *testing.T) {
		assert.Equal(t, "...", TruncateRunes("abc", -1))
	})
}
