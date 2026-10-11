package bootstrap

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresult "github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"trpc.group/trpc-go/trpc-go/log"
)

// registerFetchPlanManager 为配置了 SCF 市场地域的空间启动采集计划对账：每个空间一个独立协程。
func registerFetchPlanManager(cfg *Config, dbm *store.Store, process *Runtime, results *collectorresult.Manager, metrics *marketfetch.Metrics) error {
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
	manager := &marketfetch.PlanManager{
		Spaces: func(context.Context) ([]string, error) { return spaces, nil },
		Factory: func(spaceID string) (*marketfetch.PlanReconciler, error) {
			options := marketfetch.PlanOptions{
				SpaceID: spaceID, Markets: cfg.SCFMarkets[strings.ToLower(spaceID)], SymbolsPerFunction: cfg.SCFPlan.SymbolsPerFunction,
				Fetch: cfg.SCFPlan.planFetch(), DefaultLatenessS: defaultLatenessSeconds(spaceID),
			}
			return &marketfetch.PlanReconciler{
				SpaceID: spaceID, Options: options, Deploy: deploy,
				Groups: &marketfetch.Reconciler{
					Tasks: dbm.Tasks(), Symbols: source, ResolveSourceID: marketwiring.DefaultSourceID, ResolveSymbol: marketwiring.ResolveSymbol,
					TaskFilter: func(params *domain.CollectParams) bool { return params.TimerManaged() },
				},
				Store: dbm.FetchPlans(), Storage: registrar, Cloud: invoker, Access: cfg.scfAccessEnvironment, Lateness: results,
				Activity: invoker, Metrics: metrics, Operations: operations,
			}, nil
		},
	}
	if !process.launch(func() { manager.Run(process.ctx) }) {
		return fmt.Errorf("collector runtime is closing")
	}
	log.Infof("collector fetch plan reconciliation enabled spaces=%s", strings.Join(spaces, ","))
	return nil
}
