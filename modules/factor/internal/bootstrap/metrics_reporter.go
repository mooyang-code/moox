package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/report"
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

// observeTimeout 是收到心跳后刷新因子数据集指标的最长时间。
const observeTimeout = 10 * time.Second

// registerMetricsReporter publishes moox-factor-mgr's metrics snapshot every
// 30s. Computation runs in moox-factor-engine on an operator machine, so each
// tick first turns the engine's heartbeat into factor dataset metrics; Monitor
// then checks factor results for freshness like collected data.
// 引擎每算完一期就补发心跳：收到心跳时也立刻刷新数据集指标，水位前进就补报一份快照，
// Monitor 约 1 秒内看到这一期的完成时刻，不用等 30 秒的定时上报。
func registerMetricsReporter(s *server.Server, engine engineView) error {
	service := s.Service(factorMetricsTimerService)
	if service == nil {
		return fmt.Errorf("factor metrics timer service %s is not configured", factorMetricsTimerService)
	}
	handler, err := report.NewHandler(report.DefaultConfig("factor", "factor-mgr"))
	if err != nil {
		return fmt.Errorf("create factor metrics reporter: %w", err)
	}
	datasets, err := report.NewDatasetMetrics(prometheus.DefaultRegisterer, "factor")
	if err != nil {
		return fmt.Errorf("create factor dataset metrics: %w", err)
	}
	observer := newFactorDatasetObserver(engine, datasets)
	presence, err := report.NewWorkerPresence(prometheus.DefaultRegisterer, "factor")
	if err != nil {
		return fmt.Errorf("create factor engine presence metric: %w", err)
	}
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
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		if err := observer.observe(ctx); err != nil {
			log.WarnContextf(ctx, "factor dataset metrics not refreshed: %v", err)
		}
		// 每 30 秒评估一次引擎在线状态并随快照上报，Monitor 据此发出“因子引擎离线”告警。
		if source, ok := engine.(presenceSource); ok {
			if state, engineID, last, err := source.Presence(ctx); err != nil {
				log.WarnContextf(ctx, "factor engine presence not evaluated: %v", err)
			} else {
				presence.Set(state, engineID, last)
			}
		}
		return handler.Handle(ctx)
	})
	return nil
}
