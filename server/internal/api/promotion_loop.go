package api

import (
	"context"
	"time"
)

// 这个文件是「订阅自动收录」的定时循环。
//
// 需求要求「同一份订阅被超过 3 个人订阅时直接改为全局规则」，没有说
// 什么时候检查。放在订阅刷新之后是最自然的想法，但那样等于把它绑在
// 24 小时一次的刷新窗口上 —— 用户周三订阅的东西，可能要等到周四凌晨
// 三点刷新完才被发现重复，中间一整天都是各拉各的。
//
// 所以单独一个循环，检查得更勤一些，但不做无用功：
//   - 只在真的有候选时才重建快照（重建一次 9~14 秒，空转不能触发）；
//   - 通知靠 dedupe_key 去重，重复执行不会发出重复通知。

// promotionInterval 是自动收录的检查间隔。
//
// 取 10 分钟：订阅是低频动作（一天几条），10 分钟足够及时发现；
// 而每次检查只是一条聚合查询（走 idx_subs_url），开销可以忽略。
const promotionInterval = 10 * time.Minute

// StartPromotionLoop 启动自动收录循环，ctx 取消时退出。
func (s *Server) StartPromotionLoop(ctx context.Context) {
	// 启动时不立刻跑：apid 重启很频繁（每次升级都重启），
	// 而收录会删用户订阅，不该在每次重启的瞬间就对着一批数据动手。
	// 第一次等到一个完整间隔之后。
	timer := time.NewTimer(promotionInterval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}

	s.runPromotionOnce(ctx)

	ticker := time.NewTicker(promotionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runPromotionOnce(ctx)
		}
	}
}

// runPromotionOnce 执行一轮检查，失败只记日志不中断循环。
func (s *Server) runPromotionOnce(ctx context.Context) {
	// 单轮上限：一轮最多处理几分钟，避免某次异常把循环卡死。
	rctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	res, err := s.RunSubscriptionPromotion(rctx)
	if err != nil {
		s.log.Warn("订阅自动收录检查失败", "err", err)
		return
	}
	if res.Promoted > 0 {
		s.log.Info("订阅自动收录本轮生效",
			"组数", res.Promoted, "删除私有订阅", res.RemovedSubs, "通知用户", res.Notified)
	}
}
