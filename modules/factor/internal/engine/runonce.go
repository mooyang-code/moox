package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
)

// RunOnceRequest computes one period of one set for diagnosis.
type RunOnceRequest struct {
	TaskID   string
	Members  []string
	Subjects []string
	Period   time.Time
}

// RunOnce refreshes the catalog when the manager is reachable (falling back to
// the saved snapshot), then computes and writes a single period like a
// recalc chunk.
func RunOnce(ctx context.Context, cfg *Config, req RunOnceRequest) (pipeline.Outcome, error) {
	if cfg == nil {
		return pipeline.Outcome{}, errors.New("factor engine config is required")
	}
	gateway, err := cfg.GatewayClient.OpenExternal(cfg.sourcePath)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer gateway.Close()
	storage, err := newStorageClient(gateway, cfg.Storage)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	cache, err := NewCatalogCache(cfg.CatalogSync.StateFile)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	if _, err := cache.Load(); err != nil {
		return pipeline.Outcome{}, err
	}
	manager, managerErr := NewManagerClient(gateway, cfg.Manager.Timeout, domain.EngineIdentity{EngineID: cfg.Engine.ID, BootID: "run-once"})
	if managerErr == nil {
		_ = (&catalogSyncer{client: manager, cache: cache}).SyncOnce(ctx)
	}
	set, err := cache.Set(strings.TrimSpace(req.TaskID))
	if err != nil {
		return pipeline.Outcome{}, err
	}
	members, err := selectTaskMembers(set, req.Members)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	if err := checkSubjectsInDataset(ctx, storage, set, req.Subjects); err != nil {
		return pipeline.Outcome{}, err
	}
	subjects, err := setSubjects(ctx, storage, set, req.Subjects)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	columns, err := storage.DatasetColumns(ctx, set.Set.SpaceID, set.Set.SourceDatasetID)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	clock := periodclock.Continuous{}
	start, err := clock.Align(req.Period, set.Set.Freq)
	if err != nil {
		return pipeline.Outcome{}, fmt.Errorf("align period: %w", err)
	}
	duration, err := clock.Duration(set.Set.Freq)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	pipelineConfig := pipeline.Config{WriteBatchRows: cfg.Pipeline.WriteBatchRows, ComputeWorkers: cfg.Compute.Workers, MaxMemory: int64(cfg.Compute.MaxMemory)}
	// 任务引用自定义算子时，先备齐模块（本地命中或向管理端拉取），再用同一套宿主计算。
	if refs := refsOfTasks([]domain.EngineTask{set}); len(refs) > 0 {
		if managerErr != nil {
			return pipeline.Outcome{}, fmt.Errorf("任务引用了自定义算子，但管理端客户端不可用：%w", managerErr)
		}
		modules, err := newUDFModules(ctx, cfg.UDF, cfg.Compute.Workers, nil, manager.GetUdfModule)
		if err != nil {
			return pipeline.Outcome{}, err
		}
		defer modules.Close(context.Background())
		if err := modules.EnsureAll(ctx, refs); err != nil {
			return pipeline.Outcome{}, err
		}
		pipelineConfig.UDF = modules.Host()
	}
	// 诊断入口跑在独立进程里，不能打开引擎正在使用的本地存储：直接读 Storage 的窗口计算；
	// 它同样写因子结果，跳过的项记入账本，由引擎的跳过恢复补算。
	// 它不经过引擎的任务执行器，没有同任务串行保证（读原行 __params 再合并写回）：使用前先暂停该任务。
	ledger, _, ledgerErr := gapledger.Open(cfg.Gap.LedgerDir, time.Now, 0)
	if ledgerErr != nil {
		return pipeline.Outcome{}, fmt.Errorf("open factor gap ledger: %w", ledgerErr)
	}
	defer ledger.Close()
	pipelineConfig.GapSink = ledger
	runner := pipeline.NewRunner(storage, pipeline.NewStorageData(storage), clock, pipelineConfig)
	// 可变成员按这一期已有行的 __params 计算（诊断进程直接读 Storage，不需要引擎租约）；已有行里没有这个成员时
	// 用成员自己的参数，与补算一致。
	return recalcexec.NewExecutor(runner, recalcexec.WithClock(clock), recalcexec.WithParamReader(recalcexec.NewStorageParamReader(storage))).RunChunk(ctx, recalcexec.Selection{
		Set: set.Set, Members: members, Subjects: subjects, Columns: columns,
	}, start, start.Add(duration))
}

// Set returns one set of the snapshot.
func (c *CatalogCache) Set(taskID string) (domain.EngineTask, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return domain.EngineTask{}, errors.New("factor catalog is not available: the manager is unreachable and no snapshot was saved")
	}
	for _, set := range c.sets {
		if set.Set.TaskID == taskID {
			return set, nil
		}
	}
	return domain.EngineTask{}, fmt.Errorf("factor task %q is not enabled in the catalog", taskID)
}

func selectTaskMembers(set domain.EngineTask, aliases []string) ([]domain.MemberDef, error) {
	members := make([]domain.TaskMember, 0, len(set.Members))
	for _, member := range set.Members {
		members = append(members, domain.TaskMember{
			FactorTaskMember: domain.FactorTaskMember{TaskID: set.Set.TaskID, MemberKey: member.Key, FactorID: member.Factor.FactorID,
				Status: domain.MemberStatusEnabled, Params: member.Params},
			Factor: member.Factor,
		})
	}
	return domain.SelectEnabledMembers(members, aliases)
}
