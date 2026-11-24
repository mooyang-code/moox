package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	observabilityconsumer "github.com/mooyang-code/moox/modules/monitor/internal/observability/eventconsumer"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/metricspb"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server, runtime *Runtime) *report.ModuleMetrics {
	if s == nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, fmt.Errorf("monitor server is unavailable"))
		}
		return nil
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "monitor", report.HealthCheckIDsForModule("monitor"))
	if err != nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, err)
		}
		return nil
	}
	h, err := report.NewHandler(report.DefaultConfig("monitor", "monitor"))
	if err != nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, err)
		}
		log.WarnContextf(trpc.BackgroundContext(), "monitor metrics reporter disabled: %v", err)
		return moduleMetrics
	}
	if runtime != nil {
		h.SetReadiness(runtime.readiness)
		h.SetLifecycle(func() *metricspb.ModuleLifecycle { return runtime.life.Load().ReportState() })
	}
	service := s.Service("trpc.moox.monitor.metrics.timer")
	if service == nil {
		if runtime != nil {
			runtime.setMetricsReporterState(false, fmt.Errorf("metrics timer service is not configured"))
		}
		log.Warn("monitor metrics timer service is not configured, skip register")
		return moduleMetrics
	}
	timer.RegisterHandlerService(service, timerjob.KeepRunning("monitor_metrics", func(ctx context.Context) error {
		err := h.Handle(ctx)
		if errors.Is(err, report.ErrInFlight) {
			// A slow snapshot is still active. Keep the previous readiness state;
			// a skipped tick is neither a fresh success nor a new failure.
			return nil
		}
		if runtime != nil {
			runtime.setMetricsReporterState(err == nil, err)
		}
		return err
	}))
	return moduleMetrics
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startObservabilityConsumer(
	ctx context.Context,
	cfg *config.Config,
	runtime *Runtime,
	hostStore *hostmetrics.Store,
	klineInventory *monmetrics.TaskResultInventoryCache,
) {
	if cfg == nil || !cfg.Observability.Enabled || runtime == nil || runtime.Store == nil ||
		runtime.Store.Ping(ctx) != nil || runtime.Repositories == nil || hostStore == nil {
		return
	}
	var messageStore *monmetrics.MetricMessageStore
	if runtime.MetricStores != nil {
		messageStore = runtime.MetricStores.Messages
	}
	if cfg.Metrics.Enabled && messageStore == nil {
		return
	}
	consumerCfg, err := observabilityConsumerConfig(cfg.Observability)
	if err != nil {
		runtime.setObservabilityIngestState(false, err)
		return
	}
	routes := observabilityconsumer.Routes{
		Metrics: metricsObservabilityRoute(messageStore, runtime.ModuleMetrics, runtime, cfg.Metrics.Enabled),
	}
	if runtime.Repositories != nil {
		routes.Logs = logObservabilityRoute(runtime.Repositories.Logs)
	}
	runtime.Go(func() {
		for ctx.Err() == nil {
			runtime.setObservabilityIngestState(false, errors.New("observability consumer is connecting"))
			jc := jetstream.ConfigFromEnv(cfg.Observability.EventBusURLs, "moox-monitor-observability")
			if path := strings.TrimSpace(cfg.Observability.CredentialFile); path != "" {
				if err := jc.ApplyCredentialFile(jetstream.ExpandCredentialPath(path)); err != nil {
					runtime.setObservabilityIngestState(false, fmt.Errorf("observability consumer credential: %w", err))
					log.WarnContextf(ctx, "observability consumer credential unavailable")
					if !waitObservabilityRetry(ctx) {
						return
					}
					continue
				}
			}
			// EventBus 停掉后 nats.go 会一直重连，消费循环不会返回错误；连接断开与重连要单独接到观测就绪状态上。
			var stopped atomic.Bool
			jc.ConnectionStateHandler = observabilityConnectionHandler(runtime, &stopped)
			js, err := jetstream.Connect(ctx, jc)
			if err != nil {
				runtime.setObservabilityIngestState(false, err)
				log.WarnContextf(ctx, "observability eventbus unavailable; ingestion degraded")
				if !waitObservabilityRetry(ctx) {
					return
				}
				continue
			}
			registry, registryErr := events.DefaultRegistry()
			if registryErr != nil {
				_ = js.Close()
				runtime.setObservabilityIngestState(false, registryErr)
				if !waitObservabilityRetry(ctx) {
					return
				}
				continue
			}
			consumer, bindErr := observabilityconsumer.NewConsumer(ctx, js, registry, consumerCfg, routes)
			if bindErr != nil {
				// DeliverPolicy is immutable for a JetStream durable. A previous
				// release may have created this shared consumer with the opposite
				// policy; remove it once so the configured policy can take effect on
				// the next retry instead of leaving observability permanently stale.
				if strings.Contains(bindErr.Error(), "conflicts in DeliverPolicy") {
					if deleteErr := js.DeleteConsumer(ctx, events.MonitorObservabilityConsumer.Stream(), observabilityconsumer.DefaultConsumer); deleteErr != nil {
						log.WarnContextf(ctx, "observability conflicting durable delete failed: %v", deleteErr)
					}
				}
				_ = js.Close()
				runtime.setObservabilityIngestState(false, bindErr)
				log.WarnContextf(ctx, "observability durable unavailable: %v", bindErr)
				if !waitObservabilityRetry(ctx) {
					return
				}
				continue
			}
			err = consumer.Run(ctx, func() { runtime.setObservabilityIngestState(true, nil) })
			stopped.Store(true)
			_ = consumer.Close()
			_ = js.Close()
			runtime.setObservabilityIngestState(false, err)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.WarnContextf(ctx, "observability ingestion stopped; retrying")
			}
			if !waitObservabilityRetry(ctx) {
				return
			}
		}
	})
}

