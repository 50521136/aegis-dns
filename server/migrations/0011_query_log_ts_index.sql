-- 查询日志改成按分钟保留（默认 60 分钟），清理频率从每天提到每 10 分钟。
-- 现有索引是 (user_id, ts DESC)，最左列不是 ts，`WHERE ts < ?` 用不上它，
-- 每次都全表扫描。日志表在小机器上可能有几十万行，加个 ts 单列索引。
CREATE INDEX IF NOT EXISTS idx_qlog_ts ON query_log(ts);
