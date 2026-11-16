-- M1.2：技能索引与摘要状态。
--
-- skill_index 是技能的持久化镜像（便于查询与统计），
-- 真实来源仍是 skills/ 目录下的 SKILL.md。

CREATE TABLE IF NOT EXISTS skill_index (
    name          TEXT PRIMARY KEY,
    path          TEXT NOT NULL,
    description   TEXT NOT NULL,
    version       TEXT NOT NULL DEFAULT '',
    spec          INTEGER NOT NULL DEFAULT 1,
    triggers_json TEXT NOT NULL DEFAULT '[]',
    allowed_tools TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,
    content_hash  TEXT NOT NULL,
    last_used_at  INTEGER
);

CREATE TABLE IF NOT EXISTS summary_state (
    session_id          TEXT PRIMARY KEY REFERENCES sessions(id),
    covered_through_seq INTEGER NOT NULL DEFAULT 0,
    summary             TEXT NOT NULL DEFAULT '',
    extracted_facts     TEXT NOT NULL DEFAULT '{}',
    template_version    TEXT NOT NULL DEFAULT 'v1',
    model               TEXT NOT NULL DEFAULT '',
    tokens_in           INTEGER NOT NULL DEFAULT 0,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS mcp_servers (
    alias      TEXT PRIMARY KEY,
    transport  TEXT NOT NULL,
    command    TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL DEFAULT '',
    tools_json TEXT NOT NULL DEFAULT '[]',
    healthy    INTEGER NOT NULL DEFAULT 1,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);
