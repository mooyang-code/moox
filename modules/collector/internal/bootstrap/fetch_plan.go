package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/localexec"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresult "github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// managedSpaces 返回管理集合：有启用任务、库里有计划、配置了云函数或处于暂停的空间。只查本地库，不依赖 Admin。
// 暂停的空间必须在内：否则恢复时清不掉暂停开关，收敛也会空转；有计划的空间要留着，停掉最后一个任务后计划才能收尾。
func managedSpaces(ctx context.Context, cfg *Config, dbm *store.Store) ([]string, error) {
	enabled, err := dbm.Tasks().EnabledSpaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list spaces with enabled tasks: %w", err)
	}
	stored, err := dbm.FetchPlans().Spaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list spaces with fetch plans: %w", err)
	}
	pausedSet, err := dbm.FetchPlans().PausedSpaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list paused spaces: %w", err)
	}
	paused := make([]string, 0, len(pausedSet))
	for space := range pausedSet {
		paused = append(paused, space)
	}
	return mergeSpaces(enabled, stored, cfg.scfSpaces(), paused), nil
}

// registerFetchPlanManager 启动采集计划对账：管理集合里的每个空间一个独立协程。对账器始终启动，推送采集不依赖云函数配置。
func registerFetchPlanManager(cfg *Config, dbm *store.Store, process *Runtime, results *collectorresult.Manager, metrics *marketfetch.Metrics, svc *collectsvc.Service, stream *collectsvc.StreamService, control *collectorControl) error {
	managed := func(ctx context.Context) ([]string, error) { return managedSpaces(ctx, cfg, dbm) }
	// 启动时先逐个云函数空间检查部署参数，配置错误尽早暴露。
	for _, space := range cfg.scfSpaces() {
		if _, err := cfg.fetchPlanDeploy(space); err != nil {
			return err
		}
	}
	registrar, err := marketstorage.NewGatewaySnapshotRegistrar(process.gateway, marketstorage.InstTypeSPOT)
	if err != nil {
		return fmt.Errorf("initialize snapshot registrar: %w", err)
	}
	invoker := scfinvoker.New(scfinvoker.Config{Gateway: process.gateway})
	source := storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway))
	operations := make(chan struct{}, cfg.SCFPlan.MaxConcurrentOperations)
	auth, err := marketstorage.ResolveStorageAuthInfo(marketstorage.InstTypeSPOT)
	if err != nil {
		return fmt.Errorf("resolve storage auth: %w", err)
	}
	var reconcilers sync.Map
	var autoScans sync.Map
	checker := &klinecheck.Checker{Plans: dbm.FetchPlans(), Rows: storageio.NewGatewayClient(process.gateway), Auth: auth, Absences: dbm.Backfill(), Fields: marketwiring.FieldOffers()}
	// 应有时间点与函数侧同一套过滤：只有该市场此频率的交易桶才应有 K 线。
	checker.Calendar = markets.TradingBar
	backfiller := &klinecheck.Backfiller{
		Checker: checker, Store: dbm.Backfill(), Plans: dbm.FetchPlans(), Invoker: invoker, BaseCtx: process.ctx, Refill: true,
		Paused: func(ctx context.Context, spaceID string) (bool, error) {
			paused, err := dbm.FetchPlans().PausedSpaces(ctx)
			// 恢复模式：暂停开关仍在（实时采集关闭），但补采要运行，补齐暂停区间。
			return paused[strings.ToLower(spaceID)] && !control.Recovering(spaceID), err
		},
		// 并发上限：SCF 执行端受补采函数的预留并发硬性限制（5）；空间的数据集全部用本地执行端（collector-stream）补采时
		// 不受这个限制，可以放大，补采（尤其是暂停恢复后补暂停区间）快得多。
		MaxInFlightFor: func(ctx context.Context, spaceID string) int {
			modes, err := dbm.IngestModes().List(ctx, spaceID)
			if err != nil || len(modes) == 0 {
				return 0
			}
			for _, mode := range modes {
				if mode.Executor != store.ExecutorLocal {
					return 0
				}
			}
			return klinecheck.LocalMaxInFlight
		},
		Functions: func(spaceID string) klinecheck.FunctionProvider {
			if value, ok := reconcilers.Load(spaceID); ok {
				return value.(*marketfetch.PlanReconciler)
			}
			return nil
		},
		// 补采按数据集的执行端调用：scf 走补采函数，local 调香港机 collector-stream 里的本地执行端。
		Executor: func(ctx context.Context, spaceID, datasetID string) (string, error) {
			modes, err := dbm.IngestModes().List(ctx, spaceID)
			if err != nil {
				return "", err
			}
			if mode, ok := modes[datasetID]; ok && mode.Executor == store.ExecutorLocal {
				return marketfetch.PlanExecutorLocal, nil
			}
			return marketfetch.PlanExecutorSCF, nil
		},
		Local: &localexec.Client{Gateway: process.gateway},
		// 补采作业成功后通知因子：引擎立即推进连续水位、补算上游失败的标的。
		Notify: klinecheck.FactorNotifier(process.gateway),
	}
	process.beforeClose = append(process.beforeClose, func() error { backfiller.Wait(); return nil })
	svc.SetKlineOps(backfiller)
	svc.SetActivityLogger(invoker)
	manager := &marketfetch.PlanManager{
		Configured: managed,
		Spaces: func(ctx context.Context) ([]string, error) {
			// 运行集合：管理集合 ∩ Admin 中仍启用的空间 − 暂停的空间；空间被停用或删除后，管理器先停定时器再退出该空间的协程。
			spaces, err := managed(ctx)
			if err != nil {
				return nil, err
			}
			active, err := invoker.ListActiveSpaceIDs(ctx)
			if err != nil {
				return nil, err
			}
			// 暂停的空间不在运行集合里：管理器会先停用它的定时器再退出协程，恢复后重新启用。
			paused, err := dbm.FetchPlans().PausedSpaces(ctx)
			if err != nil {
				return nil, err
			}
			running := make([]string, 0, len(spaces))
			for _, space := range spaces {
				if _, ok := active[strings.ToLower(space)]; ok && !paused[strings.ToLower(space)] {
					running = append(running, space)
				}
			}
			return running, nil
		},
		Factory: func(spaceID string) (*marketfetch.PlanReconciler, error) {
			latenessSeconds, err := defaultLatenessSeconds(spaceID)
			if err != nil {
				return nil, err
			}
			options := marketfetch.PlanOptions{
				StreamLatenessS: cfg.Stream.LatenessS,
				SpaceID:         spaceID, Markets: cfg.SCFMarkets[strings.ToLower(spaceID)], SymbolsPerFunction: cfg.SCFPlan.symbolsPerFunction(spaceID),
				Fetch: cfg.SCFPlan.fetchFor(spaceID, ""), MarketFetch: cfg.SCFPlan.marketFetches(spaceID),
				DefaultLatenessS: latenessSeconds, MarketLatenessS: cfg.SCFPlan.marketLateness(spaceID), AssumedEgressIPs: cfg.SCFPlan.AssumedEgressIPs,
			}
			if strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
				// A 股的函数内并发等 fetch 参数由各源的限速配置推导，不再在计划配置里手填。
				options.DeriveFetch = marketfetch.StockCNFetchLimits()
				if sizing := cfg.SCFPlan.autoSizing(spaceID); sizing != nil {
					options.AutoSize = marketfetch.StockCNAutoSize(*sizing)
				}
			}
			deploy, err := cfg.fetchPlanDeploy(spaceID)
			if err != nil {
				return nil, err
			}
			reconciler := &marketfetch.PlanReconciler{
				Preflight: calendarPreflight(spaceID),
				SpaceID:   spaceID, Options: options, Deploy: deploy,
				Groups: &marketfetch.GroupBuilder{
					Tasks: dbm.Tasks(), Symbols: source, ResolveSourceID: marketwiring.DefaultSourceID, ResolveSymbol: marketwiring.ResolveSymbol,
				},
				Store: dbm.FetchPlans(), Storage: registrar, Cloud: invoker, Access: cfg.scfPlanAccessEnvironment, Lateness: results,
				Activity: invoker, Metrics: metrics, Operations: operations,
				Modes: dbm.IngestModes(), DefaultMode: cfg.SCFPlan.defaultModeFor(spaceID), Workers: dbm.StreamWorkers(),
				StreamOnline: func(ctx context.Context) (bool, error) { return stream.Online(ctx, spaceID) },
				Exclusions:   dbm.SubjectExclusions(),
			}
			if balance := cfg.SCFPlan.Spaces[strings.ToLower(spaceID)].EgressBalance; balance != nil {
				balancer, err := egressbalance.New(balance.config())
				if err != nil {
					return nil, fmt.Errorf("scf_plan.spaces[%s].egress_balance: %w", spaceID, err)
				}
				reconciler.Egress = balancer
			}
			reconcilers.Store(spaceID, reconciler)
			return reconciler, nil
		},
		AfterTick: func(ctx context.Context, spaceID string) error {
			if err := backfiller.Tick(ctx, spaceID); err != nil {
				return err
			}
			// 自动补采扫描要逐标的读取 Storage，耗时远超一轮对账的预算：放到独立协程里跑，
			// 每个空间同一时间只跑一个，用进程上下文加较长的超时。
			if _, running := autoScans.LoadOrStore(spaceID, struct{}{}); running {
				return nil
			}
			if !process.launch(func() {
				defer autoScans.Delete(spaceID)
				scanCtx, cancel := context.WithTimeout(process.ctx, 15*time.Minute)
				defer cancel()
				if err := backfiller.AutoScan(scanCtx, spaceID); err != nil {
					log.WarnContextf(scanCtx, "collector auto backfill scan failed space=%s: %v", spaceID, err)
				}
				// 早于数据集最早生效点的历史缺口，检查与自动补采都看不到：定期读回并写日志，不补采。
				if _, err := backfiller.ScanHistory(scanCtx, spaceID); err != nil {
					log.WarnContextf(scanCtx, "collector history scan failed space=%s: %v", spaceID, err)
				}
			}) {
				autoScans.Delete(spaceID)
			}
			return nil
		},
	}
	svc.SetFetchPlanControl(manager)
	svc.SetSourceIDResolver(marketwiring.DefaultSourceID)
	control.bind(dbm.FetchPlans(), backfiller, manager, managed)
	if !process.launch(func() { control.runRecovery(process.ctx) }) {
		return fmt.Errorf("collector runtime is closing")
	}
	// 心跳中断时在线指标也要变：定时按心跳超时刷新。
	if !process.launch(func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-process.ctx.Done():
				return
			case <-ticker.C:
				stream.RefreshOnline(process.ctx, control.managed(process.ctx))
			}
		}
	}) {
		return fmt.Errorf("collector runtime is closing")
	}
	if !process.launch(func() { manager.Run(process.ctx) }) {
		return fmt.Errorf("collector runtime is closing")
	}
	log.Infof("collector fetch plan reconciliation enabled spaces=%s", strings.Join(control.managed(process.ctx), ","))
	return nil
}

// calendarPreflight 检查空间所在市场的交易日历覆盖期：不足 60 天告警，不足 14 天或已过期则拒绝创建新计划
// （覆盖期之外无法判断哪些周期是交易桶）。连续交易的市场没有交易日历，总是通过。
func calendarPreflight(spaceID string) func(context.Context) error {
	return func(ctx context.Context) error {
		now := time.Now()
		if err := markets.CheckCalendarHorizon(spaceID, now, 14); err != nil {
			return fmt.Errorf("%s 交易日历覆盖期不足：%w", spaceID, err)
		}
		if err := markets.CheckCalendarHorizon(spaceID, now, 60); err != nil {
			log.WarnContextf(ctx, "%s 交易日历覆盖期快用完了：%v", spaceID, err)
		}
		return nil
	}
}
