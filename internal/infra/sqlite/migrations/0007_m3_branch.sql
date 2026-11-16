-- M3.2 分支树：用 parent_id 把消息串成可回溯的链。
-- 注意：迁移执行器按行尾分号切分，每条语句必须单行写完。

-- 回填历史消息的 parent_id：按 seq 顺序串成线性链。
-- M0–M2 的数据 parent_id 全是 NULL，语义上就是一条单链；
-- 回填后 History 的「沿父链回溯」逻辑对老数据同样成立。
UPDATE messages SET parent_id = (SELECT m2.id FROM messages m2 WHERE m2.session_id = messages.session_id AND m2.seq < messages.seq ORDER BY m2.seq DESC LIMIT 1) WHERE parent_id IS NULL;

CREATE INDEX IF NOT EXISTS idx_messages_parent ON messages(session_id, parent_id);

-- 会话级模型覆盖（FR-PRV-010）：空串表示跟随路由默认。
ALTER TABLE sessions ADD COLUMN model_ref TEXT NOT NULL DEFAULT '';

-- 审批事件日志：Web 弹窗的批准/拒绝需要可追溯（M3.2）。
CREATE TABLE IF NOT EXISTS approvals (
  id          TEXT PRIMARY KEY,
  turn_id     TEXT NOT NULL,
  session_id  TEXT NOT NULL DEFAULT '',
  call_id     TEXT NOT NULL DEFAULT '',
  tool_name   TEXT NOT NULL,
  risk        TEXT NOT NULL DEFAULT 'read',
  summary     TEXT NOT NULL DEFAULT '',
  action      TEXT NOT NULL DEFAULT '',
  reason      TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending',
  created_at  INTEGER NOT NULL,
  resolved_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_approvals_status ON approvals(status, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_approvals_turn ON approvals(turn_id, created_at DESC);
