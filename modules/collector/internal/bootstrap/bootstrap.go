// Package bootstrap wires the independent moox-collector service process.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/eastmoneylimit"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	collectorobservability "github.com/mooyang-code/moox/modules/collector/internal/observability"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresult "github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var collectorStartedAt = time.Now()

// Initialize 读 configPath 指向的业务配置（-config），初始化存储并注册 RPC 服务。
// 内置采集任务种子 collection-tasks.yaml 与业务配置在同一目录。
func Initialize(ctx context.Context, s *server.Server, configPath string) (*Runtime, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	process := newRuntime(ctx, s)
	ctx = process.ctx
	keepRuntime := false
	defer func() {
		if !keepRuntime {
			_ = process.Close()
		}
	}()
	log.InfoContextf(ctx, "开始初始化 moox-collector...")

	cfg, err := Load(configPath)
	if err != nil {
		log.ErrorContextf(ctx, "加载 collector 配置失败: %v", err)
		return nil, err
	}
	dbm, err := store.Open(&store.Options{
		Path:            cfg.Database.Path,
		DefaultIngest:   markets.DefaultIngest,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
	})
	if err != nil {
		log.ErrorContextf(ctx, "初始化 collector 数据库失败: %v", err)
		return nil, err
	}
	process.closeDatabase = dbm.Close
	if err := dbm.ApplySchema(collectorschema.AllSQL()); err != nil {
		log.ErrorContextf(ctx, "初始化 collector schema 失败: %v", err)
		return nil, err
	}
	if err := seedBuiltinTasks(ctx, dbm.Tasks(), filepath.Join(filepath.Dir(configPath), builtinTasksFile)); err != nil {
		log.ErrorContextf(ctx, "写入内置采集任务失败: %v", err)
		return nil, err
	}
	process.gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "collector directory refresh failed: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("initialize collector gateway: %w", err)
	}

	// 东方财富的主动限流与冷却：进程内所有直连 *.eastmoney.com 的请求（A 股标的列表、美股交易所对照等）共用，
	// 要早于标签同步等任何会访问东方财富的后台任务启动。关注项与 EventBus 的关注项合并上报，所以共用一个实例。
	attn := attention.New(prometheus.DefaultRegisterer)
	if err := cnstock.ConfigureRequestLimit(cfg.EgressLimits.Eastmoney, eastmoneylimit.New(prometheus.DefaultRegisterer, attn).Observe); err != nil {
		return nil, fmt.Errorf("初始化东方财富限流器: %w", err)
	}

	datasetMetrics, err := report.NewDatasetMetrics(prometheus.DefaultRegisterer, "collector")
	if err != nil {
		return nil, fmt.Errorf("initialize collector dataset metrics: %w", err)
	}
	realtimeInventory := collectorobservability.NewRealtimeInventory(dbm.Tasks(), datasetMetrics)
	realtimeInventory.SetResampleEnabled(cfg.KlineResample.Enabled)
	if err := realtimeInventory.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initialize collector realtime dataset inventory: %w", err)
	}
	resultMetadata, resultMetadataErr := marketstorage.NewGatewayResampleMetadataClient(process.gateway)
	if resultMetadataErr != nil {
		return nil, fmt.Errorf("initialize collector result metadata client: %w", resultMetadataErr)
	}
	resultManager := collectorresult.NewManager(resultMetadata.Client, resultMetadata.View)
	resultManager.SetFieldOffers(marketwiring.FieldOffers())
	// 启动时要连 Storage 核对内置任务的元数据。安装顺序保证首装时它已就绪；以后重启时 Storage 恰好不可用，
	// 就在进程内每 5 秒重试，不退出。
	if err := retryUntilDone(ctx, taskResultMetadataRetryInterval, func(ctx context.Context) error {
		return ensureTaskResultMetadata(ctx, dbm.Tasks(), resultManager)
	}); err != nil {
		return nil, fmt.Errorf("initialize collector task results: %w", err)
	}
	svc := collectsvc.New(dbm, collectsvc.Dependencies{
		DatasetSource:     storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway)),
		RealtimeInventory: realtimeInventory,
		ResultManager:     resultManager,
		// 附加字段由行情源目录里的来源提供：建任务、改任务、任务表单都按目录判断。
		FieldOffers: marketwiring.FieldOffers(),
	})
	collectorpb.RegisterCollectMgrService(s.Service("trpc.moox.collector.CollectMgr"), svc)
	marketFetchMetrics := fetchplan.NewControlMetrics(prometheus.DefaultRegisterer)
	subjectSyncer, err := registerSubjectSyncRuntime(s, cfg, process, dbm.TagSyncRuns())
	if err != nil {
		return nil, err
	}
	svc.SetSubjectSyncer(subjectSyncer)
	streamService := collectsvc.NewStreamService(dbm, collectsvc.StreamSettings{HeartbeatTimeout: time.Duration(cfg.Stream.HeartbeatTimeoutSeconds) * time.Second, CommitReserveMS: cfg.Stream.CommitReserveMS},
		fetchplan.NewStreamMetrics(prometheus.DefaultRegisterer), subjectSyncer)
	streamServer := s.Service("trpc.moox.collector.StreamCollector")
	if streamServer == nil {
		return nil, fmt.Errorf("trpc_go.yaml 缺少 trpc.moox.collector.StreamCollector 服务")
	}
	collectorpb.RegisterStreamCollectorService(streamServer, streamService)
	svc.SetStreamService(streamService)
	bus, err := newEventBusRuntime(cfg, dbm, attn)
	if err != nil {
		return nil, fmt.Errorf("initialize collector event bus: %w", err)
	}
	registerCollectorBackground(ctx, s, cfg, dbm, marketFetchMetrics, process, bus)
	if err := registerDelistDetection(cfg, dbm, process, bus, svc); err != nil {
		return nil, err
	}
	bus.start(process)
	control := &collectorControl{}
	if err := registerFetchPlanManager(cfg, dbm, process, resultManager, marketFetchMetrics, svc, streamService, control, bus); err != nil {
		return nil, err
	}
	healthState, life, err := registerHealth(s, cfg, dbm, process, control, svc)
	if err != nil {
		return nil, err
	}
	registerMetricsReporter(s, realtimeInventory, healthState, life)

	keepRuntime = true
	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return process, nil
}

