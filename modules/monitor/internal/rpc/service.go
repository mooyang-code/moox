package rpc

import (
	"github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

// Options contains only dependencies required by the six public Monitor RPCs.
// Checks, raw metric catalogs, webhook CRUD and custom rule engines are
// intentionally absent: those are internal implementation details now.
type Options struct {
	InstanceID       string
	HostStore        *hostmetrics.Store
	HostReader       *hostmetrics.StorageReader
	HostStorageReady func() bool
	DoctorContext    *doctor.Builder
	HealthView       *healthview.Builder
}

type Service struct {
	alerts           *store.AlertRepository
	notifications    *store.NotificationRepository
	hostStore        *hostmetrics.Store
	hostReader       *hostmetrics.StorageReader
	hostStorageReady func() bool
	doctorContext    *doctor.Builder
	healthView       *healthview.Builder
}

func New(repos *store.Repositories, opts Options) *Service {
	if repos == nil {
		repos = store.NewRepositories(nil)
	}
	return &Service{
		alerts:           repos.Alerts,
		notifications:    repos.Notifications,
		hostStore:        opts.HostStore,
		hostReader:       opts.HostReader,
		hostStorageReady: opts.HostStorageReady,
		doctorContext:    opts.DoctorContext,
		healthView:       opts.HealthView,
	}
}
