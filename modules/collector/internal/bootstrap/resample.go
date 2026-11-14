package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/resample/eventconsumer"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rs/xid"
	"trpc.group/trpc-go/trpc-go/log"
)

// resampleRuntime 汇总本地 K 线合成的各个部分：准备目标数据集、兜底检查、历史作业与补写通知发布。
// 事件主路径（订阅源数据集的周期完成事件与补采完成事件）由 consumers 承担，没有配置事件总线时只靠兜底检查。
type resampleRuntime struct {
	cfg       KlineResampleConfig
	preparer  *collectorresample.Preparer
	fallbacks []*collectorresample.Fallback
	jobs      *collectorresample.JobRunner
	publisher *collectorresample.NoticePublisher
	state     *store.ResampleStateRepository
}

// jobDrainBudget 是定时器每一轮里连续推进历史作业的预算，必须小于一分钟，让下一轮兜底检查如期开始。
const jobDrainBudget = 40 * time.Second

// newResampleRuntime 构造合成运行时；元数据或存储客户端不可用时返回错误，调用方按“合成关闭”处理。
func newResampleRuntime(process *Runtime, dbm *store.Store, cfg *Config, source *storagesource.DatasetSource, spaceIDs []string, bus *eventBusRuntime) (*resampleRuntime, error) {
	metadataClient, err := marketstorage.NewGatewayResampleMetadataClient(process.gateway, marketstorage.InstTypeSPOT)
	if err != nil {
		return nil, fmt.Errorf("metadata client: %w", err)
	}
	localStorage, err := marketstorage.NewGatewayResampleStorage(process.gateway, marketstorage.InstTypeSPOT)
	if err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	view, ok := localStorage.(collectorresample.ViewWaiter)
	if !ok {
		return nil, errors.New("Storage 适配器没有 DataView 方法")
	}
	catalog := &collectorresample.Catalog{Metadata: metadataClient.Client, Auth: metadataClient.Auth, View: view}
	k := cfg.KlineResample
	metrics := collectorresample.NewMetrics(prometheus.DefaultRegisterer)
	engine := &collectorresample.Engine{
		Primary: localStorage, State: dbm.Resample(), Absences: dbm.Backfill(), Metrics: metrics,
		ExecTimeout: k.ExecTimeout, MaxReadKeys: k.MaxReadKeys,
	}
	if period, ok := localStorage.(collectorresample.PeriodStorage); ok {
		engine.Period = period
	}
	if registrar, regErr := marketstorage.NewGatewaySnapshotRegistrar(process.gateway, marketstorage.InstTypeSPOT); regErr == nil {
		engine.Snapshots = registrar
	} else {
		log.WarnContextf(process.ctx, "collector kline resample snapshot registration disabled: %v", regErr)
	}
	runtime := &resampleRuntime{
		cfg:      k,
		preparer: &collectorresample.Preparer{Tasks: dbm.Tasks(), Source: source, Catalog: catalog, Limit: 50},
		state:    dbm.Resample(),
	}
	for _, spaceID := range spaceIDs {
		runtime.fallbacks = append(runtime.fallbacks, &collectorresample.Fallback{
			Tasks: dbm.Tasks(), Source: source, Engine: engine, Metrics: metrics,
			Config: collectorresample.FallbackConfig{SpaceID: spaceID, LookbackBars: k.FallbackLookbackBars, Grace: k.FallbackGrace, MaxBucketsPerTick: k.FallbackMaxBuckets},
		})
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	runtime.publisher = &collectorresample.NoticePublisher{State: dbm.Resample(), Registry: registry}
	if bus != nil {
		runtime.publisher.Enabled, runtime.publisher.Wake = true, bus.relay.Wake
	}
	jobs := &collectorresample.JobRunner{
		State: dbm.Resample(), Tasks: dbm.Tasks(), Source: source, Engine: engine,
		Flush:  runtime.publisher.Flush,
		Config: collectorresample.JobRunnerConfig{BucketsPerRound: k.HistoryBucketsPerRound, MaxJobsPerRound: k.HistoryMaxJobs},
	}
	runtime.jobs = jobs

	if bus != nil {
		index := &collectorresample.TaskIndex{Tasks: dbm.Tasks()}
		// 启动时事件总线可能还没起来：Collector 级的连接管理器每分钟重试，连上之后才创建消费者。
		bus.hub.OnConnected("K 线合成消费者", func(_ context.Context, client *jetstream.Client) (func() error, error) {
			consumers, startErr := eventconsumer.Start(process.ctx, client, eventconsumer.ConsumersConfig{SpaceIDs: spaceIDs}, registry,
				&eventconsumer.PeriodHandler{Registry: registry, Trigger: &collectorresample.PeriodTrigger{Index: index, Engine: engine, Metrics: metrics}},
				&eventconsumer.BackfilledHandler{
					Registry: registry,
					Trigger: &collectorresample.BackfillTrigger{
						Index: index, State: dbm.Resample(), SettleAfter: 2 * k.ExecTimeout,
						NewID: func() string { return xid.New().String() },
					},
				})
			if startErr != nil {
				return nil, startErr
			}
			return consumers.Close, nil
		})
	}
	return runtime, nil
}

// prepare 在启动时做一次目标数据集准备。
func (r *resampleRuntime) prepare(ctx context.Context) {
	prepareCtx, cancel := context.WithTimeout(ctx, r.cfg.ScanTimeout)
	defer cancel()
	if err := r.preparer.RunOnce(prepareCtx); err != nil {
		log.WarnContextf(ctx, "collector kline resample initial preparation failed: %v", err)
	}
}

// tick 是每分钟第 5 秒的定时器回调内容：准备目标数据集，兜底检查，封存补写通知，连续推进历史作业。
func (r *resampleRuntime) tick(ctx context.Context) {
	step := func(name string, run func(context.Context) error) {
		stepCtx, cancel := context.WithTimeout(ctx, r.cfg.ScanTimeout)
		defer cancel()
		if err := run(stepCtx); err != nil {
			log.WarnContextf(ctx, "collector kline resample %s failed: %v", name, err)
		}
	}
	step("preparation", r.preparer.RunOnce)
	for _, fallback := range r.fallbacks {
		fallback := fallback
		step("fallback check space="+fallback.Config.SpaceID, func(c context.Context) error { return fallback.Tick(c, time.Now().UTC()) })
	}
	step("notice publish", r.publisher.Flush)
	if err := r.jobs.Drain(ctx, jobDrainBudget); err != nil {
		log.WarnContextf(ctx, "collector kline resample jobs failed: %v", err)
	}
}

// cleanup 清理过期的补写通知、已发布事件与已结束的历史作业。
func (r *resampleRuntime) cleanup(ctx context.Context, now time.Time) error {
	var errs []error
	if _, err := r.state.CleanupNotices(ctx, now.Add(-noticeRetention)); err != nil {
		errs = append(errs, fmt.Errorf("cleanup backfill notices: %w", err))
	}
	if _, err := r.state.CleanupFinishedJobs(ctx, now.Add(-resampleJobRetention)); err != nil {
		errs = append(errs, fmt.Errorf("cleanup resample jobs: %w", err))
	}
	return errors.Join(errs...)
}

// 补写通知（含未成批的）与已结束历史作业的保留期。
const (
	noticeRetention      = 7 * 24 * time.Hour
	resampleJobRetention = 30 * 24 * time.Hour
)