// taskResultMetadataRetryInterval 是启动时核对任务元数据失败后的重试间隔。
const taskResultMetadataRetryInterval = 5 * time.Second

// retryUntilDone 反复执行 do，直到成功或 ctx 结束（返回最后一次的错误）；每次失败记一条警告。
func retryUntilDone(ctx context.Context, interval time.Duration, do func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := do(ctx)
		if err == nil {
			return nil
		}
		log.WarnContextf(ctx, "核对任务元数据失败（第 %d 次，%s 后重试）：%v", attempt, interval, err)
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
}

func ensureTaskResultMetadata(ctx context.Context, repo *store.TaskRepository, manager *collectorresult.Manager) error {
	if repo == nil || manager == nil {
		return fmt.Errorf("task result metadata dependencies are not configured")
	}
	for page := 1; ; page++ {
		// Provision the result identity for disabled tasks too. A disabled task
		// can be enabled later without requiring an operator-only metadata repair;
		// the scheduler still considers only enabled tasks for execution.
		tasks, total, err := repo.List(ctx, store.TaskFilter{Page: page, PageSize: store.MaxEnabledTasks})
		if err != nil {
			return fmt.Errorf("list collector tasks: %w", err)
		}
		for _, task := range tasks {
			params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
			if err != nil {
				return fmt.Errorf("parse task %s/%s result config: %w", task.SpaceID, task.TaskID, err)
			}
			var ids collectorresult.IDs
			if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
				// Resample targets have a stricter immutable lineage contract and are
				// provisioned by resample.Preparer after it has resolved the source
				// Dataset. The generic result manager must not claim that Dataset as a
				// raw collection result.
				ids = collectorresult.PersistedResultIDs(task.SpaceID, task.TaskID, task.ResultDatasetID, task.ResultViewID)
			} else {
				if len(task.TagIDs) == 0 {
					return fmt.Errorf("task %s/%s has no bound tags", task.SpaceID, task.TaskID)
				}
				logicalIDs := collectorresult.ResultIDsForTask(task.SpaceID, task.TaskID, task.DataType, params.Frequency)
				ids, err = manager.Ensure(ctx, task.SpaceID, task.TaskID, task.DataType, "", collectorresult.Config{
					ViewID:       firstNonEmptyResultID(task.ResultViewID, logicalIDs.ViewID),
					Name:         task.TaskName,
					Description:  task.Description,
					Frequency:    taskResultFrequency(*params),
					SubjectTags:  append([]string(nil), task.TagIDs...),
					OutputFields: params.OutputFields,
				})
				if err != nil {
					return fmt.Errorf("ensure task %s/%s result: %w", task.SpaceID, task.TaskID, err)
				}
			}
			if err := persistTaskResultIDs(ctx, repo, &task, ids); err != nil {
				return err
			}
		}
		if int64(page*store.MaxEnabledTasks) >= total || len(tasks) == 0 {
			break
		}
	}
	return nil
}

