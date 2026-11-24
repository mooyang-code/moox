package report

import "time"

// 外部程序（因子引擎、归档 worker）的在线状态。它们运行在运维机上、不在部署体系内，
// 由管理端（factor-mgr、archive-mgr）按最近一次被接受的心跳评估，离线时上报关注项。
const (
	PresenceNotRequired = 0 // 没有需要它工作的配置（没有启用的因子任务 / 归档任务）
	PresenceOnline      = 1
	PresenceOffline     = 2
)

// DefaultPresenceTimeout 是判定离线的心跳间隔：外部程序每 10 秒发一次心跳，2 分钟没有即离线。
const DefaultPresenceTimeout = 2 * time.Minute

// EvaluatePresence 按是否需要、最近心跳时间得出在线状态。
func EvaluatePresence(required bool, lastHeartbeat, now time.Time, timeout time.Duration) int {
	if !required {
		return PresenceNotRequired
	}
	if timeout <= 0 {
		timeout = DefaultPresenceTimeout
	}
	if !lastHeartbeat.IsZero() && now.Sub(lastHeartbeat) <= timeout {
		return PresenceOnline
	}
	return PresenceOffline
}

// PresenceAttention 返回外部程序离线关注项的 detail：不离线时为空；写明机器与最后心跳时刻（不写已离线多久）。
func PresenceAttention(state int, worker string, lastHeartbeat time.Time) []string {
	if state != PresenceOffline {
		return nil
	}
	if worker == "" || lastHeartbeat.IsZero() {
		return []string{"从未收到心跳，请检查运维机上的程序是否已安装并启动"}
	}
	return []string{worker + " 最后心跳 " + AttentionTime(lastHeartbeat) + "，超过 2 分钟没有心跳"}
}
