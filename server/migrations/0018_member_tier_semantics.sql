-- 会员种类与会员期统一。
--
-- 之前 tier_id 的语义是「管理员永久指定的档位」，与 member_until（会员到期）
-- 各算一套：同一个用户可能同时显示「会员档」和「超级会员」，谁也说不清哪个
-- 在生效。现在统一成：**种类只在会员期内生效，到期回落到默认档**
-- （判定入口 model.EffectiveTierID）。
--
-- 迁移要保住老数据的实际效果：被永久指定过档位的用户，原本不受到期时间影响，
-- 改成永久会员期才能保持原样。
UPDATE users
   SET member_until = 253402300799
 WHERE tier_id != ''
   AND member_until <= strftime('%s','now')
   AND tier_id NOT IN (SELECT id FROM tiers WHERE is_default = 1);

-- 显式指定成「默认档」本身没有任何意义（非会员本来就是默认档），
-- 留着只会在界面上显示成「永久会员 · 普通用户」。
UPDATE users
   SET tier_id = ''
 WHERE tier_id != ''
   AND member_until <= strftime('%s','now')
   AND tier_id IN (SELECT id FROM tiers WHERE is_default = 1);

-- 邀请码可以指定「核销后发给邀请人的会员种类」。
-- 空串 = 用全局设置 member_reward_tier，再空则走默认会员档。
ALTER TABLE invite_codes ADD COLUMN reward_tier TEXT NOT NULL DEFAULT '';
