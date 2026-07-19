package bootstrap

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Runtime owns monitor's process-scoped resources and shutdown ordering.
type Runtime struct {
	StartedAt          time.Time
	cancel             context.CancelFunc
	workers            sync.WaitGroup
	closeOnce          sync.Once
	closeErr           error
	Store              *store.Store
	Repositories       *store.Repositories
	MetricStores       *monmetrics.Stores
	HostRuleCache      *hostmetrics.RuleCache
	Scheduler          *scheduler.Scheduler
	MetricScheduler    *monmetrics.RuleScheduler
	MetricsIngestReady atomic.Bool
	metricsIngestError atomic.Value
}

func (r *Runtime) setMetricsIngestState(ready bool, err error) {
	if r == nil {
		return
	}
	r.MetricsIngestReady.Store(ready)
	message := ""
	if err != nil {
		message = sanitizedMetricsError(err)
	}
	r.metricsIngestError.Store(message)
}

func (r *Runtime) metricsIngestErrorMessage() string {
	if r == nil {
		return ""
	}
	message, _ := r.metricsIngestError.Load().(string)
	return message
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.HostRuleCache != nil {
			_ = r.HostRuleCache.Stop(trpc.BackgroundContext())
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
