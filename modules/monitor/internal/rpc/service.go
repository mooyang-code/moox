package rpc

import (
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
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
	InstanceID       string
	HostStore        *hostmetrics.Store
	HostReader       *hostmetrics.StorageReader
	HostStorageReady func() bool
	DoctorContext    *doctor.Builder
	HealthView       *healthview.Builder
	// HostWatchdog 是 GetHostPolicy 下发的主机守护参数。
	HostWatchdog config.HostWatchdogConfig
	// Lifecycle 返回 Monitor 的生命周期控制器（健康检查注册时才创建，之前为 nil）：告警屏蔽复用它的暂停。
	Lifecycle func() *lifecycle.Controller
}

type Service struct {
	alerts           *store.AlertRepository
	notifications    *store.NotificationRepository
	hostWatchdog     config.HostWatchdogConfig
	hostStore        *hostmetrics.Store
	hostReader       *hostmetrics.StorageReader
	hostStorageReady func() bool
	doctorContext    *doctor.Builder
	healthView       *healthview.Builder
	lifecycle        func() *lifecycle.Controller
}

func New(repos *store.Repositories, opts Options) *Service {
	if repos == nil {
		repos = store.NewRepositories(nil)
	}
	return &Service{
		alerts:           repos.Alerts,
		notifications:    repos.Notifications,
		hostWatchdog:     opts.HostWatchdog,
		hostStore:        opts.HostStore,
		hostReader:       opts.HostReader,
		hostStorageReady: opts.HostStorageReady,
		doctorContext:    opts.DoctorContext,
		healthView:       opts.HealthView,
		lifecycle:        opts.Lifecycle,
	}
}
