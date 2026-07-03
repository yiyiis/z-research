-- z-research 报告存储表结构（SQLite）
-- 由 store 包在初始化时自动执行（IF NOT EXISTS）。

CREATE TABLE IF NOT EXISTS reports (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    query      TEXT    NOT NULL,          -- 原始研究查询
    title      TEXT    NOT NULL DEFAULT '',-- 报告标题（取首行或前 N 字）
    content    TEXT    NOT NULL,          -- 完整 Markdown 报告
    sources    TEXT    NOT NULL DEFAULT '[]', -- 来源列表 JSON
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')) -- Unix 时间戳（秒）
);

-- 按时间倒序查询历史列表时用的索引。
CREATE INDEX IF NOT EXISTS idx_reports_created_at ON reports (created_at DESC);
