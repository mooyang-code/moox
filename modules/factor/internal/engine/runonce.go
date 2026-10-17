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
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
)

// RunOnceRequest computes one period of one set for diagnosis.
type RunOnceRequest struct {
	SetID     string
	FactorIDs []string
	Subjects  []string
	Period    time.Time
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
	cache, err := NewCatalogCache(cfg.Python.FactorsDir, cfg.CatalogSync.StateFile)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	if _, err := cache.Load(); err != nil {
		return pipeline.Outcome{}, err
	}
	if manager, err := NewManagerClient(gateway, cfg.Manager.Timeout, domain.EngineIdentity{EngineID: cfg.Engine.ID, BootID: "run-once"}); err == nil {
		_ = (&catalogSyncer{client: manager, cache: cache}).SyncOnce(ctx)
	}
	set, err := cache.Set(strings.TrimSpace(req.SetID))
	if err != nil {
		return pipeline.Outcome{}, err
	}
	factors, err := selectSetFactors(set, req.FactorIDs)
	if err != nil {
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
	pool, err := pyexec.New(ctx, cfg.Python.Workers, cfg.Python.processConfig())
	if err != nil {
		return pipeline.Outcome{}, err
	}
	defer pool.Close()
	clock := periodclock.Continuous{}
	start, err := clock.Align(req.Period, set.Set.Freq)
	if err != nil {
		return pipeline.Outcome{}, fmt.Errorf("align period: %w", err)
	}
	duration, err := clock.Duration(set.Set.Freq)
	if err != nil {
		return pipeline.Outcome{}, err
	}
	pipelineConfig := pipeline.Config{
		ReadBatchSubjects: cfg.Pipeline.ReadBatchSubjects, ReadWorkers: cfg.Pipeline.ReadWorkers,
		ReadTimeout:     cfg.Pipeline.ReadTimeout,
		BulkReadWorkers: cfg.Pipeline.BulkReadWorkers, ReadRetryBackoff: cfg.Pipeline.ReadRetryBackoff, ReadBreakerFailures: cfg.Pipeline.ReadBreakerFailures,
		WarmupBatchSubjects: cfg.Pipeline.WarmupBatchSubjects, WarmupWorkers: cfg.Pipeline.WarmupWorkers, WarmupBatchPause: cfg.Pipeline.WarmupBatchPause, WriteBatchRows: cfg.Pipeline.WriteBatchRows,
		PythonWorkers: cfg.Python.Workers, FactorsDir: cfg.Python.FactorsDir,
	}
	// 与引擎同样执行空洞检查并记账本：这个入口同样会写因子结果，不能绕过空洞防护。
	if cfg.Gap.Enabled {
		ledger, _, ledgerErr := gapledger.Open(cfg.Gap.LedgerDir, time.Now, 0)
		if ledgerErr != nil {
			return pipeline.Outcome{}, fmt.Errorf("open factor gap ledger: %w", ledgerErr)
		}
		defer ledger.Close()
		pipelineConfig.GapCheck, pipelineConfig.MassRatio, pipelineConfig.GapSink = true, cfg.Gap.MassRatio, ledger
	}
	runner := pipeline.NewRunner(storage, pool, clock, pipelineConfig)
	return recalcexec.NewExecutor(runner, recalcexec.WithClock(clock)).RunChunk(ctx, recalcexec.Selection{
		Set: set.Set, Factors: factors, Subjects: subjects, Columns: columns,
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

func selectSetFactors(set domain.EngineSet, factorIDs []string) ([]domain.FactorDef, error) {
	members := make([]domain.SetMember, 0, len(set.Factors))
	for _, factor := range set.Factors {
		members = append(members, domain.SetMember{
			FactorSetMember: domain.FactorSetMember{SetID: set.Set.SetID, FactorID: factor.FactorID, Status: domain.MemberStatusEnabled},
			Factor:          factor,
		})
	}
	return domain.SelectEnabledFactors(members, factorIDs)
}
