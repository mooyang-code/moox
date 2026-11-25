package rpc

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// Options contains only dependencies required by the public Monitor RPCs.
// Checks, raw metric catalogs, webhook CRUD and custom rule engines are
// intentionally absent: those are internal implementation details now.
type Options struct {
	InstanceID string
	// StartedAt 是 Monitor 进程的启动时间，随 GetHostPolicy 下发：各主机采集器按它的变化发现 Monitor 反复重启。
	StartedAt     time.Time
	HostStore     *hostmetrics.Store
	DoctorContext *doctor.Builder
	HealthView    *healthview.Builder
	// Lifecycle 返回 Monitor 的生命周期控制器（健康检查注册时才创建，之前为 nil）：告警屏蔽复用它的暂停。
	Lifecycle func() *lifecycle.Controller
	// EvaluationStale 返回检查评估停滞的原因（最近 3 分钟没有一次完整成功的评估），正常时为空；为 nil 时不判断。
	// 停滞时 GetHostPolicy 返回失败，由各主机 HostAgent 的“连续 3 次拉取失败”直推告警。
	EvaluationStale func() string
	// HostReports 处理 HostAgent 随 GetHostPolicy 带来的主机快照；为 nil 时丢弃快照。
	HostReports func(context.Context, *monitorpb.HostReport) error
}

type Service struct {
	alerts        *store.AlertRepository
	notifications *store.NotificationRepository
	logs          *store.LogRecordRepository
	startedAt     time.Time
	hostStore     *hostmetrics.Store
	doctorContext *doctor.Builder
	healthView    *healthview.Builder
	lifecycle     func() *lifecycle.Controller
	evalStale     func() string
	hostReports   func(context.Context, *monitorpb.HostReport) error
}

func New(repos *store.Repositories, opts Options) *Service {
	if repos == nil {
		repos = store.NewRepositories(nil)
	}
	return &Service{
		alerts:        repos.Alerts,
		notifications: repos.Notifications,
		logs:          repos.Logs,
		startedAt:     opts.StartedAt,
		hostStore:     opts.HostStore,
		doctorContext: opts.DoctorContext,
		healthView:    opts.HealthView,
		lifecycle:     opts.Lifecycle,
		evalStale:     opts.EvaluationStale,
		hostReports:   opts.HostReports,
	}
}
