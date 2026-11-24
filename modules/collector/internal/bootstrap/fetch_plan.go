package bootstrap

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/adminrpc"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresult "github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
func registerFetchPlanManager(cfg *Config, dbm *store.Store, process *Runtime, results *collectorresult.Manager, metrics *fetchplan.ControlMetrics, svc *collectsvc.Service, stream *collectsvc.StreamService, control *collectorControl, bus *eventBusRuntime) error {
	managed := func(ctx context.Context) ([]string, error) { return managedSpaces(ctx, cfg, dbm) }
	// 启动时先逐个云函数空间检查部署参数，配置错误尽早暴露。
	for _, space := range cfg.scfSpaces() {
		if _, err := cfg.fetchPlanDeploy(space); err != nil {
			return err
		}
	}
	registrar, err := marketstorage.NewGatewaySnapshotRegistrar(process.gateway)
	if err != nil {
		return fmt.Errorf("initialize snapshot registrar: %w", err)
	}
	admin := adminrpc.New(process.gateway)
	if err := tencentscf.ConfigureRateLimits(cfg.Cloud.TencentSCF.APIRateLimits); err != nil {
		return fmt.Errorf("cloud.tencent_scf.api_rate_limits: %w", err)
	}
	// 腾讯云直接在 Collector 里调：云账号密钥经网关读 Admin 的 GetCloudCredential，创建或更新采集函数时补齐 CLS 上报环境。
	credentials := &cloud.GatewayCredentials{Gateway: process.gateway}
	functions := cloud.NewFunctions(dbm, credentials, cloud.NewCLSProvisioner(cfg.Cloud.CLS, nil))
	packages := cloud.NewPackages(dbm, credentials)
	backend := cloudBackend{TencentFunctions: functions, packages: packages}
	source := storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway))
	operations := make(chan struct{}, cfg.SCFPlan.MaxConcurrentOperations)
	reconcilers := &fetchplan.Reconcilers{}
	var autoScans sync.Map
	checker := &klinecheck.Checker{Timeline: dbm.SeriesTimeline(), Rows: storageio.NewGatewayClient(process.gateway), Absences: dbm.Backfill(), Fields: marketwiring.FieldOffers()}
	// 应有时间点与函数侧同一套过滤：只有该市场此频率的交易桶才应有 K 线。
	checker.Calendar = markets.TradingBar
	backfiller := &klinecheck.Backfiller{
		Checker: checker, Store: dbm.Backfill(), Plans: dbm.FetchPlans(), Invoker: reconcilers, BaseCtx: process.ctx, Refill: true,
		Paused: func(ctx context.Context, spaceID string) (bool, error) {
			space := strings.ToLower(spaceID)
			manual, err := dbm.FetchPlans().ManuallyPausedSpaces(ctx)
			if err != nil || manual[space] {
				// 手工暂停的空间不补采，系统恢复追赶时也一样。
				return true, err
			}
			paused, err := dbm.FetchPlans().PausedSpaces(ctx)
			// 恢复模式：系统暂停仍在（实时采集关闭），但补采要运行，补齐暂停区间。
			return paused[space] && !control.Recovering(spaceID), err
		},
		// 并发上限：云函数补采受补采函数的预留并发硬性限制（5）；空间的 K 线任务都不用云函数（采集方式是 stream 或 local，
		// 补采走 collector-stream 的本地执行端）时不受这个限制，可以放大，补采（尤其是暂停恢复后补暂停区间）快得多。
		MaxInFlightFor: func(ctx context.Context, spaceID string) int {
			tasks, err := dbm.Tasks().ListKline(ctx, spaceID, "")
			if err != nil || len(tasks) == 0 {
				return 0
			}
			for _, task := range tasks {
				if task.Ingest == domain.IngestSCF {
					return 0
				}
			}
			return klinecheck.LocalMaxInFlight
		},
		Functions: func(spaceID string) klinecheck.FunctionProvider {
			if reconciler := reconcilers.Get(spaceID); reconciler != nil {
				return reconciler
			}
			return nil
		},
		Executor: backfillExecutor(dbm.Tasks(), markets.DefaultIngest),
		Local:    &fetchplan.LocalExecutorClient{Gateway: process.gateway},
		// 收市后整天重取：时刻取各市场 market.yaml 的 refetch_after_close_minutes，没配的市场不做。
		CloseRefetch: markets.CloseRefetchDay, RefetchFrequency: routedFrequency,
	}
	if bus != nil {
		// 补采作业结束后立刻把分块的补写通知封存成事件，不等维护轮或合成的定时器：因子引擎和合成都订阅这个事件。
		backfiller.FlushNotices = bus.notices.Flush
	}
	// 退出时先等补采协程，再取消并等待全部空间在跑的云端操作（之后不再接受新操作）。
	process.beforeClose = append(process.beforeClose, func() error { backfiller.Wait(); return nil }, func() error { reconcilers.Close(); return nil })
	svc.SetKlineOps(backfiller)
	svc.SetActivityLogger(admin)
	svc.SetCloud(collectsvc.CloudDeps{
		Accounts: cloud.NewAccounts(dbm), Packages: packages, Reconcilers: reconcilers,
		AccountInUse: func(ctx context.Context, accountID string) error {
			return cfg.cloudAccountInUse(ctx, functions, accountID)
		},
	})
	manager := &fetchplan.PlanManager{
		Configured: managed,
		Spaces: func(ctx context.Context) ([]string, error) {
			// 运行集合：管理集合 ∩ Storage 中仍启用的空间 − 暂停的空间；空间被停用或删除后，管理器先停定时器再退出该空间的协程。
			spaces, err := managed(ctx)
			if err != nil {
				return nil, err
			}
			active, err := activeSpaceIDs(ctx, storageio.NewGatewayClient(process.gateway))
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
		Factory: func(spaceID string) (*fetchplan.PlanReconciler, error) {
			latenessSeconds, err := defaultLatenessSeconds(spaceID)
			if err != nil {
				return nil, err
			}
			options := fetchplan.PlanOptions{
				StreamLatenessS: cfg.Stream.LatenessS,
				SpaceID:         spaceID, Markets: cfg.SCFMarkets[strings.ToLower(spaceID)], BackfillRegion: cfg.SCFPlan.Spaces[strings.ToLower(spaceID)].BackfillRegion, SymbolsPerFunction: cfg.SCFPlan.symbolsPerFunction(spaceID),
				Fetch: cfg.SCFPlan.fetchFor(spaceID, ""), MarketFetch: cfg.SCFPlan.marketFetches(spaceID),
				DefaultLatenessS: latenessSeconds, MarketLatenessS: cfg.SCFPlan.marketLateness(spaceID),
			}
			// 有路由文件的市场，函数内并发等 fetch 参数由各源的限速配置推导，不在计划配置里手填；没有路由文件的市场保持配置值。
			options.DeriveFetch = fetchplan.RouteFetchLimits(spaceID)
			// 每函数标的数的估算看 market.yaml 的 scf_plan.auto_size 配没配；同样只对有路由文件的市场算得出来。
			if sizing := cfg.SCFPlan.autoSizing(spaceID); sizing != nil {
				options.AutoSize = fetchplan.RouteAutoSize(spaceID, *sizing)
			}
			deploy, err := cfg.fetchPlanDeploy(spaceID)
			if err != nil {
				return nil, err
			}
			reconciler := &fetchplan.PlanReconciler{
				Preflight: calendarPreflight(spaceID),
				SpaceID:   spaceID, Options: options, Deploy: deploy,
				Groups: &fetchplan.GroupBuilder{
					Tasks: dbm.Tasks(), Symbols: source, ResolveSourceID: marketwiring.DefaultSourceID, ResolveSymbol: marketwiring.ResolveSymbol,
				},
				Store: dbm.FetchPlans(), Storage: registrar, Cloud: backend, Entry: cfg.scfPlanEntryEnvironment, Lateness: results,
				Activity: admin, Metrics: metrics, Operations: operations, OtherSpaces: otherSCFSpaces(cfg.SCFMarkets, spaceID),
				Workers:      dbm.StreamWorkers(),
				StreamOnline: func(ctx context.Context) (bool, error) { return stream.Online(ctx, spaceID) },
				// 进程启动不满一个心跳超时不按离线放行：重启后心跳表里的时间是旧的。
				StartedAt: collectorStartedAt, StreamTimeout: time.Duration(cfg.Stream.HeartbeatTimeoutSeconds) * time.Second,
				Exclusions: dbm.SubjectExclusions(),
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
			// 先提交收市后的整天重取（只在“收市 + 延迟”的那一轮有事可做），紧接着的 Tick 就能认领它的分块；失败不挡住补采推进。
			if err := backfiller.RefetchAfterClose(ctx, spaceID); err != nil {
				log.WarnContextf(ctx, "collector close refetch failed space=%s: %v", spaceID, err)
			}
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
	if !process.launch(func() { runPackageCleanup(process.ctx, packages) }) {
		return fmt.Errorf("collector runtime is closing")
	}
	log.Infof("collector fetch plan reconciliation enabled spaces=%s", strings.Join(control.managed(process.ctx), ","))
	return nil
}

// routedFrequency 报告市场的路由文件里有没有这个周期的 K 线路由（数据源声明支持的周期）；没有路由文件的市场不限周期。
func routedFrequency(spaceID, frequency string) bool {
	route, found, err := marketfetch.LoadRoute(spaceID)
	if err != nil {
		log.Warnf("collector close refetch: 读 %s 路由失败：%v", spaceID, err)
		return false
	}
	if !found {
		return true
	}
	_, err = route.Kline(frequency)
	return err == nil
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

// cloudBackend 是对账器用到的云端能力：云函数操作加按包名取最新的可用代码包。
type cloudBackend struct {
	*cloud.TencentFunctions
	packages *cloud.Packages
}

func (b cloudBackend) LatestPackage(ctx context.Context, spaceID, packageName string) (*cloud.Package, error) {
	return b.packages.Latest(ctx, spaceID, packageName)
}

// otherSCFSpaces 返回除 spaceID 以外配置了云函数的空间各自的市场与地域：对账器据此排除别的空间的函数名。
func otherSCFSpaces(markets map[string]map[string][]string, spaceID string) map[string]map[string][]string {
	out := map[string]map[string][]string{}
	for space, byMarket := range markets {
		if !strings.EqualFold(space, spaceID) {
			out[space] = byMarket
		}
	}
	return out
}

// cloudAccountInUse 判断云账号还在不在用：被 scf_plan.cloud_account_id 引用，或者云上（配置过的地域里）还有它建的采集函数。
// 账号删掉后这些函数没人管，定时器会一直开着，所以有函数时也拒绝；读不了云上时同样拒绝。代码包由 cloud.Accounts.Delete 检查。
func (c *Config) cloudAccountInUse(ctx context.Context, functions cloud.Functions, accountID string) error {
	if strings.EqualFold(strings.TrimSpace(c.SCFPlan.CloudAccountID), strings.TrimSpace(accountID)) {
		return fmt.Errorf("%w：scf_plan.cloud_account_id 引用了它", cloud.ErrAccountInUse)
	}
	regions := make([]string, 0, len(c.SCFRegions))
	for region := range c.SCFRegions {
		regions = append(regions, region)
	}
	sort.Strings(regions)
	for _, region := range regions {
		refs, err := functions.List(ctx, accountID, region, "moox-fetch")
		if err != nil {
			return fmt.Errorf("%w：无法确认 %s 里没有它建的函数：%v", cloud.ErrAccountInUse, region, err)
		}
		if len(refs) > 0 {
			return fmt.Errorf("%w：%s 里还有 %d 个它建的函数（例如 %s）。collector function delete 只删当前配置账号下的函数，"+
				"删不到这个账号的：用 moox-cli setup inspect-scf 查出这些函数，在腾讯云控制台手工删除后再删账号", cloud.ErrAccountInUse, region, len(refs), refs[0].Name)
		}
	}
	return nil
}

// packageCleanupInterval 是代码包清理的间隔：每个（空间、包名）只留最新 3 个可用包，上传超过 6 小时仍没完成的删除。
const packageCleanupInterval = 24 * time.Hour

// runPackageCleanup 每天清理一次代码包（启动 10 分钟后第一次）。COS 删除可能较慢，不放进每分钟一轮的维护任务。
func runPackageCleanup(ctx context.Context, packages *cloud.Packages) {
	timer := time.NewTimer(10 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		result, err := packages.Cleanup(cleanupCtx)
		cancel()
		switch {
		case err != nil:
			log.WarnContextf(ctx, "collector code package cleanup failed: %v", err)
		case len(result.Deleted) > 0 || len(result.Failed) > 0:
			log.InfoContextf(ctx, "collector code package cleanup deleted=%s failed=%s", strings.Join(result.Deleted, ","), strings.Join(result.Failed, "; "))
		}
		timer.Reset(packageCleanupInterval)
	}
}

// backfillExecutor 按任务的采集方式选补采执行端：scf 走补采函数，stream 与 local 调 collector-stream 里的本地执行端。
// 任务已删除、只保留了数据的数据集查不到任务，按空间的默认采集方式（defaultIngest）选。
func backfillExecutor(tasks *store.TaskRepository, defaultIngest func(string) string) func(context.Context, string, string) (string, error) {
	return func(ctx context.Context, spaceID, datasetID string) (string, error) {
		listed, err := tasks.ListKline(ctx, spaceID, datasetID)
		if err != nil {
			return "", err
		}
		ingest := defaultIngest(spaceID)
		if len(listed) > 0 {
			ingest = listed[0].Ingest
		}
		if ingest == domain.IngestStreaming || ingest == domain.IngestLocal {
			return marketfetch.PlanExecutorLocal, nil
		}
		return marketfetch.PlanExecutorSCF, nil
	}
}

// activeSpaceIDs 返回 Storage 中处于启用状态的空间 ID（小写）；采集计划对账据此启停各空间的协程。
func activeSpaceIDs(ctx context.Context, storage *storageio.Client) (map[string]struct{}, error) {
	active := map[string]struct{}{}
	for page := uint32(1); ; page++ {
		rsp, err := storage.ListSpaces(ctx, &storagepb.ListSpacesReq{Status: "active", Page: &storagepb.Page{Page: page, Size: 200}})
		if err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list spaces: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, space := range rsp.GetSpaces() {
			active[strings.ToLower(space.GetSpaceId())] = struct{}{}
		}
		if !rsp.GetPageResult().GetHasMore() {
			return active, nil
		}
	}
}