// observabilityConnectionHandler 把观测消费者连接的断开与重连接到观测就绪状态：断开即未就绪（依赖观测输入的检查冻结），
// 重连后置回就绪并重新开始恢复宽限期。stopped 置位后（本轮连接已收尾）不再改状态，避免迟到的回调覆盖外层循环的结论。
func observabilityConnectionHandler(runtime *Runtime, stopped *atomic.Bool) func(bool, error) {
	return func(connected bool, err error) {
		if stopped.Load() {
			return
		}
		if connected {
			runtime.setObservabilityIngestState(true, nil)
			return
		}
		if err == nil {
			err = errors.New("observability eventbus connection lost")
		}
		runtime.setObservabilityIngestState(false, err)
	}
}

func metricsObservabilityRoute(
	messageStore *monmetrics.MetricMessageStore,
	moduleMetrics *report.ModuleMetrics,
	runtime *Runtime,
	enabled bool,
) func(context.Context, *eventpb.EventMessage, *metricspb.MetricReport) error {
	return func(ctx context.Context, message *eventpb.EventMessage, metricReport *metricspb.MetricReport) error {
		if !enabled {
			return nil
		}
		if messageStore == nil {
			return errors.New("metrics catalog is unavailable")
		}
		if message.GetSpaceId() != events.SystemSpaceID {
			messageStore.RecordIngestRejected(moduleMetrics, metricReport.GetServiceName(), monmetrics.RejectSpace)
			return observabilityconsumer.Permanent(fmt.Errorf("unsupported metric space %q", message.GetSpaceId()))
		}
		observed := message.GetOccurredAt().AsTime()
		samples, err := monmetrics.ParseSnapshot(metricReport.GetSnapshot(), monmetrics.Envelope{
			ServiceName: metricReport.GetServiceName(), InstanceID: metricReport.GetInstanceId(),
			MessageID: message.GetEventId(), ProducerNodeID: metricReport.GetNodeId(),
			ProducerVersion: metricReport.GetServiceVersion(), ObservedAt: observed,
		}, monmetrics.DefaultLimits())
		if err != nil {
			messageStore.RecordIngestRejected(moduleMetrics, metricReport.GetServiceName(), monmetrics.RejectReason(err))
			return observabilityconsumer.Permanent(err)
		}
		parsed := len(samples)
		samples = monmetrics.FilterHealthSamples(samples)
		filtered := parsed - len(samples)
		duplicate, err := messageStore.IsDuplicate(ctx, message.GetEventId())
		if err != nil || duplicate {
			return err
		}
		// 服务指标只保存最新值与目录（SQLite），不写 Storage 历史：Storage 故障不会卡住健康事实的入库。
		if err := messageStore.TouchService(ctx, message, metricReport); err != nil {
			runtime.recordObservabilityWriteFailure(reasonMetricsCatalog, err)
			return err
		}
		duplicate, err = messageStore.CommitIngest(ctx, message, metricReport, samples)
		if err != nil || duplicate {
			if err != nil {
				runtime.recordObservabilityWriteFailure(reasonMetricsCatalog, err)
			}
			return err
		}
		// 过滤数在入库成功且不是重复投递之后才计：失败重试、重复投递不会把同一批过滤样本反复累加。
		monmetrics.RecordSamplesFiltered(metricReport.GetServiceName(), filtered)
		runtime.recordObservabilityWriteSuccess()
		monmetrics.RecordIngest(moduleMetrics, "success", observed)
		return nil
	}
}

// hostReportIngest 处理 HostAgent 随拉取主机策略带来的主机快照：校验、入库，并记下写入成败（进 Monitor 自己的就绪判断）。
func hostReportIngest(store *hostmetrics.Store, runtime *Runtime) func(context.Context, *monitorpb.HostReport) error {
	return func(ctx context.Context, report *monitorpb.HostReport) error {
		validated, err := hostmetrics.ValidateReport(report.GetReportId(), report.GetCollectedAt(), report.GetMetric())
		if err != nil {
			return err
		}
		if store == nil {
			err := errors.New("主机快照处理未初始化")
			runtime.recordHostWriteFailure(err)
			return err
		}
		if err := store.Persist(ctx, validated); err != nil {
			runtime.recordHostWriteFailure(err)
			return err
		}
		runtime.recordHostWriteSuccess()
		return nil
	}
}

func waitObservabilityRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(30 * time.Second):
		return true
	}
}

// 健康原因按出错环节区分，不带原始错误文本（可能含地址或凭据）。
const (
	reasonEventbus       = "eventbus connection unavailable"
	reasonMetricsCatalog = "metrics catalog write failed"
	reasonHostMetrics    = "host metrics write failed"
	reasonAuthSuffix     = ": authentication failed"
)

// failureReason 返回环节原因；凭据类错误追加认证失败说明。
func failureReason(reason string, err error) string {
	if err == nil {
		return ""
	}
	message := strings.ToLower(err.Error())
	for _, secretTerm := range []string{"credential", "authorization", "authentication", "password", "token"} {
		if strings.Contains(message, secretTerm) {
			return reason + reasonAuthSuffix
		}
	}
	return reason
}
