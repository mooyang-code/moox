package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
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

// registerFetchPlanManager 为配置了 SCF 市场地域的空间启动采集计划对账：每个空间一个独立协程。
func registerFetchPlanManager(cfg *Config, dbm *store.Store, process *Runtime, results *collectorresult.Manager, metrics *marketfetch.Metrics, svc *collectsvc.Service) error {
	spaces := cfg.fetchPlanSpaces(marketFetchSpaceIDs())
	if len(spaces) == 0 {
		return nil
	}
	// 启动时先逐个空间检查部署参数，配置错误尽早暴露。
	for _, space := range spaces {
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
	checker := &klinecheck.Checker{Plans: dbm.FetchPlans(), Rows: storageio.NewGatewayClient(process.gateway), Auth: auth}
	checkCalendar := newStockCNCheckCalendar()
	checker.Calendar, checker.CalendarReady = checkCalendar.valid, checkCalendar.ready
	backfiller := &klinecheck.Backfiller{
		Checker: checker, Store: dbm.Backfill(), Plans: dbm.FetchPlans(), Invoker: invoker, BaseCtx: process.ctx, Refill: true,
		Functions: func(spaceID string) klinecheck.FunctionProvider {
			if value, ok := reconcilers.Load(spaceID); ok {
				return value.(*marketfetch.PlanReconciler)
			}
			return nil
		},
	}
	process.beforeClose = append(process.beforeClose, func() error { backfiller.Wait(); return nil })
	svc.SetKlineOps(backfiller)
	manager := &marketfetch.PlanManager{
		Configured: spaces,
		Spaces: func(ctx context.Context) ([]string, error) {
			// 配置里的空间 ∩ Admin 中仍启用的空间；空间被停用或删除后，管理器先停定时器再退出该空间的协程。
			active, err := invoker.ListActiveSpaceIDs(ctx)
			if err != nil {
				return nil, err
			}
			running := make([]string, 0, len(spaces))
			for _, space := range spaces {
				if _, ok := active[strings.ToLower(space)]; ok {
					running = append(running, space)
				}
			}
			return running, nil
		},
		Factory: func(spaceID string) (*marketfetch.PlanReconciler, error) {
			options := marketfetch.PlanOptions{
				SpaceID: spaceID, Markets: cfg.SCFMarkets[strings.ToLower(spaceID)], SymbolsPerFunction: cfg.SCFPlan.symbolsPerFunction(spaceID),
				Fetch: cfg.SCFPlan.fetchFor(spaceID), DefaultLatenessS: defaultLatenessSeconds(spaceID),
			}
			var preflight func(context.Context) error
			if strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
				preflight = stockCNCalendarPreflight
			}
			deploy, err := cfg.fetchPlanDeploy(spaceID)
			if err != nil {
				return nil, err
			}
			reconciler := &marketfetch.PlanReconciler{
				Preflight: preflight,
				SpaceID:   spaceID, Options: options, Deploy: deploy,
				Groups: &marketfetch.Reconciler{
					Tasks: dbm.Tasks(), Symbols: source, ResolveSourceID: marketwiring.DefaultSourceID, ResolveSymbol: marketwiring.ResolveSymbol,
					TaskFilter: func(params *domain.CollectParams) bool { return params.TimerManaged() },
				},
				Store: dbm.FetchPlans(), Storage: registrar, Cloud: invoker, Access: cfg.scfPlanAccessEnvironment, Lateness: results,
				Activity: invoker, Metrics: metrics, Operations: operations,
			}
			reconcilers.Store(spaceID, reconciler)
			return reconciler, nil
		},
		AfterTick: func(ctx context.Context, spaceID string) error {
			if err := backfiller.Tick(ctx, spaceID); err != nil {
				return err
			}
			return backfiller.AutoScan(ctx, spaceID)
		},
	}
	if !process.launch(func() { manager.Run(process.ctx) }) {
		return fmt.Errorf("collector runtime is closing")
	}
	log.Infof("collector fetch plan reconciliation enabled spaces=%s", strings.Join(spaces, ","))
	return nil
}

