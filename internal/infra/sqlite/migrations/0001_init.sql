-- M0 首批表：会话、消息、轮次、模型调用明细、价格表。
-- 主键一律 UUIDv7（TEXT），全部实体带时间戳，删除为软删除（deleted_at）。

CREATE TABLE IF NOT EXISTS sessions (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL DEFAULT '',
    preset_id   TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    archived_at INTEGER,
    deleted_at  INTEGER
);

CREATE TABLE IF NOT EXISTS messages (
    id           TEXT PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id),
    seq          INTEGER NOT NULL,
    parent_id    TEXT,
    role         TEXT NOT NULL,
    content_json TEXT NOT NULL,
    trust        TEXT NOT NULL DEFAULT 'trusted',
    pinned       INTEGER NOT NULL DEFAULT 0,
    compacted    INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_messages_session_seq ON messages(session_id, seq);

CREATE TABLE IF NOT EXISTS turns (
    id         TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    status     TEXT NOT NULL,
    error_json TEXT,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER
);

CREATE INDEX IF NOT EXISTS idx_turns_session ON turns(session_id, started_at);

CREATE TABLE IF NOT EXISTS llm_calls (
    id                TEXT PRIMARY KEY,
    turn_id           TEXT NOT NULL,
    step              INTEGER NOT NULL DEFAULT 0,
    provider          TEXT NOT NULL,
    model             TEXT NOT NULL,
    purpose           TEXT NOT NULL DEFAULT 'chat',
    input_tokens      INTEGER NOT NULL DEFAULT 0,
    output_tokens     INTEGER NOT NULL DEFAULT 0,
    cached_tokens     INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
    latency_ms        INTEGER NOT NULL DEFAULT 0,
    ttft_ms           INTEGER NOT NULL DEFAULT 0,
    price_version     TEXT NOT NULL DEFAULT '',
    error_kind        TEXT,
    created_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_llm_calls_turn ON llm_calls(turn_id);
CREATE INDEX IF NOT EXISTS idx_llm_calls_created ON llm_calls(created_at);

CREATE TABLE IF NOT EXISTS prices (
    provider       TEXT NOT NULL,
    model          TEXT NOT NULL,
    currency       TEXT NOT NULL DEFAULT 'CNY',
    price_in       REAL NOT NULL DEFAULT 0,
    price_out      REAL NOT NULL DEFAULT 0,
    price_cached   REAL NOT NULL DEFAULT 0,
    effective_from INTEGER NOT NULL,
    PRIMARY KEY (provider, model, effective_from)
);
