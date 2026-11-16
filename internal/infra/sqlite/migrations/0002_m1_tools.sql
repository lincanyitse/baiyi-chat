-- M1 阶段 A：工具执行所需的最小表结构。
-- 后续阶段（B/C/D/E/F）会继续追加 0003/0004…

ALTER TABLE turns ADD COLUMN step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE turns ADD COLUMN spent_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN token_est INTEGER NOT NULL DEFAULT 0;

-- 工具结果以 (turn_id, tool_call_id) 为幂等键：
-- 同一调用重放时直接命中缓存，不重复执行。
CREATE TABLE IF NOT EXISTS tool_results (
    turn_id      TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    name         TEXT NOT NULL,
    args_hash    TEXT NOT NULL,
    result_json  TEXT NOT NULL,
    is_error     INTEGER NOT NULL DEFAULT 0,
    handle       TEXT NOT NULL DEFAULT '',
    duration_ms  INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (turn_id, tool_call_id)
);

CREATE INDEX IF NOT EXISTS idx_tool_results_turn ON tool_results(turn_id);

-- 被截断的大结果按内容寻址存储，句柄为内容哈希前缀。
CREATE TABLE IF NOT EXISTS tool_output_store (
    handle       TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL UNIQUE,
    size         INTEGER NOT NULL,
    encoding     TEXT NOT NULL DEFAULT 'utf8',
    data         BLOB NOT NULL,
    created_at   INTEGER NOT NULL
);
