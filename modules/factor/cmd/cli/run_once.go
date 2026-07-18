package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/log"
)

func runOnce(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.SpaceID == "" || cfg.DatasetID == "" || cfg.SubjectID == "" || cfg.Freq == "" || cfg.BarTime.IsZero() {
		return fmt.Errorf("--space, --dataset, --subject, --freq and --bar-time are required")
	}
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}

	factorRepo := db.Factors()
	factors, err := factorRepo.ListEnabledTimeseries(ctx)
	if err != nil {
		return err
	}
	factors = filterFactors(factors, cfg.FactorIDs)
	if len(factors) == 0 {
		return fmt.Errorf("no enabled factors selected")
	}

	appCfg := bootstrap.Default()
	auth := serviceAuth()
	credentials, err := gatewayauth.ResolveCredentials(appCfg.Storage.KeyID, appCfg.Storage.HMACKeyFile)
	if err != nil {
		return err
	}
	metaTarget := gatewayauth.ServiceGatewayTarget(storageio.NormalizeStorageTarget(appCfg.Storage.GatewayTarget, "11003"))
	metaProxy := storagepb.NewMetadataClientProxy(gatewayauth.NewTRPCClientOptions(metaTarget, appCfg.Storage.GatewayNodeID, credentials)...)
	syncer := registry.NewMetadataSync(metadataAdapter{proxy: metaProxy}, auth)
	if err := syncer.SyncResultDataset(ctx, cfg.SpaceID, cfg.DatasetID, cfg.Freq, factors); err != nil {
		return err
	}

	task := buildTask(cfg, factors)
	storageClient := storageio.NewClientWithCredentials(appCfg.Storage.GatewayTarget, appCfg.Storage.GatewayNodeID, credentials, auth)
	frame, err := storageClient.ReadWindow(ctx, storageio.WindowKey{
		SpaceID:       cfg.SpaceID,
		SourceDataset: cfg.DatasetID,
		SubjectID:     cfg.SubjectID,
		Freq:          cfg.Freq,
	}, task.LookbackBars, cfg.BarTime, inputColumns(task.Factors))
	if err != nil {
		logRunOnce(ctx, task, domain.RunStatusFailed, err.Error(), 0)
		return err
	}

	exec, err := engine.NewStdioExecutor(engine.StdioConfig{
		PythonBin:     appCfg.Engine.PythonBin,
		WorkerPath:    filepath.Join("pyworker", "worker.py"),
		FactorsDir:    cfg.FactorsDir,
		SectionsDir:   appCfg.Engine.SectionsDir,
		Encoding:      "json",
		TaskTimeout:   time.Duration(appCfg.Engine.TaskTimeoutMS) * time.Millisecond,
		MaxFrameBytes: 64 << 20,
	})
	if err != nil {
		logRunOnce(ctx, task, domain.RunStatusFailed, err.Error(), 0)
		return err
	}
	defer exec.Close()

	result, err := exec.Execute(ctx, task, frame)
	if err != nil {
		logRunOnce(ctx, task, domain.RunStatusFailed, err.Error(), 0)
		return err
	}
	if err := storageClient.WriteFactorPatch(ctx, task, frame, result); err != nil {
		logRunOnce(ctx, task, domain.RunStatusFailed, err.Error(), result.ElapsedMS)
		return err
	}
	logRunOnce(ctx, task, domain.RunStatusSucceeded, "", result.ElapsedMS)
	return json.NewEncoder(out).Encode(runOncePayload(task, domain.RunStatusSucceeded, len(factors), result.ElapsedMS))
}

func buildTask(cfg cliConfig, factors []domain.FactorDef) *engine.FactorTask {
	specs := make([]engine.FactorSpec, 0, len(factors))
	lookback := 0
	for _, factor := range factors {
		params := mustParseParams(factor.ParamsJSON)
		specs = append(specs, engine.FactorSpec{
			FactorID:      factor.FactorID,
			Name:          factor.Name,
			SourceHash:    factor.SourceHash,
			SourcePath:    filepath.Join(cfg.FactorsDir, factor.Name+".py"),
			EstimatedMS:   int64(factor.AvgRuntimeMS),
			Params:        params,
			WritebackBars: factor.WritebackBars,
			ExtraColumns:  registry.ExtraColumnsFromFactors([]domain.FactorDef{factor}),
		})
		if factor.LookbackBars > lookback {
			lookback = factor.LookbackBars
		}
	}
	return &engine.FactorTask{
		TaskID:        fmt.Sprintf("manual-%d", time.Now().UnixNano()),
		Kind:          "timeseries",
		SpaceID:       cfg.SpaceID,
		SourceDataset: cfg.DatasetID,
		TargetDataset: registry.ResultDataset(cfg.DatasetID),
		SubjectID:     cfg.SubjectID,
		Freq:          cfg.Freq,
		BarTime:       cfg.BarTime,
		LookbackBars:  lookback,
		Factors:       specs,
	}
}

