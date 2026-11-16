-- M3.2 F 阶段：Web 本地口令。
-- 注意：迁移执行器按行尾分号切分，每条语句必须单行写完。

-- 单行表：单用户场景只需要一条记录（id 恒为 'default'）。
CREATE TABLE IF NOT EXISTS web_auth (
  id            TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL DEFAULT '',
  updated_at    INTEGER NOT NULL
);