// persistTaskResultIDs records the result identities on the task row and in
// its collect params when either differs from what is stored.
func persistTaskResultIDs(ctx context.Context, repo *store.TaskRepository, task *domain.CollectionTask, ids collectorresult.IDs) error {
	needsUpdate := task.ResultDatasetID != ids.DatasetID || task.ResultViewID != ids.ViewID
	task.ResultDatasetID, task.ResultViewID = ids.DatasetID, ids.ViewID
	rawParams := map[string]any{}
	if err := json.Unmarshal([]byte(task.CollectParams), &rawParams); err != nil {
		return fmt.Errorf("decode task %s/%s collect params: %w", task.SpaceID, task.TaskID, err)
	}
	if rawParams["target_dataset_id"] != ids.DatasetID {
		rawParams["target_dataset_id"] = ids.DatasetID
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	encoded, err := json.Marshal(rawParams)
	if err != nil {
		return fmt.Errorf("encode task %s/%s collect params: %w", task.SpaceID, task.TaskID, err)
	}
	task.CollectParams = string(encoded)
	if _, err := repo.UpdateByTaskID(ctx, task.SpaceID, task.TaskID, *task); err != nil {
		return fmt.Errorf("persist task %s/%s result: %w", task.SpaceID, task.TaskID, err)
	}
	return nil
}

func firstNonEmptyResultID(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func taskResultFrequency(params domain.CollectParams) string {
	if strings.EqualFold(params.Collector.DataType, "kline_resample") && strings.TrimSpace(params.TargetFrequency) != "" {
		return params.TargetFrequency
	}
	if strings.TrimSpace(params.Frequency) != "" {
		return params.Frequency
	}
	return params.TargetFrequency
}

type realtimeInventoryReconciler interface {
	Due(time.Time) bool
	Refresh(context.Context) error
}

type metricsReporter interface {
	Handle(context.Context) error
}

// registerMetricsReporter 注册指标上报；上报带上与 /readyz 同一个就绪判断和模块级生命周期（首页的系统状态读它）。
func registerMetricsReporter(s *server.Server, inventory realtimeInventoryReconciler, state *health.State, life *lifecycle.Controller) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("collector", "collector"))
	if err != nil {
		log.Warnf("collector metrics reporter disabled: %v", err)
		return
	}
	if state != nil {
		h.SetReadiness(healthz.Readiness(state.Snapshot))
	}
	if life != nil {
		h.SetLifecycle(life.ReportState)
	}
	service := s.Service("trpc.moox.collector.metrics.timer")
	if service == nil {
		log.Warn("collector metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, metricsTimerHandler(inventory, h, time.Now))
}

func metricsTimerHandler(inventory realtimeInventoryReconciler, reporter metricsReporter, now func() time.Time) func(context.Context) error {
	return func(ctx context.Context) error {
		if inventory != nil && inventory.Due(now()) {
			if err := inventory.Refresh(ctx); err != nil {
				log.WarnContextf(ctx, "collector realtime dataset inventory refresh failed: %v", err)
			}
		}
		return reporter.Handle(ctx)
	}
}

// registerHealth 注册健康检查，并建好生命周期控制器交给 CollectMgr（GetLifecycle、ControlLifecycle）；
// 返回的健康状态与控制器再交给指标上报。
func registerHealth(s *server.Server, cfg *Config, dbm *store.Store, process *Runtime, control *collectorControl, svc *collectsvc.Service) (*health.State, *lifecycle.Controller, error) {
	if cfg == nil {
		return nil, nil, nil
	}
	state := health.New("collector", "collector", "", "")
	state.SnapshotFunc = collectorHealthSnapshot(cfg, dbm, state)
	if s == nil {
		return nil, nil, fmt.Errorf("collector health service is unavailable")
	}
	var life *lifecycle.Controller
	if control != nil && process != nil {
		var err error
		life, err = newCollectorLifecycle(cfg.Database.Path, control)
		if err != nil {
			return nil, nil, fmt.Errorf("collector lifecycle state failed to open: %w", err)
		}
		if svc != nil {
			svc.SetLifecycle(life)
		}
		if !process.launch(func() { life.Run(process.ctx, collectorLifecycleRefresh) }) {
			return nil, nil, fmt.Errorf("collector runtime is closing")
		}
	}
	if err := health.Register(s.Service("trpc.moox.collector.Health"), state); err != nil {
		return nil, nil, fmt.Errorf("collector health server failed to start: %w", err)
	}
	return state, life, nil
}

func collectorHealthSnapshot(cfg *Config, dbm *store.Store, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		// Do not synchronously ping SQLite from /readyz. The collector's single
		// SQLite connection is intentionally shared by the scheduler and the
		// completion consumer; during a large batch, a health probe can otherwise
		// wait behind a writer and make a healthy process appear dead to cron.
		databaseReady := dbm != nil
		state.SetReady(databaseReady)
		rsp := healthz.Base("collector", "collector", "", "", collectorStartedAt, databaseReady)
		rsp.Details = map[string]any{
			"database":       databaseReady,
			"gateway_caller": cfg.GatewayClient.Caller,
		}
		return rsp
	}
}

// registerCollectorBackground 启动 Collector 的后台任务：K 线重采样与保留期清理。
// 维护与清理每轮现取管理集合（managedSpaces，与采集计划同一份名单）；合成覆盖 barclock 登记的全部市场。
// 实时行情采集已全部由 SCF 定时器按采集计划触发（见 registerFetchPlanManager），这里不再有任何行情调度。
func registerCollectorBackground(ctx context.Context, s *server.Server, cfg *Config, dbm *store.Store, metrics *fetchplan.ControlMetrics, process *Runtime, bus *eventBusRuntime) {
	if s == nil || cfg == nil || dbm == nil {
		return
	}
	metadataSource := storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway))
	var resample *resampleRuntime
	if cfg.KlineResample.Enabled {
		runtime, resampleErr := newResampleRuntime(process, dbm, cfg, metadataSource, resampleSpaceIDs(), bus)
		if resampleErr != nil {
			log.WarnContextf(process.ctx, "collector kline resample disabled: %v", resampleErr)
		} else {
			resample = runtime
			resample.prepare(process.ctx)
		}
	}
	maintenanceSpaceCursor := 0
	retentionConfig := cfg.CollectorRetention
	maintenanceRunner := fetchplan.NewMaintenanceRunner(retentionConfig.interval(), retentionConfig.offset(), retentionConfig.timeout(), func(passCtx context.Context) error {
		now := time.Now().UTC()
		var passErrors []error
		// 先封存搁置的补写通知再清理：合成关闭时没有合成定时器来封存，补采作业结束时那一次失败的话只剩这里兜底，
		// 否则没封存的通知过了保留期会被直接清掉。
		if bus != nil {
			if err := sealStaleNotices(passCtx, bus.notices, now); err != nil {
				passErrors = append(passErrors, fmt.Errorf("封存补写通知: %w", err))
			}
		}
		// 补写通知与发件箱的清理不分空间，每轮都做：合成关闭、当前没有任何空间在管理集合里时也一样。
		if err := cleanupNoticeState(passCtx, dbm.Resample(), now); err != nil {
			passErrors = append(passErrors, err)
		}
		// 每轮现取管理集合（与采集计划同一份名单），任务增删、空间启停后下一轮就生效。
		spaceIDs, err := managedSpaces(passCtx, cfg, dbm)
		if err != nil {
			return errors.Join(append(passErrors, fmt.Errorf("读取管理集合失败: %w", err))...)
		}
		if len(spaceIDs) == 0 {
			return errors.Join(passErrors...)
		}
		for _, spaceID := range spaceIDs {
			metrics.ObserveMaintenanceDeletes(spaceID, nil)
		}
		start := maintenanceSpaceCursor % len(spaceIDs)
		maintenanceSpaceCursor = nextMaintenanceSpaceCursor(start, len(spaceIDs))
		for offset := 0; offset < len(spaceIDs); offset++ {
			if passCtx.Err() != nil {
				break
			}
			spaceID := spaceIDs[(start+offset)%len(spaceIDs)]
			deletedRows := make(map[string]int64)
			backfillDeleted, err := dbm.Backfill().CleanupFinishedJobs(passCtx, spaceID, now.Add(-autoBackfillRetention), now.Add(-manualBackfillRetention),
				maintenanceBudget(retentionConfig.MaxRowsPerPass, len(spaceIDs), offset))
			if err != nil {
				passErrors = append(passErrors, fmt.Errorf("cleanup finished backfill jobs for space %s: %w", spaceID, err))
			} else {
				deletedRows["backfill_jobs"] = backfillDeleted
			}
			if err := observeCollectorMaintenanceSpace(passCtx, dbm, metrics, spaceID, now, deletedRows); err != nil {
				log.WarnContextf(passCtx, "Collector store metrics refresh failed space=%s: %v", spaceID, err)
			}
		}
		return errors.Join(passErrors...)
	})
	maintenanceRunner.Metrics = metrics
	// 合成使用独立的分钟定时器（每分钟第 5 秒）：回调只调度有界的后台步骤，源/目标 Storage 的慢 I/O 不会拖住别的定时任务。
	if resampleService := s.Service("trpc.moox.collector.kline_resample.timer"); resampleService == nil {
		if resample != nil {
			log.Warn("collector kline resample timer service is not configured, skip register")
		}
	} else {
		var resampleTickRunning atomic.Bool
		timer.RegisterHandlerService(resampleService, func(ctx context.Context) error {
			if resample == nil {
				return nil
			}
			if !resampleTickRunning.CompareAndSwap(false, true) {
				log.WarnContextf(ctx, "collector kline resample timer tick skipped because the previous tick is still running")
				return nil
			}
			process.launch(func() {
				defer resampleTickRunning.Store(false)
				resample.tick(process.ctx)
			})
			return nil
		})
	}
	if err := process.join(maintenanceRunner.StartWithDone(ctx)); err != nil {
		return
	}
}

