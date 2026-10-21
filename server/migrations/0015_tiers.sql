-- 会员种类（档位）：管理员可以自己加「普通用户 / 会员 / 高级会员 / 永久会员」这类档位，
-- 每个档位一套配额。之前只有 free/vip 两档写死在代码里，想多一档就得改代码。
CREATE TABLE IF NOT EXISTS tiers (
  id            TEXT PRIMARY KEY,            -- 稳定标识，如 free / vip / svip
  name          TEXT NOT NULL,               -- 显示名，如「高级会员」
  sort          INTEGER NOT NULL DEFAULT 0,  -- 列表顺序，小的在前
  max_rules     INTEGER NOT NULL DEFAULT 0,  -- 0 = 不限
  max_subs      INTEGER NOT NULL DEFAULT 0,
  daily_queries INTEGER NOT NULL DEFAULT 0,
  is_default    INTEGER NOT NULL DEFAULT 0,  -- 非会员用户的默认档位，全表只应有一个 1
  created_at    INTEGER NOT NULL DEFAULT 0
);

-- 两个基础档位必须先有：users.tier_id 为空时按会员状态回退到 free/vip，
-- 而会员状态到期要能落到一个真实的档位上，不能凭空造。
INSERT OR IGNORE INTO tiers (id, name, sort, max_rules, max_subs, daily_queries, is_default, created_at)
VALUES ('free', '普通用户', 0, 20, 1, 50000, 1, strftime('%s','now'));

INSERT OR IGNORE INTO tiers (id, name, sort, max_rules, max_subs, daily_queries, is_default, created_at)
VALUES ('vip', '会员', 10, 200, 10, 0, 0, strftime('%s','now'));

-- 用户可以被直接指定一个档位；空串表示「按 member_until 自动判定 free/vip」。
-- 这样老的会员逻辑照旧能用，新档位则是显式指定。
ALTER TABLE users ADD COLUMN tier_id TEXT NOT NULL DEFAULT '';
