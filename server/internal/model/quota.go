package model

import (
	"strings"
	"time"
)

// Tier 是用户的配额档位。
//
// 刻意不落库：users 表只存 member_until 与 tier_id，档位由当前时间实时判定。
// 如果存一个 role='vip' 字段，就必须有人负责到期降级 —— 定时任务漏跑一次
// 就是白送会员，而且「到底谁还是会员」在库里永远说不清。按时间戳算，
// 到期即自动失效，不需要任何后台任务，也天然幂等。
type Tier string

const (
	TierFree Tier = "free"
	TierVIP  Tier = "vip"
)

// MemberForever 是「永久会员」的到期时间（9999-12-31 23:59:59 UTC）。
//
// 用时间戳而不是额外的布尔字段：IsMember / MemberRemainingDays / 快照
// 全都只认 member_until，多一个字段就要在每一处都记得判断它，
// 漏一处就会出现「界面显示永久、实际已经过期」。
const MemberForever int64 = 253402300799

// IsPermanentMember 判断用户是否是永久会员。
func (u *User) IsPermanentMember() bool {
	return u != nil && u.MemberUntil >= MemberForever
}

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

// TierOf 返回用户在 now 时刻的**两档**档位（free / vip）。
//
// 这是老接口，只在拿不到档位表的地方用（例如快照里给 dnsd 算请求量上限）。
// 需要真实会员种类（普通用户 / 会员 / 超级会员 …）时用 EffectiveTierID。
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

// DefaultTierID 返回非会员用户应当落到的档位 id。
//
// 优先取被标成默认的那条（管理员在「会员种类」里勾的），
// 其次内置 free，最后取 sort 最小的 —— 全表一条都没有时也不能返回空串，
// 否则配额换算会静默退化成「不限」。
func DefaultTierID(tiers map[string]TierDef) string {
	for id, t := range tiers {
		if t.IsDefault {
			return id
		}
	}
	if _, ok := tiers[string(TierFree)]; ok {
		return string(TierFree)
	}
	best, bestSort := "", 0
	for id, t := range tiers {
		if best == "" || t.Sort < bestSort {
			best, bestSort = id, t.Sort
		}
	}
	if best == "" {
		return string(TierFree)
	}
	return best
}

// FallbackMemberTierID 返回会员没有显式指定种类时应当落到的档位。
//
// 内置 vip 优先（老部署只有两档，行为不变），否则取 sort 最大的非默认档
// —— 管理员加了「高级会员 / 超级会员」却没标默认时，会员期自然落在最高的那档。
func FallbackMemberTierID(tiers map[string]TierDef) string {
	if _, ok := tiers[string(TierVIP)]; ok {
		return string(TierVIP)
	}
	def := DefaultTierID(tiers)
	best, bestSort := "", 0
	for id, t := range tiers {
		if id == def {
			continue
		}
		if best == "" || t.Sort > bestSort {
			best, bestSort = id, t.Sort
		}
	}
	if best == "" {
		return string(TierVIP)
	}
	return best
}

// EffectiveTierID 返回用户此刻生效的会员种类 id。**全站唯一的档位判定入口。**
//
// 会员状态只有两个维度：会员种类（users.tier_id）+ 有效期（users.member_until）。
// 之前 tier_id 是「永久指定」、member_until 是「会员到什么时候」，两个概念
// 各算一套，于是同一个用户会同时显示「会员档」和「超级会员」，谁也说不清
// 哪个在生效。现在统一成：**种类只在会员期内生效，到期回落到默认档。**
func EffectiveTierID(u *User, tiers map[string]TierDef, now time.Time) string {
	def := DefaultTierID(tiers)
	if u == nil {
		return def
	}
	if u.Role == RoleAdmin {
		if _, ok := tiers[string(TierVIP)]; ok {
			return string(TierVIP)
		}
		if len(tiers) == 0 {
			return string(TierVIP)
		}
		return FallbackMemberTierID(tiers)
	}
	if !u.IsMember(now) {
		return def
	}
	if id := strings.TrimSpace(u.TierID); id != "" {
		// 档位被删了也不静默降级成「不限」：回落到默认会员档，
		// 这样配额至少是管理员配过的一个真实数字。
		if _, ok := tiers[id]; ok {
			return id
		}
	}
	return FallbackMemberTierID(tiers)
}

