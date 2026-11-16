-- M2.2 记忆：语义事实、情节、候选、事件日志、画像卡。
-- 注意：迁移执行器按行尾分号切分，DDL 必须单行写完。

-- facts：语义记忆，按 (subject_key, predicate_key) 维护当前真值并保留版本链。
CREATE TABLE IF NOT EXISTS facts (
  id             TEXT PRIMARY KEY,
  subject        TEXT NOT NULL,
  subject_key    TEXT NOT NULL,
  predicate      TEXT NOT NULL,
  predicate_key  TEXT NOT NULL,
  value          TEXT NOT NULL,
  value_key      TEXT NOT NULL,
  -- active | superseded | disputed | archived | deleted
  status         TEXT NOT NULL DEFAULT 'active',
  confidence     REAL NOT NULL DEFAULT 0.8,
  importance     REAL NOT NULL DEFAULT 0.5,
  confirmations  INTEGER NOT NULL DEFAULT 1,
  access_count   INTEGER NOT NULL DEFAULT 0,
  last_access_at INTEGER,
  -- explicit | turn | session_end | imported | external
  source_type    TEXT NOT NULL DEFAULT 'turn',
  source_ref     TEXT NOT NULL DEFAULT '',
  session_id     TEXT NOT NULL DEFAULT '',
  turn_id        TEXT NOT NULL DEFAULT '',
  valid_from     INTEGER NOT NULL,
  valid_to       INTEGER,
  superseded_by  TEXT,
  deleted_at     INTEGER,
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_facts_active ON facts(subject_key, predicate_key) WHERE status = 'active' AND deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_facts_subject ON facts(subject_key, status);

CREATE INDEX IF NOT EXISTS idx_facts_source ON facts(source_type, created_at);

-- episodes：情节记忆（一段对话的摘要 + 可检索要素）。
CREATE TABLE IF NOT EXISTS episodes (
  id            TEXT PRIMARY KEY,
  session_id    TEXT NOT NULL,
  turn_id       TEXT NOT NULL DEFAULT '',
  summary       TEXT NOT NULL,
  -- 关键词用空格分隔，供纯 SQL 召回打分
  keywords      TEXT NOT NULL DEFAULT '',
  entities      TEXT NOT NULL DEFAULT '',
  importance    REAL NOT NULL DEFAULT 0.5,
  access_count  INTEGER NOT NULL DEFAULT 0,
  last_access_at INTEGER,
  created_at    INTEGER NOT NULL,
  -- 归档后不再预召回，但仍可被 memory_search 查到
  archived_at   INTEGER,
  deleted_at    INTEGER
);

CREATE INDEX IF NOT EXISTS idx_episodes_session ON episodes(session_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_episodes_live ON episodes(archived_at, deleted_at, created_at DESC);

-- memory_candidates：抽取后待合并的候选。
-- 保留它是为了让「为什么这条没写进记忆」可追溯。
CREATE TABLE IF NOT EXISTS memory_candidates (
  id           TEXT PRIMARY KEY,
  session_id   TEXT NOT NULL,
  turn_id      TEXT NOT NULL DEFAULT '',
  subject      TEXT NOT NULL,
  predicate    TEXT NOT NULL,
  value        TEXT NOT NULL,
  source_type  TEXT NOT NULL,
  confidence   REAL NOT NULL DEFAULT 0.5,
  -- pending | inserted | duplicate | updated | contradicted | rejected
  resolution   TEXT NOT NULL DEFAULT 'pending',
  resolved_id  TEXT,
  reason       TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_candidates_turn ON memory_candidates(turn_id);

-- memory_events：审计日志，支撑撤销与溯源。
CREATE TABLE IF NOT EXISTS memory_events (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  fact_id     TEXT NOT NULL DEFAULT '',
  episode_id  TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL,
  actor       TEXT NOT NULL DEFAULT 'system',
  detail      TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_events_fact ON memory_events(fact_id, id);

-- profile_card：渲染好的画像卡行，只在 facts 变更时重算。
CREATE TABLE IF NOT EXISTS profile_card (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  line       TEXT NOT NULL,
  priority   INTEGER NOT NULL DEFAULT 0,
  fact_id    TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);

-- profile_state：世代号。facts 变更时自增，画像卡按此比对是否过期。
CREATE TABLE IF NOT EXISTS profile_state (
  key        TEXT PRIMARY KEY,
  generation INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);

-- turns 增加污点标记，供抽取 SQL 过滤外部内容。
ALTER TABLE turns ADD COLUMN trust TEXT NOT NULL DEFAULT 'user';
