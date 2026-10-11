package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
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
	deploy, err := cfg.fetchPlanDeploy()
	if err != nil {
		return err
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
	backfiller := &klinecheck.Backfiller{
		Checker: checker, Store: dbm.Backfill(), Plans: dbm.FetchPlans(), Invoker: invoker, BaseCtx: process.ctx,
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
		Spaces: func(context.Context) ([]string, error) { return spaces, nil },
		Factory: func(spaceID string) (*marketfetch.PlanReconciler, error) {
			options := marketfetch.PlanOptions{
				SpaceID: spaceID, Markets: cfg.SCFMarkets[strings.ToLower(spaceID)], SymbolsPerFunction: cfg.SCFPlan.SymbolsPerFunction,
				Fetch: cfg.SCFPlan.planFetch(), DefaultLatenessS: defaultLatenessSeconds(spaceID),
			}
			reconciler := &marketfetch.PlanReconciler{
				SpaceID: spaceID, Options: options, Deploy: deploy,
				Groups: &marketfetch.Reconciler{
					Tasks: dbm.Tasks(), Symbols: source, ResolveSourceID: marketwiring.DefaultSourceID, ResolveSymbol: marketwiring.ResolveSymbol,
					TaskFilter: func(params *domain.CollectParams) bool { return params.TimerManaged() },
				},
				Store: dbm.FetchPlans(), Storage: registrar, Cloud: invoker, Access: cfg.scfAccessEnvironment, Lateness: results,
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
