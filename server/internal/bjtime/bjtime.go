// Package bjtime 统一「自然日」的口径。
//
// 全站的自然日一律按北京时间（UTC+8）划分，不是 UTC。
// 原因很直接：用户在中国，签到和每日请求量额度必须在本地 0 点重置。
// 按 UTC 算的话，重置时刻落在早上 8 点 —— 用户凌晨签到会被算成前一天，
// 额度也要到上班时间才恢复，怎么看都是错的。
//
// 只有这一处定义日界，签到与配额都调它，避免两个地方各算一套。
package bjtime

import "time"

// offset 是北京时间相对 UTC 的固定偏移。
// 中国全境不使用夏令时，固定 +8 即可，不需要 tzdata。
const offset = 8 * 3600

// Day 返回某个时刻所属的北京自然日序号（从 Unix 纪元起算的天数）。
//
// 用序号而不是日期字符串：数据库里存 INTEGER 比较快，
// 判断「昨天/今天」也只是一次减法。
func Day(t time.Time) int64 { return (t.Unix() + offset) / 86400 }

// Today 返回当前北京时间所属的自然日序号。
func Today() int64 { return Day(time.Now()) }

// StartOfDay 返回某个自然日序号的北京时间 0 点对应的 Unix 秒。
func StartOfDay(day int64) int64 { return day*86400 - offset }
