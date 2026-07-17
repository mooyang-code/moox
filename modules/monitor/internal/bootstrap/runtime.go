package bootstrap

import (
	"context"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Runtime owns monitor's process-scoped resources and shutdown ordering.
type Runtime struct {
	StartedAt       time.Time
	cancel          context.CancelFunc
	workers         sync.WaitGroup
	closeOnce       sync.Once
	closeErr        error
	Store           *store.Store
	Repositories    *store.Repositories
	MetricStores    *monmetrics.Stores
	HostRuleCache   *hostmetrics.RuleCache
	Scheduler       *scheduler.Scheduler
	MetricScheduler *monmetrics.RuleScheduler
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.Scheduler != nil {
			r.Scheduler.Stop()
		}
		if r.HostRuleCache != nil {
			_ = r.HostRuleCache.Stop(trpc.BackgroundContext())
		}
		if r.MetricScheduler != nil {
			r.MetricScheduler.Stop()
		}
		r.workers.Wait()
		if r.Store != nil {
			r.closeErr = r.Store.Close()
		}
	})
	return r.closeErr
}

func (r *Runtime) Go(fn func()) {
	if r == nil || fn == nil {
		return
	}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		fn()
	}()
}
