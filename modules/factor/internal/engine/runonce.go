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
	SetID    string
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
	set, err := cache.Set(strings.TrimSpace(req.SetID))
	if err != nil {
		return pipeline.Outcome{}, err
	}
	members, err := selectSetMembers(set, req.Members)
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
	// 诊断入口跑在独立进程里，不能打开引擎正在使用的本地存储：直接读 Storage 的窗口计算；
	// 它同样写因子结果，跳过的项记入账本，由引擎的跳过恢复补算。
	ledger, _, ledgerErr := gapledger.Open(cfg.Gap.LedgerDir, time.Now, 0)
	if ledgerErr != nil {
		return pipeline.Outcome{}, fmt.Errorf("open factor gap ledger: %w", ledgerErr)
	}
	defer ledger.Close()
	pipelineConfig.GapSink = ledger
	runner := pipeline.NewRunner(storage, pipeline.NewStorageData(storage), clock, pipelineConfig)
	// 带可调参数的成员必须按这一期的冻结记录计算。诊断进程不持有引擎租约，读取冻结记录会被管理端拒绝；
	// 此时用默认参数写正式结果会覆盖按冻结参数算出的值，所以直接拒绝，请改用补算（持有租约的引擎按冻结记录执行）。
	var intervals map[string][]domain.ParamInterval
	for _, member := range members {
		if member.FixedParams() || len(domain.DefaultSnapshot(member.Factor)) == 0 {
			continue
		}
		if managerErr != nil {
			return pipeline.Outcome{}, fmt.Errorf("成员 %s 带可调参数，需要读取冻结参数，但无法连接因子管理端：%w", member.Alias, managerErr)
		}
		if intervals, err = manager.ListParamIntervals(ctx, set.Set.SetID, start, start); err != nil {
			return pipeline.Outcome{}, fmt.Errorf("成员 %s 带可调参数，读取冻结参数失败，拒绝按默认参数写入正式结果（请改用补算）：%w", member.Alias, err)
		}
		break
	}
	return recalcexec.NewExecutor(runner, recalcexec.WithClock(clock)).RunChunk(ctx, recalcexec.Selection{
		Set: set.Set, Members: members, Subjects: subjects, Columns: columns, Intervals: intervals,
	}, start, start.Add(duration))
}

// Set returns one set of the snapshot.
func (c *CatalogCache) Set(setID string) (domain.EngineSet, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return domain.EngineSet{}, errors.New("factor catalog is not available: the manager is unreachable and no snapshot was saved")
	}
	for _, set := range c.sets {
		if set.Set.SetID == setID {
			return set, nil
		}
	}
	return domain.EngineSet{}, fmt.Errorf("factor set %q is not enabled in the catalog", setID)
}

func selectSetMembers(set domain.EngineSet, aliases []string) ([]domain.MemberDef, error) {
	members := make([]domain.SetMember, 0, len(set.Members))
	for _, member := range set.Members {
		members = append(members, domain.SetMember{
			FactorSetMember: domain.FactorSetMember{SetID: set.Set.SetID, MemberAlias: member.Alias, FactorID: member.Factor.FactorID,
				Status: domain.MemberStatusEnabled, Params: member.Params},
			Factor: member.Factor,
		})
	}
	return domain.SelectEnabledMembers(members, aliases)
}
