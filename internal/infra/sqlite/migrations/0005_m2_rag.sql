-- M2.1 知识与嵌入：任务队列、文档、切分块、FTS5 索引、向量。
-- 注意：迁移执行器按行尾分号切分，DDL 必须单行写完。

CREATE TABLE IF NOT EXISTS jobs (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  kind         TEXT    NOT NULL,
  payload      TEXT    NOT NULL DEFAULT '{}',
  dedupe_key   TEXT    NOT NULL DEFAULT '',
  status       TEXT    NOT NULL DEFAULT 'pending',
  priority     INTEGER NOT NULL DEFAULT 0,
  attempts     INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  last_error   TEXT    NOT NULL DEFAULT '',
  run_after    INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  started_at   INTEGER,
  finished_at  INTEGER
);

CREATE INDEX IF NOT EXISTS idx_jobs_pick ON jobs(status, run_after, priority DESC, id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_jobs_dedupe ON jobs(kind, dedupe_key) WHERE dedupe_key <> '' AND status IN ('pending', 'running');

CREATE TABLE IF NOT EXISTS documents (
  id            TEXT PRIMARY KEY,
  source        TEXT    NOT NULL,
  title         TEXT    NOT NULL DEFAULT '',
  mime          TEXT    NOT NULL DEFAULT '',
  sha256        TEXT    NOT NULL,
  size_bytes    INTEGER NOT NULL DEFAULT 0,
  char_count    INTEGER NOT NULL DEFAULT 0,
  status        TEXT    NOT NULL DEFAULT 'pending',
  error         TEXT    NOT NULL DEFAULT '',
  embed_model   TEXT    NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_documents_sha ON documents(sha256);

CREATE TABLE IF NOT EXISTS chunks (
  id            TEXT PRIMARY KEY,
  document_id   TEXT    NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  ordinal       INTEGER NOT NULL,
  heading_path  TEXT    NOT NULL DEFAULT '',
  kind          TEXT    NOT NULL DEFAULT 'para',
  text          TEXT    NOT NULL,
  char_start    INTEGER NOT NULL DEFAULT 0,
  char_end      INTEGER NOT NULL DEFAULT 0,
  page          INTEGER NOT NULL DEFAULT 0,
  token_est     INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks(document_id, ordinal);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(chunk_id UNINDEXED, text_bigram, tokenize='unicode61');

CREATE TABLE IF NOT EXISTS embeddings (
  owner_kind TEXT    NOT NULL,
  owner_id   TEXT    NOT NULL,
  model      TEXT    NOT NULL,
  dim        INTEGER NOT NULL,
  vec        BLOB    NOT NULL,
  norm       REAL    NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (owner_kind, owner_id, model)
);

CREATE INDEX IF NOT EXISTS idx_embeddings_model ON embeddings(model, owner_kind);

CREATE TABLE IF NOT EXISTS kb_settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
