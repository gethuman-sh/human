package devcontainer

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type memStore struct {
	files     map[string][]byte
	writeSeen int
}

func (m *memStore) ReadFile(name string) ([]byte, error) {
	data, ok := m.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (m *memStore) WriteFile(name string, data []byte, _ os.FileMode) error {
	m.writeSeen++
	m.files[name] = data
	return nil
}

const docPath = ".devcontainer/devcontainer.json"

func loadDoc(t *testing.T, content string) (*Document, *memStore) {
	t.Helper()
	store := &memStore{files: map[string][]byte{docPath: []byte(content)}}
	doc, err := LoadDocument(store, docPath)
	require.NoError(t, err)
	return doc, store
}

// devcontainer.json is JSONC and the file is the user's. Decoding it into a map
// and re-marshalling returned it with the comments gone and the keys in
// alphabetical order — so an edit of one key rewrote the whole file.
func TestDocument_CommentsAndKeyOrderSurviveAnEdit(t *testing.T) {
	original := `{
  // the image everything else hangs off
  "name": "human",
  "image": "mcr.microsoft.com/devcontainers/go:1",
  "features": {
    // node is needed for the CLI
    "ghcr.io/devcontainers/features/node:1": {}
  },
  "forwardPorts": [8080]
}`
	doc, store := loadDoc(t, original)

	require.NoError(t, doc.AddFeature("ghcr.io/gethuman-sh/treehouse/human:1"))
	require.NoError(t, doc.Save())

	written := string(store.files[docPath])
	assert.Contains(t, written, "// the image everything else hangs off", "comments survive")
	assert.Contains(t, written, "// node is needed for the CLI")
	assert.Less(t, strings.Index(written, `"name"`), strings.Index(written, `"image"`), "key order survives")
	assert.Less(t, strings.Index(written, `"features"`), strings.Index(written, `"forwardPorts"`))
	assert.True(t, gjson.Get(written, `features.ghcr\.io/gethuman-sh/treehouse/human:1`).Exists(),
		"and the feature is actually added")
	assert.True(t, gjson.Get(written, `features.ghcr\.io/devcontainers/features/node:1`).Exists(),
		"next to the one already there")
}

// A commented file used to be unreadable: the feature writer failed on it and
// the LSP writer skipped it, both because encoding/json rejects JSONC.
func TestDocument_ACommentedFileIsReadable(t *testing.T) {
	doc, _ := loadDoc(t, "{\n  // just a comment\n  \"name\": \"human\"\n}")
	assert.False(t, doc.HasFeature("ghcr.io/gethuman-sh/treehouse/human:1"))
	require.NoError(t, doc.AddFeature("ghcr.io/gethuman-sh/treehouse/human:1"))
	assert.True(t, doc.HasFeature("ghcr.io/gethuman-sh/treehouse/human:1"))
}

// postStartCommand's array form is a single command's argv. The old code read
// it as absent and wrote over it, silently deleting whatever the user ran on
// every container start.
func TestDocument_AnArgvPostStartCommandIsRefusedNotReplaced(t *testing.T) {
	original := `{"name":"human","postStartCommand":["bash","-lc","./scripts/setup.sh"]}`
	doc, store := loadDoc(t, original)

	err := doc.AppendPostStartCommand("human-lsp", "npm i -g gopls")
	require.Error(t, err, "there is nothing to append to an argv array without changing what it means")
	assert.False(t, doc.Changed())
	require.NoError(t, doc.Save())
	assert.Zero(t, store.writeSeen, "the user's command is left exactly as it was")
	assert.Equal(t, original, string(store.files[docPath]))
}

// The object form is the spec's own way to have more than one post-start
// command, so ours joins it under its own name instead of replacing anything.
func TestDocument_AnObjectPostStartCommandGainsANamedEntry(t *testing.T) {
	doc, store := loadDoc(t, `{"postStartCommand":{"setup":"./scripts/setup.sh"}}`)

	require.NoError(t, doc.AppendPostStartCommand("human-lsp", "npm i -g gopls"))
	require.NoError(t, doc.Save())

	written := string(store.files[docPath])
	assert.Equal(t, "./scripts/setup.sh", gjson.Get(written, "postStartCommand.setup").String())
	assert.Equal(t, "npm i -g gopls", gjson.Get(written, "postStartCommand.human-lsp").String())
}

func TestDocument_AStringPostStartCommandIsExtended(t *testing.T) {
	doc, store := loadDoc(t, `{"postStartCommand":"./scripts/setup.sh"}`)

	require.NoError(t, doc.AppendPostStartCommand("human-lsp", "npm i -g gopls"))
	require.NoError(t, doc.Save())

	assert.Equal(t, "./scripts/setup.sh && npm i -g gopls",
		gjson.Get(string(store.files[docPath]), "postStartCommand").String())
}

func TestDocument_AnAbsentPostStartCommandIsSet(t *testing.T) {
	doc, store := loadDoc(t, `{"name":"human"}`)

	require.NoError(t, doc.AppendPostStartCommand("human-lsp", "npm i -g gopls"))
	require.NoError(t, doc.Save())

	assert.Equal(t, "npm i -g gopls", gjson.Get(string(store.files[docPath]), "postStartCommand").String())
}

// Running the wizard twice must change nothing the second time, in any of the
// shapes — and a document nothing changed is not written at all.
func TestDocument_EditsAreIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		edit    func(*Document) error
	}{
		{
			name:    "feature already declared",
			content: `{"features":{"ghcr.io/gethuman-sh/treehouse/human:1":{}}}`,
			edit:    func(d *Document) error { return d.AddFeature("ghcr.io/gethuman-sh/treehouse/human:1") },
		},
		{
			name:    "command already in the string",
			content: `{"postStartCommand":"npm i -g gopls"}`,
			edit:    func(d *Document) error { return d.AppendPostStartCommand("human-lsp", "npm i -g gopls") },
		},
		{
			name:    "command already named",
			content: `{"postStartCommand":{"human-lsp":"npm i -g gopls"}}`,
			edit:    func(d *Document) error { return d.AppendPostStartCommand("human-lsp", "npm i -g gopls") },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, store := loadDoc(t, tc.content)

			require.NoError(t, tc.edit(doc))
			assert.False(t, doc.Changed(), "the edit is already in the file")
			require.NoError(t, doc.Save())
			assert.Zero(t, store.writeSeen)
			assert.Equal(t, tc.content, string(store.files[docPath]))
		})
	}
}

// A file that is not JSON at all is reported rather than replaced with one that
// is — the same rule as every other document human edits but does not own.
func TestDocument_ABrokenFileIsReportedNotReplaced(t *testing.T) {
	store := &memStore{files: map[string][]byte{docPath: []byte(`{"name": `)}}
	_, err := LoadDocument(store, docPath)
	require.Error(t, err)
	assert.Zero(t, store.writeSeen)
}
