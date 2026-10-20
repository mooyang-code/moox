package report

import (
	"errors"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// 外部程序（因子引擎、归档 worker）的在线状态。它们运行在运维机上、不在部署体系内，
// 由管理端（factor-mgr、archive-mgr）按最近一次被接受的心跳评估，经指标上报给 Monitor 告警。
const (
	PresenceNotRequired = 0 // 没有需要它工作的配置（没有启用的因子集 / 归档任务）
	PresenceOnline      = 1
	PresenceOffline     = 2
)

// DefaultPresenceTimeout 是判定离线的心跳间隔：外部程序每 10 秒发一次心跳，2 分钟没有即离线。
const DefaultPresenceTimeout = 2 * time.Minute

// PresenceMetricSuffixes 是在线状态指标名的后缀，Monitor 按它们识别并生成检查。
const (
	PresenceStateSuffix         = "_worker_presence_state"
	PresenceLastHeartbeatSuffix = "_worker_last_heartbeat_timestamp_seconds"
)

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

// WorkerPresence 是一个管理端的在线状态指标：moox_<module>_worker_presence_state{worker} 与
// moox_<module>_worker_last_heartbeat_timestamp_seconds{worker}。worker 是最近一次心跳的程序标识（含机器名），
// 每次设置前清空旧标签，同一时刻只保留当前这一台。
type WorkerPresence struct {
	state, lastHeartbeat *prometheus.GaugeVec
}

func NewWorkerPresence(registerer prometheus.Registerer, module string) (*WorkerPresence, error) {
	if module == "" {
		return nil, errors.New("worker presence requires a module name")
	}
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	p := &WorkerPresence{
		state: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_" + module + PresenceStateSuffix, Help: "外部程序在线状态：0 不需要，1 在线，2 离线。",
		}, []string{"worker"}),
		lastHeartbeat: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "moox_" + module + PresenceLastHeartbeatSuffix, Help: "外部程序最近一次被接受心跳的时间（Unix 秒），没有心跳时为 0。",
		}, []string{"worker"}),
	}
	for _, collector := range []*prometheus.GaugeVec{p.state, p.lastHeartbeat} {
		if err := registerer.Register(collector); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// Set 记录本次评估结果；worker 为空表示从未收到心跳。
func (p *WorkerPresence) Set(state int, worker string, lastHeartbeat time.Time) {
	p.state.Reset()
	p.lastHeartbeat.Reset()
	p.state.WithLabelValues(worker).Set(float64(state))
	value := 0.0
	if !lastHeartbeat.IsZero() {
		value = float64(lastHeartbeat.Unix())
	}
	p.lastHeartbeat.WithLabelValues(worker).Set(value)
}