func observeCollectorMaintenanceSpace(ctx context.Context, db *store.Store, metrics *fetchplan.ControlMetrics, spaceID string, now time.Time, deleted map[string]int64) error {
	metrics.ObserveMaintenanceDeletes(spaceID, deleted)
	statsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return metrics.RefreshOperationalStats(statsCtx, db, spaceID, now)
}

// 已结束的补采作业的保留期：自动作业量大、只用于观察近况；手动作业保留更久以便追溯。
const (
	autoBackfillRetention   = 7 * 24 * time.Hour
	manualBackfillRetention = 30 * 24 * time.Hour
)

func maintenanceBudget(total, spaces, index int) int {
	if total <= 0 || spaces <= 0 || index < 0 || index >= spaces {
		return 0
	}
	base, remainder := total/spaces, total%spaces
	if index < remainder {
		base++
	}
	return base
}

func nextMaintenanceSpaceCursor(current, spaces int) int {
	if spaces <= 0 {
		return 0
	}
	return (current + 1) % spaces
}

// resampleSpaceIDs 返回 K 线合成的消费者与兜底检查覆盖的空间：barclock 登记的全部市场（没有登记交易日历的空间本来就不能合成）。
// 名单是固定的，新建合成任务不需要重启；Collector 的 EventBus 凭据也按这份名单授权。
func resampleSpaceIDs() []string {
	markets := barclock.Markets()
	spaceIDs := make([]string, 0, len(markets))
	for _, market := range markets {
		spaceIDs = append(spaceIDs, market.ID)
	}
	return spaceIDs
}
