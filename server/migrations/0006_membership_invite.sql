-- 会员体系 + 邀请码激励。
--
-- 设计要点：档位不落库。
-- 只存 member_until（到期时间戳），档位由「当前时间 < member_until」实时算出。
-- 存一个 role='vip' 字段就必须有人（定时任务）负责到期降级，漏跑一次就是
-- 白送会员；而按时间戳判定天然到期即失效，不需要任何后台任务。
ALTER TABLE users ADD COLUMN member_until INTEGER NOT NULL DEFAULT 0;
-- invited_by 记录拉新关系，用于「有效邀请」统计与防重复发放奖励。
ALTER TABLE users ADD COLUMN invited_by TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_users_member_until ON users(member_until);

-- === 邀请码 ===
CREATE TABLE IF NOT EXISTS invite_codes (
  code        TEXT PRIMARY KEY,              -- 大写字母数字，便于口头/抄写
  owner_id    TEXT NOT NULL DEFAULT '',      -- 码归谁：拉新奖励记到他头上
  note        TEXT NOT NULL DEFAULT '',      -- 备注：发给谁 / 哪场活动
  reward_days INTEGER NOT NULL DEFAULT 30,   -- 邀请人获得的会员天数
  max_uses    INTEGER NOT NULL DEFAULT 0,    -- 0 = 不限次数
  used_count  INTEGER NOT NULL DEFAULT 0,
  expires_at  INTEGER NOT NULL DEFAULT 0,    -- 0 = 永不过期
  enabled     INTEGER NOT NULL DEFAULT 1,
  created_at  INTEGER NOT NULL
);

-- === 邀请码使用记录 ===
CREATE TABLE IF NOT EXISTS invite_uses (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  code       TEXT NOT NULL,
  invitee_id TEXT NOT NULL,                  -- 被邀请人 user id
  inviter_id TEXT NOT NULL DEFAULT '',       -- 邀请人 user id（码属于谁）
  reward_days INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invite_uses_code ON invite_uses(code);
CREATE INDEX IF NOT EXISTS idx_invite_uses_invitee ON invite_uses(invitee_id);
