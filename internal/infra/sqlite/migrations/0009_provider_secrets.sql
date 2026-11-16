-- API Key 加密存储（ADR-0020）。
-- 注意：迁移执行器按行尾分号切分，每条语句必须单行写完。

-- 每个 Provider 一条密文。AES-256-GCM，nonce 前置在密文里。
CREATE TABLE IF NOT EXISTS provider_secrets (
  provider_name TEXT PRIMARY KEY,
  key_encrypted BLOB NOT NULL,
  updated_at    INTEGER NOT NULL
);

-- 数据密钥的保护记录。单行表，id 恒为 'dek'。
-- protection 记录保护方式（dpapi / file-binding），供界面提示与将来迁移。
CREATE TABLE IF NOT EXISTS secret_meta (
  id          TEXT PRIMARY KEY,
  wrapped_dek BLOB NOT NULL,
  protection  TEXT NOT NULL,
  created_at  INTEGER NOT NULL
);
