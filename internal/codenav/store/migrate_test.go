package store_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/gethuman-sh/human/internal/codenav/store"
)

// legacyRouteSchema is the route table as it existed before SC-1274 added the
// file_id column and its index — the exact shape found on databases in the
// field that trip the migration path.
// Only route is seeded: it is the table whose column moved. schema.sql creates
// every other table fresh (with its full column set), so seeding a pre-file_id
// route alone reproduces the exact "route exists but lacks file_id" state that
// tripped idx_route_file, without hand-copying the whole legacy schema.
const legacyRouteSchema = `
CREATE TABLE route (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  method     TEXT,
  pattern    TEXT,
  handler_id INTEGER,
  framework  TEXT
);
CREATE INDEX idx_route_proj ON route(project_id);
`

// TestOpenMigratesPreFileIDDatabase guards the ordering bug that turned the
// board's codenav LED red: schema.sql creates idx_route_file over route(file_id),
// so applying it against a pre-file_id database must not fail before the column
// migration runs. Open must heal such a database instead of erroring out.
func TestOpenMigratesPreFileIDDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	// Seed a database with the old route table (no file_id, no idx_route_file).
	seed, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(legacyRouteSchema); err != nil {
		t.Fatalf("seed legacy schema: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	// Open used to return "apply schema: no such column: file_id" here.
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open on pre-file_id database: %v", err)
	}
	defer func() { _ = st.Close() }()

	// The migration must have added file_id so route is queryable through it.
	if _, err := st.DB().Exec(`SELECT file_id FROM route LIMIT 1`); err != nil {
		t.Fatalf("route.file_id still missing after Open: %v", err)
	}
	if _, err := st.ListProjects(); err != nil {
		t.Fatalf("ListProjects after migration: %v", err)
	}
}
