package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const factorMetricsTimerService = "trpc.moox.factor.metrics.timer"

// heartbeatSource 是引擎中枢的心跳回调登记，见 enginehub.Hub.OnHeartbeat。
type heartbeatSource interface {
	OnHeartbeat(func())
}

// presenceSource 是引擎在线状态评估，见 enginehub.Hub.Presence。
type presenceSource interface {
	Presence(ctx context.Context) (int, string, time.Time, error)
}

// delegateSource 是代引擎上报给 Monitor 的一行，见 enginehub.Hub.DelegatedReport。
type delegateSource interface {
	DelegatedReport(ctx context.Context) (*metricspb.DelegatedReport, error)
}

// observeTimeout 是收到心跳后刷新因子数据集指标的最长时间。
const observeTimeout = 10 * time.Second

// registerMetricsReporter publishes moox-factor-mgr's metrics snapshot every
// 30s. Computation runs in moox-factor-engine on an operator machine, so each
// tick first turns the engine's heartbeat into factor dataset metrics; Monitor
// then checks factor results for freshness like collected data.
// 引擎每算完一期就补发心跳：收到心跳时也立刻刷新数据集指标，水位前进就补报一份快照，
// Monitor 约 1 秒内看到这一期的完成时刻，不用等 30 秒的定时上报。
// 上报还带上 factor-mgr 自己的就绪判断与模块级生命周期，以及代引擎上报的一行（引擎的就绪与生命周期取自它的心跳）。
func registerMetricsReporter(s *server.Server, engine engineView, readiness func(context.Context) (bool, string), life *lifecycle.Controller) error {
	service := s.Service(factorMetricsTimerService)
	if service == nil {
		return fmt.Errorf("factor metrics timer service %s is not configured", factorMetricsTimerService)
	}
	handler, err := report.NewHandler(report.DefaultConfig("factor", "factor-mgr"))
	if err != nil {
		return fmt.Errorf("create factor metrics reporter: %w", err)
	}
	handler.SetReadiness(readiness)
	handler.SetLifecycle(life.ReportState)
	if source, ok := engine.(delegateSource); ok {
		handler.SetDelegates(func(ctx context.Context) []*metricspb.DelegatedReport {
			delegated, err := source.DelegatedReport(ctx)
			if err != nil {
				log.WarnContextf(ctx, "factor engine delegated report not evaluated: %v", err)
			}
			if delegated == nil {
				return nil
			}
			return []*metricspb.DelegatedReport{delegated}
		})
	}
	datasets, err := report.NewDatasetMetrics(prometheus.DefaultRegisterer, "factor")
	if err != nil {
		return fmt.Errorf("create factor dataset metrics: %w", err)
	}
	observer := newFactorDatasetObserver(engine, datasets)
	attention, err := report.NewAttention(prometheus.DefaultRegisterer, attentionKinds)
	if err != nil {
		return fmt.Errorf("create factor attention metric: %w", err)
	}
	attention.AddSource("watermark", observer.watermarkAttention)
	handler.FlushOnOutputAdvance()
	if source, ok := engine.(heartbeatSource); ok {
		source.OnHeartbeat(func() {
			go func() {
				ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), observeTimeout)
				defer cancel()
				if err := observer.observe(ctx); err != nil {
					log.WarnContextf(ctx, "factor dataset metrics not refreshed after heartbeat: %v", err)
				}
			}()
		})
	}
	timer.RegisterHandlerService(service, timerjob.KeepRunning("factor_metrics", func(ctx context.Context) error {
		if err := observer.observe(ctx); err != nil {
			log.WarnContextf(ctx, "factor dataset metrics not refreshed: %v", err)
		}
		// 每 30 秒评估一次引擎在线状态，离线时上报关注项 engine_offline；读不到时保留上一次的结果。
		if source, ok := engine.(presenceSource); ok {
			if state, engineID, last, err := source.Presence(ctx); err != nil {
				log.WarnContextf(ctx, "factor engine presence not evaluated: %v", err)
			} else {
				attention.Set("engine", map[string][]string{attentionEngineOffline: report.PresenceAttention(state, engineID, last)})
			}
		}
		return handler.Handle(ctx)
	}))
	return nil
}
