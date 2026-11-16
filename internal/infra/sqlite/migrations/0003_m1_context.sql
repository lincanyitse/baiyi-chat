-- M1 阶段 C：上下文快照与内容寻址 blob。
--
-- 快照只存段元数据；正文超过 2KB 的段进 blobs，避免把数据库撑爆。

CREATE TABLE IF NOT EXISTS blobs (
    hash       TEXT PRIMARY KEY,
    size       INTEGER NOT NULL,
    encoding   TEXT NOT NULL DEFAULT 'utf8',
    data       BLOB NOT NULL,
    ref_count  INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS context_snapshots (
    id                TEXT PRIMARY KEY,
    turn_id           TEXT NOT NULL,
    step              INTEGER NOT NULL,
    model             TEXT NOT NULL,
    segments_json     TEXT NOT NULL,
    budget_json       TEXT NOT NULL,
    cache_breakpoints TEXT NOT NULL DEFAULT '',
    totals_json       TEXT NOT NULL,
    prefix_hash       TEXT NOT NULL,
    prev_snapshot_id  TEXT,
    created_at        INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_snapshots_turn ON context_snapshots(turn_id, step);
