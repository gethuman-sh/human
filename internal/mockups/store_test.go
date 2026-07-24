package mockups

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "mockups.json"))
}

func TestPathIn(t *testing.T) {
	assert.Equal(t, filepath.Join("proj", ".human", "mockups.json"), PathIn("proj"))
}

func TestStore_SetAndAll_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	created := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	require.NoError(t, s.Set("SC-123", Entry{Slug: "sc-123", Created: created}))
	require.NoError(t, s.Set("SC-456", Entry{Slug: "sc-456", Created: created}))

	assert.Equal(t, map[string]Entry{
		"SC-123": {Slug: "sc-123", Created: created},
		"SC-456": {Slug: "sc-456", Created: created},
	}, s.All())
}

func TestStore_Set_OverwritesExisting(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-123", Entry{Slug: "old"}))
	require.NoError(t, s.Set("SC-123", Entry{Slug: "new"}))

	assert.Equal(t, "new", s.All()["SC-123"].Slug)
}

func TestStore_All_MissingFile(t *testing.T) {
	s := newTestStore(t)
	assert.Empty(t, s.All())
}

func TestStore_All_CorruptFile(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, os.WriteFile(s.path, []byte("{not json"), 0o600))
	assert.Empty(t, s.All())
}

func TestStore_All_FutureVersion(t *testing.T) {
	// A future format must read as empty rather than misinterpreting fields.
	s := newTestStore(t)
	data := `{"version":2,"mocks":{"SC-1":{"slug":"sc-1"}}}`
	require.NoError(t, os.WriteFile(s.path, []byte(data), 0o600))
	assert.Empty(t, s.All())
}

func TestStore_Delete_RemovesEntry(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-123", Entry{Slug: "sc-123"}))
	require.NoError(t, s.Set("SC-456", Entry{Slug: "sc-456"}))
	require.NoError(t, s.Delete("SC-123"))

	all := s.All()
	assert.NotContains(t, all, "SC-123")
	assert.Contains(t, all, "SC-456")
}

func TestStore_Delete_MissingFileIsNoOp(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Delete("SC-123"))

	_, err := os.Stat(s.path)
	assert.True(t, os.IsNotExist(err), "delete must never create the file")
}

func TestStore_Delete_MissingKeyIsNoOp(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-456", Entry{Slug: "sc-456"}))
	require.NoError(t, s.Delete("SC-123"))

	assert.Contains(t, s.All(), "SC-456")
}

func TestStore_ConcurrentSets_LoseNoEntry(t *testing.T) {
	s := newTestStore(t)
	keys := []string{"SC-1", "SC-2", "SC-3", "SC-4", "SC-5"}

	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			assert.NoError(t, s.Set(k, Entry{Slug: SlugFor(k)}))
		}(key)
	}
	wg.Wait()

	assert.Len(t, s.All(), len(keys))
}

func TestStore_Choose_PreservesSetLink(t *testing.T) {
	s := newTestStore(t)
	created := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	require.NoError(t, s.Set("SC-1", Entry{Slug: "sc-1", Created: created}))
	require.NoError(t, s.Choose("SC-1", Choice{Slug: "sc-1-o3-v1", File: "02.html"}))

	e := s.All()["SC-1"]
	assert.Equal(t, "sc-1", e.Slug)
	assert.Equal(t, created, e.Created)
	require.NotNil(t, e.Chosen)
	assert.Equal(t, Choice{Slug: "sc-1-o3-v1", File: "02.html"}, *e.Chosen)
}

func TestStore_ClearChoice_RemovesWinner(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-1", Entry{Slug: "sc-1"}))
	require.NoError(t, s.Choose("SC-1", Choice{Slug: "sc-1-o3-v1", File: "02.html"}))
	require.NoError(t, s.ClearChoice("SC-1"))

	_, ok := s.ChosenFor("SC-1")
	assert.False(t, ok)
	assert.Equal(t, "sc-1", s.All()["SC-1"].Slug, "set link must survive clearing the winner")
}

func TestStore_ClearChoice_MissingIsNoOp(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.ClearChoice("SC-1"))

	_, err := os.Stat(s.path)
	assert.True(t, os.IsNotExist(err), "clearing a missing choice must never create the file")
}

func TestStore_ChosenFor_AbsentAndPresent(t *testing.T) {
	s := newTestStore(t)

	require.NoError(t, s.Set("SC-1", Entry{Slug: "sc-1"}))
	_, ok := s.ChosenFor("SC-1")
	assert.False(t, ok)

	require.NoError(t, s.Choose("SC-1", Choice{Slug: "sc-1-o1-v1", File: "01.html"}))
	c, ok := s.ChosenFor("SC-1")
	assert.True(t, ok)
	assert.Equal(t, Choice{Slug: "sc-1-o1-v1", File: "01.html"}, c)
}

func TestStore_All_OldFileWithoutChosen(t *testing.T) {
	// A pre-winner mockups.json (no "chosen" key) must read back unchanged.
	s := newTestStore(t)
	data := `{"version":1,"mocks":{"SC-1":{"slug":"sc-1"}}}`
	require.NoError(t, os.WriteFile(s.path, []byte(data), 0o600))

	e := s.All()["SC-1"]
	assert.Equal(t, "sc-1", e.Slug)
	assert.Nil(t, e.Chosen)
}

func TestSlugFor(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"SC-123", "sc-123"},
		{"HUM 42!", "hum-42"},
		{"sc-7", "sc-7"},
		{"--SC--9--", "sc-9"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, SlugFor(tt.key), "SlugFor(%q)", tt.key)
	}
}
