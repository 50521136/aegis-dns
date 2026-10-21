package model

import "time"

// Tier 是用户的配额档位。
//
// 刻意不落库：users 表只存 member_until，档位由当前时间实时判定。
// 如果存一个 role='vip' 字段，就必须有人负责到期降级 —— 定时任务漏跑一次
// 就是白送会员，而且「到底谁还是会员」在库里永远说不清。按时间戳算，
// 到期即自动失效，不需要任何后台任务，也天然幂等。
type Tier string

const (
	TierFree Tier = "free"
	TierVIP  Tier = "vip"
)

// TierDef 是管理员自定义的会员种类（档位）。
//
// 之前档位写死在代码里，只有 free/vip 两档；想加「高级会员」就得改代码重新发版。
// 现在档位是一张表，配额全在库里。
type TierDef struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Sort         int    `json:"sort"`
	MaxRules     int    `json:"max_rules"`
	MaxSubs      int    `json:"max_subs"`
	DailyQueries int64  `json:"daily_queries"`
	IsDefault    bool   `json:"is_default"`
}

// Quota 是某个档位在某套全局设置下的实际配额。字段为 0 表示不限制。
type Quota struct {
	Tier         Tier  `json:"tier"`
	MaxRules     int   `json:"max_rules"`
	MaxSubs      int   `json:"max_subs"`
	DailyQueries int64 `json:"daily_queries"`
}

// IsMember 判断用户在 now 时刻是否是会员。
func (u *User) IsMember(now time.Time) bool {
	return u != nil && u.MemberUntil > now.Unix()
}

// TierOf 返回用户在 now 时刻的档位。
//
// 管理员不参与会员体系：管理员本来就没有配额约束，
// 给他算档位只会让「管理员是不是会员」这种问题出现。
func TierOf(u *User, now time.Time) Tier {
	if u != nil && u.Role == RoleAdmin {
		return TierVIP
	}
	if u.IsMember(now) {
		return TierVIP
	}
	return TierFree
}

// QuotaFor 返回某档位在给定设置下的配额。
//
// 这是全站唯一的档位→配额换算入口。规则、订阅、请求量三处都问它，
// 避免出现「规则按会员档算、订阅还按旧的统一配额算」这种错位。
func QuotaFor(t Tier, s *GlobalSettings) Quota {
	if s == nil {
		return Quota{Tier: t}
	}
	if t == TierVIP {
		return Quota{
			Tier:         TierVIP,
			MaxRules:     s.VipMaxRules,
			MaxSubs:      s.VipMaxSubs,
			DailyQueries: s.VipDailyQueries,
		}
	}
	return Quota{
		Tier:         TierFree,
		MaxRules:     s.FreeMaxRules,
		MaxSubs:      s.FreeMaxSubs,
		DailyQueries: s.FreeDailyQueries,
	}
}

// QuotaForTierDef 把库里的一条档位定义转成配额。
func QuotaForTierDef(t TierDef) Quota {
	return Quota{
		Tier:         Tier(t.ID),
		MaxRules:     t.MaxRules,
		MaxSubs:      t.MaxSubs,
		DailyQueries: t.DailyQueries,
	}
}

// QuotaForUser 是 TierOf + QuotaFor 的组合，再叠加管理员的单用户覆盖。
//
// 覆盖值的语义是「0 = 不覆盖」，所以不能直接用 0 表示「不限制」：
// 想要某个用户不限请求量，得显式填一个负数（-1），
// 由 applyOverride 统一把负数转成 0（0 在 Quota 里表示不限制）。
// 这样「没填」和「填了不限」才分得开。
func QuotaForUser(u *User, s *GlobalSettings, tiers map[string]TierDef, now time.Time) Quota {
	if u == nil {
		return QuotaFor(TierFree, s)
	}
	var q Quota
	// 优先级：显式指定的档位 > 按会员状态判定的 free/vip。
	// 显式档位优先是因为它就是「管理员指定了这个用户是什么等级」，
	// 而 member_until 只表达「会员到什么时候」，两者冲突时以后者为准会让人费解。
	//
	// 自动判定这条路径也要先查 tiers 表：free/vip 的配额在两个地方出现过
	// （settings 的 free_max_rules 和 tiers 表里的 free 行）。
	// 如果这里只读 settings，管理员在「会员种类」里改了 vip 的配额，
	// 自动档的用户却毫无变化 —— 两个真相来源，迟早对不上。
	// 现在统一以 tiers 表为准，settings 只作为 tiers 缺行时的兜底。
	if u.TierID != "" {
		if t, ok := tiers[u.TierID]; ok {
			q = QuotaForTierDef(t)
		}
	}
	if q.Tier == "" {
		if t, ok := tiers[string(TierOf(u, now))]; ok {
			q = QuotaForTierDef(t)
		} else {
			q = QuotaFor(TierOf(u, now), s)
		}
	}
	q.MaxRules = applyOverride(q.MaxRules, u.CustomMaxRules)
	q.MaxSubs = applyOverride(q.MaxSubs, u.CustomMaxSubs)
	q.DailyQueries = applyOverrideI64(q.DailyQueries, int64(u.CustomDailyQueries))
	return q
}

// applyOverride 把管理员手填的值叠到档位默认上。
// 0 = 用默认；-1 = 不限（Quota 里 0 表示不限）；其他正数直接覆盖。
func applyOverride(def, custom int) int {
	switch {
	case custom == 0:
		return def
	case custom < 0:
		return 0
	default:
		return custom
	}
}

// applyOverrideI64 是 applyOverride 的 int64 版本（每日请求量是 int64）。
func applyOverrideI64(def, custom int64) int64 {
	switch {
	case custom == 0:
		return def
	case custom < 0:
		return 0
	default:
		return custom
	}
}

// MemberRemainingDays 返回会员剩余天数，非会员返回 0。
func (u *User) MemberRemainingDays(now time.Time) int {
	if !u.IsMember(now) {
		return 0
	}
	secs := u.MemberUntil - now.Unix()
	days := int(secs / 86400)
	if secs%86400 != 0 {
		days++
	}
	return days
}

// ExtendMembership 把会员期延长 days 天。
//
// 从「现在」和「原到期时间」里取较晚的那个作为起点：
// 连续兑换两个邀请奖励时不会把前一段剩余时间冲掉，
// 而已经过期的用户从当下开始算，不会凭空倒贴。
func (u *User) ExtendMembership(days int, now time.Time) {
	if days <= 0 {
		return
	}
	base := now.Unix()
	if u.MemberUntil > base {
		base = u.MemberUntil
	}
	u.MemberUntil = base + int64(days)*86400
}
