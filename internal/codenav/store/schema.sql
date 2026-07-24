-- codenav schema. Pure-SQLite (FTS5), driven by modernc.org/sqlite.
-- All DDL is idempotent (IF NOT EXISTS) so it doubles as the migration.

CREATE TABLE IF NOT EXISTS project (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  root_path  TEXT NOT NULL,
  vcs_rev    TEXT,
  indexed_at INTEGER,
  source_sig TEXT
);

CREATE TABLE IF NOT EXISTS file (
  id           INTEGER PRIMARY KEY,
  project_id   INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  path         TEXT NOT NULL,
  lang         TEXT,
  content_hash TEXT NOT NULL,
  fidelity     TEXT,
  -- size+mtime are the cheap change-detection fingerprint: an incremental
  -- refresh stats each file and only re-hashes when these differ.
  mtime        INTEGER,
  size         INTEGER,
  UNIQUE(project_id, path)
);

CREATE TABLE IF NOT EXISTS symbol (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  file_id    INTEGER NOT NULL REFERENCES file(id) ON DELETE CASCADE,
  qname      TEXT NOT NULL,
  name       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  signature  TEXT,
  doc        TEXT,
  start_line INTEGER, start_col INTEGER,
  end_line   INTEGER, end_col   INTEGER
);

CREATE TABLE IF NOT EXISTS reference (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  symbol_id  INTEGER REFERENCES symbol(id) ON DELETE CASCADE,
  qname      TEXT NOT NULL,
  file_id    INTEGER NOT NULL REFERENCES file(id) ON DELETE CASCADE,
  line       INTEGER, col INTEGER,
  role       TEXT
);

CREATE TABLE IF NOT EXISTS edge (
  src_id     INTEGER NOT NULL REFERENCES symbol(id) ON DELETE CASCADE,
  dst_id     INTEGER NOT NULL REFERENCES symbol(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  confidence REAL,
  PRIMARY KEY (src_id, dst_id, kind)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS route (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  method     TEXT,
  pattern    TEXT,
  handler_id INTEGER REFERENCES symbol(id),
  framework  TEXT,
  -- file_id ties a route to its registration site so an incremental refresh can
  -- drop and recompute just that file's routes.
  file_id    INTEGER REFERENCES file(id) ON DELETE CASCADE
);

-- Full-text search: BM25 over code bodies and over symbol names.
CREATE VIRTUAL TABLE IF NOT EXISTS fts_code USING fts5(
  body, path UNINDEXED, project UNINDEXED,
  tokenize='unicode61 remove_diacritics 2'
);
CREATE VIRTUAL TABLE IF NOT EXISTS fts_symbol USING fts5(
  name, qname, kind UNINDEXED, symbol_id UNINDEXED, project UNINDEXED
);

CREATE INDEX IF NOT EXISTS idx_symbol_qname ON symbol(qname);
CREATE INDEX IF NOT EXISTS idx_symbol_name  ON symbol(name);
CREATE INDEX IF NOT EXISTS idx_symbol_file  ON symbol(file_id);
CREATE INDEX IF NOT EXISTS idx_symbol_proj  ON symbol(project_id);
CREATE INDEX IF NOT EXISTS idx_ref_symbol   ON reference(symbol_id);
CREATE INDEX IF NOT EXISTS idx_ref_qname    ON reference(qname);
CREATE INDEX IF NOT EXISTS idx_edge_src     ON edge(src_id, kind);
CREATE INDEX IF NOT EXISTS idx_edge_dst     ON edge(dst_id, kind);
CREATE INDEX IF NOT EXISTS idx_route_proj   ON route(project_id);
CREATE INDEX IF NOT EXISTS idx_route_file   ON route(file_id);
