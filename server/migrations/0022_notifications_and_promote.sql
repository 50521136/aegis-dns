-- 站内通知：面向普通用户的轻量消息（订阅被收录、签到奖励、AI 自动处理回复等）。
--
-- 为什么另起一张表，而不是复用 announcements：
--   * announcements 是**运营发的公告**，同一个 id 对所有人可见，已读状态
--     另存 announcement_reads；而通知是**按人产生的**，一条通知只属于一个人，
--     内容也因人而异（「你的订阅 xxx 已被收录」这句话对别人不成立）；
--   * 两者的生命周期也不同：公告由管理员增删改，通知只增不改，且要能
--     按用户批量清理、按未读计数。
--
-- 设计要点：
--   * kind 是通知类别（sub_promoted / checkin_bonus / feedback_ai / system），
--     前端按它选图标与跳转目标，不靠解析正文猜；
--   * link 是点开通知后跳转的前端路由（如 /app/subscriptions），可为空；
--   * ref_id 关联业务对象（订阅 id、反馈 id），便于去重与回溯；
--   * dedupe_key 唯一约束用来防重复通知：同一个订阅被收录的通知，
--     重复触发时靠它挡住（NULL 不参与唯一约束，所以不填就不去重）。

CREATE TABLE IF NOT EXISTS notifications (
  id          TEXT    NOT NULL PRIMARY KEY,
  user_id     TEXT    NOT NULL,
  kind        TEXT    NOT NULL,                 -- sub_promoted | checkin_bonus | feedback_ai | system
  title       TEXT    NOT NULL DEFAULT '',
  body        TEXT    NOT NULL DEFAULT '',
  link        TEXT    NOT NULL DEFAULT '',      -- 前端路由，可为空
  ref_id      TEXT    NOT NULL DEFAULT '',      -- 关联业务对象 id
  dedupe_key  TEXT,                             -- 非 NULL 时唯一，用于防重复通知
  read_at     INTEGER NOT NULL DEFAULT 0,       -- 0 = 未读
  created_at  INTEGER NOT NULL
) WITHOUT ROWID;

-- 列表页按用户倒序读，未读计数走同一条索引的前缀。
CREATE INDEX IF NOT EXISTS idx_notifications_user ON notifications(user_id, created_at DESC);

-- 未读计数是本表最高频的查询（每次进页面、每次轮询都要跑）。
-- 单独建一个只含未读行的部分索引：未读通常只有几条，
-- 索引很小，而全表会随时间无限增长。
CREATE INDEX IF NOT EXISTS idx_notifications_unread
  ON notifications(user_id) WHERE read_at = 0;

-- 防重复通知：同一个人、同一个 dedupe_key 只允许一条。
CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_dedupe
  ON notifications(user_id, dedupe_key) WHERE dedupe_key IS NOT NULL;

-- 反馈的「用户已读」与「管理员已读」两个水位。
--
-- 红点提醒要区分两端：
--   * 管理员：用户追问后需要重新处理（user_read_at 被用户顶高，而管理员那边还没看）；
--   * 用户：管理员回复后需要看到（admin_read_at 由用户顶高）。
-- 用两个时间戳水位而不是布尔位，是因为一条反馈可能往返多轮 ——
-- 布尔位在第二轮就失效了，而水位永远只需要比「最后一条对方的发言时间」。
ALTER TABLE feedback ADD COLUMN user_read_at  INTEGER NOT NULL DEFAULT 0;
ALTER TABLE feedback ADD COLUMN admin_read_at INTEGER NOT NULL DEFAULT 0;

-- 会话化之后新增两条时间戳：
--   * ai_answered_at：AI 自动回复的时间（0 = 未经 AI 自动处理）；
--   * needs_human_at：AI 判断为「需要人工」而转人工的时间，用于把这类条目
--     单独筛出来，避免混在普通未读里被忽略。
ALTER TABLE feedback ADD COLUMN ai_answered_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE feedback ADD COLUMN needs_human_at INTEGER NOT NULL DEFAULT 0;

-- 订阅被收录为全局规则的溯源与通知去重。
--
-- promoted_at：这条订阅是被系统自动收录的，记下时间（0 = 用户自建，非收录）；
-- promoted_from：收录前它属于哪个用户（用户可能改订阅名、退出，仅供追溯）。
ALTER TABLE subscriptions ADD COLUMN promoted_at   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE subscriptions ADD COLUMN promoted_from TEXT    NOT NULL DEFAULT '';

-- 「同一份订阅被 >= N 个用户订阅」需要在定时刷新后快速统计。
-- 按 url 聚合是最常用的口径，直接建索引避免全表扫。
CREATE INDEX IF NOT EXISTS idx_subs_url ON subscriptions(url);
