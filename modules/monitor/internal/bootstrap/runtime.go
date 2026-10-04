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
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/report"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Runtime owns monitor's process-scoped resources and shutdown ordering.
type Runtime struct {
	StartedAt                time.Time
	cancel                   context.CancelFunc
	workers                  sync.WaitGroup
	closeOnce                sync.Once
	closeErr                 error
	Store                    *store.Store
	Repositories             *store.Repositories
	MetricStores             *monmetrics.Stores
	HostRuleCache            *hostmetrics.RuleCache
	Scheduler                *scheduler.Scheduler
	ObservabilityIngestReady atomic.Bool
	MetricsReporterReady     atomic.Bool
	observabilityIngestError atomic.Value
	observabilityWriteError  atomic.Value
	writeStateSequence       atomic.Int64
	observabilityWriteFailed atomic.Int64
	observabilityWriteOK     atomic.Int64
	hostWriteError           atomic.Value
	hostWriteFailed          atomic.Int64
	hostWriteOK              atomic.Int64
	metricsReporterError     atomic.Value
	ModuleMetrics            *report.ModuleMetrics
	ObservabilityHealthRoute func(context.Context, *eventpb.EventMessage, *observabilitypb.HealthCheckReport) error
}

func (r *Runtime) recordObservabilityWriteFailure(err error) {
	if r == nil || err == nil {
		return
	}
	sequence := r.writeStateSequence.Add(1)
	r.observabilityWriteError.Store(sanitizedMetricsError(err))
	storeWriteStateSequence(&r.observabilityWriteFailed, sequence)
}

func (r *Runtime) recordObservabilityWriteSuccess() {
	if r == nil {
		return
	}
	storeWriteStateSequence(&r.observabilityWriteOK, r.writeStateSequence.Add(1))
}

func (r *Runtime) recordHostWriteFailure(err error) {
	if r == nil || err == nil {
		return
	}
	sequence := r.writeStateSequence.Add(1)
	r.hostWriteError.Store(sanitizedMetricsError(err))
	storeWriteStateSequence(&r.hostWriteFailed, sequence)
}

func (r *Runtime) recordHostWriteSuccess() {
	if r == nil {
		return
	}
	storeWriteStateSequence(&r.hostWriteOK, r.writeStateSequence.Add(1))
}

func storeWriteStateSequence(target *atomic.Int64, sequence int64) {
	for {
		current := target.Load()
		if current >= sequence || target.CompareAndSwap(current, sequence) {
			return
		}
	}
}

func (r *Runtime) observabilityWriteReady(now time.Time) (bool, string) {
	if r == nil {
		return false, "observability runtime is unavailable"
	}
	if ready, reason := writeStateReady(r.observabilityWriteFailed.Load(), r.observabilityWriteOK.Load(), &r.observabilityWriteError); !ready {
		return false, reason
	}
	return writeStateReady(r.hostWriteFailed.Load(), r.hostWriteOK.Load(), &r.hostWriteError)
}

func writeStateReady(failedSequence, succeededSequence int64, messageValue *atomic.Value) (bool, string) {
	if failedSequence == 0 || succeededSequence > failedSequence {
		return true, ""
	}
	message, _ := messageValue.Load().(string)
	return false, message
}

func (r *Runtime) setMetricsReporterState(ready bool, err error) {
	if r == nil {
		return
	}
	r.MetricsReporterReady.Store(ready)
	message := ""
	if err != nil {
		message = sanitizedMetricsError(err)
	}
	r.metricsReporterError.Store(message)
}

func (r *Runtime) setObservabilityIngestState(ready bool, err error) {
	if r == nil {
		return
	}
	r.ObservabilityIngestReady.Store(ready)
	message := ""
	if err != nil {
		message = sanitizedMetricsError(err)
	}
	r.observabilityIngestError.Store(message)
}

func (r *Runtime) observabilityIngestErrorMessage() string {
	if r == nil {
		return ""
	}
	message, _ := r.observabilityIngestError.Load().(string)
	return message
}

func (r *Runtime) metricsReporterErrorMessage() string {
	if r == nil {
		return ""
	}
	message, _ := r.metricsReporterError.Load().(string)
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
