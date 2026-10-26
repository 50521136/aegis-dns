package model

// Notification 是一条站内通知（只属于某一个用户）。
//
// 和 Announcement 的区别：公告是一个 id 对所有人可见、由管理员运营；
// 通知是按人产生的、内容因人而异（「你的订阅 xxx 已被收录」这句话
// 对别人不成立），只需追加、不需要编辑。
type Notification struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// Kind 是通知类别，前端按它选图标与跳转，不靠解析正文猜。
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Link 是点开通知后跳转的前端路由，可为空。
	Link string `json:"link,omitempty"`
	// RefID 关联业务对象（订阅 id、反馈 id），便于回溯。
	RefID string `json:"ref_id,omitempty"`
	// DedupeKey 非空时用于防重复通知：同一用户同 key 只允许一条。
	// json:"-" —— 它是内部去重手段，对前端没有意义。
	DedupeKey string `json:"-"`
	ReadAt    int64  `json:"read_at"`
	CreatedAt int64  `json:"created_at"`
}

// 通知类别。
//
// 这些值会出现在前端路由映射和数据库里，改动要同步两边。
const (
	// NotifySubPromoted 同一份订阅被多人订阅，已自动收录为全局规则。
	NotifySubPromoted = "sub_promoted"
	// NotifyCheckinBonus 签到（连签奖励）拿到会员天数。
	NotifyCheckinBonus = "checkin_bonus"
	// NotifyFeedbackAI 反馈被 AI 自动处理并结单。
	NotifyFeedbackAI = "feedback_ai"
	// NotifyFeedbackReply 管理员回复了反馈。
	NotifyFeedbackReply = "feedback_reply"
	// NotifySystem 系统通知（兜底类别）。
	NotifySystem = "system"
)