// TierRank 返回档位的等级（tiers.sort），查不到时返回 0。
// 发放奖励时用它判断「新档位是不是比当前更高」，低档奖励不会把高档覆盖掉。
func TierRank(tiers map[string]TierDef, id string) int {
	if t, ok := tiers[id]; ok {
		return t.Sort
	}
	return 0
}

// GrantableTierID 把一个「想要的奖励档位」规整成一个真实存在的档位 id。
// 传空串或指向已删除的档位时回落到默认会员档，避免发放时写出一个悬空 id。
func GrantableTierID(tiers map[string]TierDef, want string) string {
	if want = strings.TrimSpace(want); want != "" {
		if _, ok := tiers[want]; ok {
			return want
		}
	}
	return FallbackMemberTierID(tiers)
}

// MemberState 是一个用户此刻的会员状态，对外只暴露这一份。
//
// 之前前端拿 tier + is_member + member_days 三个字段自己拼状态，
// 于是同一个用户在两个页面显示成两种会员种类。现在后端算好一份，
// 前端只负责显示。
type MemberState struct {
	// Tier 是此刻生效的会员种类 id（非会员时是默认档）。
	Tier string `json:"tier"`
	// TierName 是它的显示名，前端不用再查一遍档位表。
	TierName string `json:"tier_name"`
	// IsMember 表示会员期是否还在有效期内。
	IsMember bool `json:"is_member"`
	// Forever 表示永久会员（member_until 是 MemberForever）。
	Forever bool `json:"member_forever"`
	// Days 是剩余天数，永久会员为 0（用 Forever 判断）。
	Days int `json:"member_days"`
	// Until 是到期时间（Unix 秒），0 表示从未开通/已过期。
	Until int64 `json:"member_until"`
	// AssignedTier 是用户被指定的种类（空串 = 会员期内走默认会员档）。
	AssignedTier string `json:"tier_id"`
}

// MemberStateOf 组装一个用户的会员状态。
func MemberStateOf(u *User, tiers map[string]TierDef, now time.Time) MemberState {
	st := MemberState{Tier: DefaultTierID(tiers)}
	if u == nil {
		return st
	}
	st.Tier = EffectiveTierID(u, tiers, now)
	st.TierName = st.Tier
	if t, ok := tiers[st.Tier]; ok {
		st.TierName = t.Name
	}
	// 只反映真实会员期：管理员虽然不受配额限制（EffectiveTierID 给他 vip），
	// 但他并不是「会员」。把管理员算成会员会让后台弹窗显示
	// 「会员 · 剩 0 天」这种自相矛盾的东西。
	st.IsMember = u.IsMember(now)
	st.Forever = u.IsPermanentMember()
	st.Until = u.MemberUntil
	st.AssignedTier = u.TierID
	if !st.Forever {
		st.Days = u.MemberRemainingDays(now)
	}
	return st
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

// QuotaForUser 是「生效档位 → 配额」的唯一入口，再叠加管理员的单用户覆盖。
//
// 覆盖值的语义是「0 = 不覆盖」，所以不能直接用 0 表示「不限制」：
// 想要某个用户不限请求量，得显式填一个负数（-1），
// 由 applyOverride 统一把负数转成 0（0 在 Quota 里表示不限制）。
// 这样「没填」和「填了不限」才分得开。
func QuotaForUser(u *User, s *GlobalSettings, tiers map[string]TierDef, now time.Time) Quota {
	if u == nil {
		return QuotaFor(TierFree, s)
	}
	// 档位判定只走 EffectiveTierID 一处：会员种类 + 有效期，别的地方
	// 再各算一套就会出现「设置页按 vip 算、规则按 svip 算」这种错位。
	tid := EffectiveTierID(u, tiers, now)
	var q Quota
	if t, ok := tiers[tid]; ok {
		q = QuotaForTierDef(t)
	} else {
		// 档位表读不到（空库/迁移未跑）时才退回 settings 里的两档数字。
		q = QuotaFor(Tier(tid), s)
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