// stockCNCalendarPreflight 检查公共交易日历的覆盖期：不足 60 天告警，不足 14 天或已过期则拒绝创建新计划
// （函数在日历失效时无法判断交易周期）。
func stockCNCalendarPreflight(ctx context.Context) error {
	calendar, err := stockmarket.LoadCalendar(stockCNCalendarPath())
	if err != nil {
		return fmt.Errorf("load stockcn calendar: %w", err)
	}
	now := time.Now()
	if err := calendar.CheckHorizon(now, 14); err != nil {
		return fmt.Errorf("stockcn calendar coverage is too short: %w", err)
	}
	if err := calendar.CheckHorizon(now, 60); err != nil {
		log.WarnContextf(ctx, "stockcn calendar coverage is running out: %v", err)
	}
	return nil
}

// stockCNCheckCalendarFilter 是完整性检查使用的交易日历过滤：与函数侧一致，stockcn 只在交易日历的
// 交易分钟应有 K 线，其余空间连续交易。日历加载失败不缓存，修复文件后下次检查会重新加载；
// 不可用时 ready 返回错误，检查直接失败而不是得到空结果。
type stockCNCheckCalendarFilter struct {
	mu       sync.Mutex
	calendar *stockmarket.Calendar
	loadedAt time.Time
	sessions map[string]map[int64]struct{}
	now      func() time.Time
}

// checkCalendarReloadInterval 是交易日历的重新加载间隔：日历文件更新后无需重启即可生效。
const checkCalendarReloadInterval = time.Hour

func newStockCNCheckCalendar() *stockCNCheckCalendarFilter {
	return &stockCNCheckCalendarFilter{sessions: map[string]map[int64]struct{}{}, now: time.Now}
}

func (f *stockCNCheckCalendarFilter) ready(spaceID string) error {
	if !strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calendar != nil && f.now().Sub(f.loadedAt) < checkCalendarReloadInterval {
		return nil
	}
	calendar, err := stockmarket.LoadCalendar(stockCNCalendarPath())
	if err == nil && (calendar == nil || calendar.Location() == nil) {
		err = fmt.Errorf("calendar has no time zone")
	}
	if err != nil {
		if f.calendar != nil {
			// 重新加载失败时继续使用已加载的日历，下次检查再试。
			f.loadedAt = f.now().Add(-checkCalendarReloadInterval + time.Minute)
			return nil
		}
		return err
	}
	f.calendar, f.loadedAt = calendar, f.now()
	f.sessions = map[string]map[int64]struct{}{}
	return nil
}

func (f *stockCNCheckCalendarFilter) valid(spaceID, frequency string, barStart time.Time) (bool, error) {
	if !strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
		return true, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calendar == nil {
		return false, fmt.Errorf("stockcn calendar is not loaded")
	}
	if !strings.EqualFold(frequency, "1m") {
		return false, fmt.Errorf("stockcn calendar only supports 1m, got %s", frequency)
	}
	if !f.calendar.Covers(barStart) {
		return false, fmt.Errorf("%s is outside the calendar coverage", barStart.In(f.calendar.Location()).Format("2006-01-02"))
	}
	tradeDate := barStart.In(f.calendar.Location()).Format("2006-01-02")
	bars, cached := f.sessions[tradeDate]
	if !cached {
		expected, err := f.calendar.ExpectedMinuteBars(tradeDate)
		if err != nil {
			return false, err
		}
		bars = make(map[int64]struct{}, len(expected))
		for _, bar := range expected {
			bars[bar.UTC().Unix()] = struct{}{}
		}
		f.sessions[tradeDate] = bars
	}
	_, ok := bars[barStart.UTC().Unix()]
	return ok, nil
}

func stockCNCalendarPath() string {
	if path := strings.TrimSpace(os.Getenv("MOOX_STOCK_CN_CALENDAR_PATH")); path != "" {
		return path
	}
	return "./config/markets/stockcn/calendar.yaml"
}
