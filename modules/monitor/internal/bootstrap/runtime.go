package bootstrap

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/storagegateway"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/report"
	trpc "trpc.group/trpc-go/trpc-go"
)

// Runtime owns monitor's process-scoped resources and shutdown ordering.
type Runtime struct {
	Gateway        *gatewayclient.Client
	StorageGateway *storagegateway.Client
	StartedAt      time.Time
	cancel         context.CancelFunc
	workers        sync.WaitGroup
	closeOnce      sync.Once
	closeErr       error
	Store          *store.Store
	Repositories   *store.Repositories
	MetricStores   *monmetrics.Stores
	HostRuleCache  *hostmetrics.RuleCache
	Scheduler      *scheduler.Scheduler
	// klineFreshness 是 K 线新鲜度评估器（没有评估器时为空）；清理失效检查时用它识别会被补建的 K 线检查。
	klineFreshness *monmetrics.KlineFreshnessEvaluator
	// life 是 Monitor 的生命周期控制器（在健康检查注册时创建，创建之前为空）：暂停时告警静默，只记录、不推送。
	life atomic.Pointer[lifecycle.Controller]
	// mutes 记录进入静默的时间，静默结束时据此补发汇总。
	mutes muteTracker

	ObservabilityIngestReady atomic.Bool
	// ingestReadySince 是观测消费者最近一次由未连接变为已连接的时刻（Unix 纳秒）；未连接时为 0。
	ingestReadySince         atomic.Int64
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
}

// recordObservabilityWriteFailure 记录服务指标写入失败；reason 指明失败的环节。
func (r *Runtime) recordObservabilityWriteFailure(reason string, err error) {
	if r == nil || err == nil {
		return
	}
	sequence := r.writeStateSequence.Add(1)
	r.observabilityWriteError.Store(failureReason(reason, err))
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
	r.hostWriteError.Store(failureReason(reasonHostMetrics, err))
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
		message = failureReason(reasonEventbus, err)
	}
	r.metricsReporterError.Store(message)
}

func (r *Runtime) setObservabilityIngestState(ready bool, err error) {
	if r == nil {
		return
	}
	if ready {
		// 只在由未连接变为已连接时记下时刻，之后重复的就绪回调不重置它。
		if !r.ObservabilityIngestReady.Swap(true) {
			r.ingestReadySince.Store(time.Now().UnixNano())
		}
	} else {
		r.ObservabilityIngestReady.Store(false)
		r.ingestReadySince.Store(0)
	}
	message := ""
	if err != nil {
		message = failureReason(reasonEventbus, err)
	}
	r.observabilityIngestError.Store(message)
}

// observationRecoveryGrace 是观测消费者重新连上之后继续冻结超时判定的时长：
// 各上报方（每分钟一次的主机快照、周期上报的服务指标）要有机会把中断期间欠下的样本补上，
// 否则旧的 last_seen 一恢复就会把主机和服务判成"已停止上报"。
const observationRecoveryGrace = 5 * time.Minute

// observationState 是观测输入当前的状态：Suspended 时所有依赖观测输入的超时判定都要冻结（既不新增告警，也不恢复已有告警）。
type observationState struct {
	Suspended bool
	// Ready 是观测消费者是否已连接；Reason 是未连接的原因，只在 !Ready 时有意义。
	Ready  bool
	Reason string
}

// observationState 在观测消费者未连接，或刚重新连上、还在恢复宽限期内时报告 Suspended。
// 观测输入没有启用时（没有消费者）永远不冻结。
func (r *Runtime) observationState(enabled bool, now time.Time) observationState {
	if !enabled || r == nil {
		return observationState{Ready: true}
	}
	if !r.ObservabilityIngestReady.Load() {
		return observationState{Suspended: true, Reason: r.observabilityIngestErrorMessage()}
	}
	since := r.ingestReadySince.Load()
	return observationState{Suspended: since != 0 && now.Sub(time.Unix(0, since)) < observationRecoveryGrace, Ready: true}
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
		if r.Gateway != nil {
			r.closeErr = r.Gateway.Close()
		}
		if r.Store != nil {
			r.closeErr = errors.Join(r.closeErr, r.Store.Close())
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
