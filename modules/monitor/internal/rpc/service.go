package rpc

import (
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
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
	}
}
