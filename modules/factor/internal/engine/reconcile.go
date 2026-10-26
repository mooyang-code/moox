package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
)

// ReconcileRequest 是对账命令的输入。
type ReconcileRequest struct {
	// TaskID 是要对账的因子任务（对账它的源数据）。
	TaskID string
	// Subjects 为空表示本地有数据的全部标的。
	Subjects []string
	// Progress 汇报进度（可为 nil）。
	Progress func(done, total int)
}

// ReconcileSummary 是对账的结果。
type ReconcileSummary struct {
	TaskID   string
	Source   string
	Subjects int
	// Replaced 是内容与 Storage 不同、被整段替换的标的数。
	Replaced int
	// Followed 是对账期间 Storage 又发生的变更里应用到本地的标的数。
	Followed int
}

// Reconcile 重建一个任务源数据的同步基线（设计 5.2「对账命令」）：本地数据与 Storage 保留期内的数据比对并整段替换，
// 完成后发布新的变更日志位点。本地时序存储同一时刻只能由一个进程打开，所以必须在引擎停止后执行。
func Reconcile(ctx context.Context, cfg *Config, req ReconcileRequest) (ReconcileSummary, error) {
	if cfg == nil {
		return ReconcileSummary{}, errors.New("factor engine config is required")
	}
	gateway, err := cfg.GatewayClient.OpenExternal(cfg.sourcePath)
	if err != nil {
		return ReconcileSummary{}, err
	}
	defer gateway.Close()
	storage, err := newStorageClient(gateway, cfg.Storage)
	if err != nil {
		return ReconcileSummary{}, err
	}
	cache, err := NewCatalogCache(cfg.CatalogSync.StateFile)
	if err != nil {
		return ReconcileSummary{}, err
	}
	if _, err := cache.Load(); err != nil {
		return ReconcileSummary{}, err
	}
	set, err := cache.Set(strings.TrimSpace(req.TaskID))
	if err != nil {
		return ReconcileSummary{}, err
	}
	store, err := seriesstore.Open(seriesstore.Options{Dir: cfg.SeriesStore.Dir, BlockCacheBytes: int64(cfg.SeriesStore.BlockCache)})
	if err != nil {
		return ReconcileSummary{}, fmt.Errorf("打开本地时序存储（引擎必须已停止）：%w", err)
	}
	defer store.Close()
	data := localdata.New(localdata.Config{Store: store, Storage: storage, Clock: periodclock.Continuous{}, Workers: cfg.Sync.Workers, BatchSubjects: cfg.Sync.BatchSubjects})
	res, err := data.ReconcileTask(ctx, set, req.Subjects, req.Progress)
	if err != nil {
		return ReconcileSummary{}, err
	}
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}
	return ReconcileSummary{TaskID: set.Set.TaskID, Source: src.String(), Subjects: res.Subjects, Replaced: len(res.Replaced), Followed: len(res.Followed)}, nil
}

// BackfillRequest 是历史回补命令的输入。
type BackfillRequest struct {
	TaskID string
	// Bars 是每个标的本地连续历史的目标根数（以本地最新一根往前数）。
	Bars     int
	Subjects []string
	Progress func(done, total int)
}

// BackfillSummary 是历史回补的结果。
type BackfillSummary struct {
	TaskID   string
	Source   string
	Subjects int
	// Extended 是补上了更早历史的标的数。
	Extended int
}

// Backfill 把任务源数据的本地历史从 Storage 回补到 Bars 根（Storage 保留期内有多少补多少）。本地时序存储同一时刻只能由一个进程打开，
// 所以必须在引擎停止后执行；Earliest 前移会登记失效重算，引擎启动后据此重算受影响的周期。
func Backfill(ctx context.Context, cfg *Config, req BackfillRequest) (BackfillSummary, error) {
	if cfg == nil {
		return BackfillSummary{}, errors.New("factor engine config is required")
	}
	gateway, err := cfg.GatewayClient.OpenExternal(cfg.sourcePath)
	if err != nil {
		return BackfillSummary{}, err
	}
	defer gateway.Close()
	storage, err := newStorageClient(gateway, cfg.Storage)
	if err != nil {
		return BackfillSummary{}, err
	}
	cache, err := NewCatalogCache(cfg.CatalogSync.StateFile)
	if err != nil {
		return BackfillSummary{}, err
	}
	if _, err := cache.Load(); err != nil {
		return BackfillSummary{}, err
	}
	set, err := cache.Set(strings.TrimSpace(req.TaskID))
	if err != nil {
		return BackfillSummary{}, err
	}
	store, err := seriesstore.Open(seriesstore.Options{Dir: cfg.SeriesStore.Dir, BlockCacheBytes: int64(cfg.SeriesStore.BlockCache)})
	if err != nil {
		return BackfillSummary{}, fmt.Errorf("打开本地时序存储（引擎必须已停止）：%w", err)
	}
	defer store.Close()
	data := localdata.New(localdata.Config{Store: store, Storage: storage, Clock: periodclock.Continuous{}, Workers: cfg.Sync.Workers, BatchSubjects: cfg.Sync.BatchSubjects})
	extended, err := data.BackfillTask(ctx, set, req.Subjects, req.Bars, req.Progress)
	if err != nil {
		return BackfillSummary{}, err
	}
	src := seriesstore.Source{SpaceID: set.Set.SpaceID, DatasetID: set.Set.SourceDatasetID, Freq: set.Set.Freq}
	subjects := len(req.Subjects)
	if subjects == 0 {
		if all, err := store.Subjects(src); err == nil {
			subjects = len(all)
		}
	}
	return BackfillSummary{TaskID: set.Set.TaskID, Source: src.String(), Subjects: subjects, Extended: extended}, nil
}