func inputColumns(specs []engine.FactorSpec) []string {
	set := map[string]struct{}{}
	out := append([]string(nil), storageio.KLineColumns...)
	for _, column := range out {
		set[column] = struct{}{}
	}
	for _, spec := range specs {
		for _, column := range spec.ExtraColumns {
			if _, ok := set[column]; ok {
				continue
			}
			set[column] = struct{}{}
			out = append(out, column)
		}
	}
	return out
}

func filterFactors(factors []domain.FactorDef, ids []string) []domain.FactorDef {
	if len(ids) == 0 {
		return factors
	}
	allowed := map[string]struct{}{}
	for _, id := range ids {
		allowed[id] = struct{}{}
	}
	out := make([]domain.FactorDef, 0, len(factors))
	for _, factor := range factors {
		if _, ok := allowed[factor.FactorID]; ok {
			out = append(out, factor)
		}
	}
	return out
}

func mustParseParams(raw string) []int {
	var params []int
	_ = json.Unmarshal([]byte(raw), &params)
	return params
}

func logRunOnce(ctx context.Context, task *engine.FactorTask, status string, errMsg string, elapsedMS int64) {
	log.InfoContextf(ctx, "factor_run_done task_id=%s trigger_type=manual space_id=%s source_dataset=%s target_dataset=%s subject_id=%s freq=%s bar_time=%s factor_count=%d status=%s elapsed_ms=%d error=%q",
		task.TaskID, task.SpaceID, task.SourceDataset, task.TargetDataset, task.SubjectID, task.Freq,
		task.BarTime.UTC().Format(time.RFC3339), len(task.Factors), status, elapsedMS, errMsg)
}

func runOncePayload(task *engine.FactorTask, status string, factorCount int, elapsedMS int64) map[string]any {
	return map[string]any{
		"ok":           status == domain.RunStatusSucceeded,
		"task_id":      task.TaskID,
		"run_id":       fmt.Sprintf("%s-%s", task.TaskID, status),
		"factor_count": factorCount,
		"elapsed_ms":   elapsedMS,
	}
}

func serviceAuth() *commonpb.AuthInfo {
	return &commonpb.AuthInfo{
		AppId:     "moox-factor",
		Operator:  "moox-factor",
		RequestId: fmt.Sprintf("factor-%d", time.Now().UnixNano()),
	}
}

type metadataAdapter struct {
	proxy storagepb.MetadataClientProxy
}

func (m metadataAdapter) CreateFactor(ctx context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	return m.proxy.CreateFactor(ctx, req)
}

func (m metadataAdapter) CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	return m.proxy.CreateDataset(ctx, req)
}

func (m metadataAdapter) UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	return m.proxy.UpdateDataset(ctx, req)
}

func (m metadataAdapter) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return m.proxy.UpsertDatasetColumn(ctx, req)
}

func (m metadataAdapter) GetFactor(ctx context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error) {
	return m.proxy.GetFactor(ctx, req)
}

func (m metadataAdapter) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return m.proxy.GetDataset(ctx, req)
}

func (m metadataAdapter) ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error) {
	return m.proxy.ListDatasetColumns(ctx, req)
}

func (m metadataAdapter) ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	return m.proxy.ListDatasetSubjects(ctx, req)
}

func (m metadataAdapter) BindDatasetSubject(ctx context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error) {
	return m.proxy.BindDatasetSubject(ctx, req)
}

func (m metadataAdapter) ListPrimaryStoreRoutes(ctx context.Context, req *storagepb.ListPrimaryStoreRoutesReq) (*storagepb.ListPrimaryStoreRoutesRsp, error) {
	return m.proxy.ListPrimaryStoreRoutes(ctx, req)
}

func (m metadataAdapter) CreatePrimaryStoreRoute(ctx context.Context, req *storagepb.CreatePrimaryStoreRouteReq) (*storagepb.CreatePrimaryStoreRouteRsp, error) {
	return m.proxy.CreatePrimaryStoreRoute(ctx, req)
}
