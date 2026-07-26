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
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
)

func runOnce(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.SpaceID == "" || cfg.DatasetID == "" || cfg.SubjectID == "" || cfg.Freq == "" ||
		cfg.StartTime.IsZero() || cfg.EndTime.IsZero() || !cfg.StartTime.Before(cfg.EndTime) {
		return fmt.Errorf("--space, --dataset, --subject, --freq, --start-time and --end-time are required")
	}
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	factors, err := db.Factors().ListEnabled(ctx)
	if err != nil {
		return err
	}
	factors = filterFactors(factors, cfg.FactorIDs)
	if len(factors) == 0 {
		return fmt.Errorf("no enabled factors selected")
	}
	for i := range factors {
		if override := cfg.FactorSourcePaths[factors[i].FactorID]; override != "" {
			factors[i].SourcePath = override
		}
	}

	appCfg := bootstrap.Default()
	auth := serviceAuth()
	credentials, err := gatewayauth.ResolveCredentials(appCfg.Storage.KeyID, appCfg.Storage.HMACKeyFile)
	if err != nil {
		return err
	}
	targetDataset := cfg.TargetDataset
	if targetDataset == "" {
		targetDataset = registry.ResultDataset(cfg.DatasetID)
	}
	taskID := cfg.TaskID
	if taskID == "" {
		taskID = fmt.Sprintf("manual-%d", time.Now().UnixNano())
	}
	task, err := scheduler.BuildTask(scheduler.TaskScope{
		TaskID: taskID, TriggerType: "manual", SpaceID: cfg.SpaceID,
		SourceDataset: cfg.DatasetID, TargetDataset: targetDataset,
		SubjectID: cfg.SubjectID, Freq: cfg.Freq,
		StartTime: cfg.StartTime, EndTime: cfg.EndTime,
	}, factors, cfg.FactorsDir)
	if err != nil {
		return err
	}

	storageClient := storageio.NewClientWithCredentials(
		appCfg.Storage.GatewayTarget, appCfg.Storage.GatewayNodeID, credentials, auth,
	)
	pythonExec, err := engine.NewPythonExecutor(ctx, 1, process.Config{
		PythonBin:   appCfg.Engine.PythonBin,
		WorkerPath:  filepath.Join("pyworker", "worker.py"),
		Args:        []string{"--factors-dir", cfg.FactorsDir},
		TaskTimeout: time.Duration(appCfg.Engine.TaskTimeoutMS) * time.Millisecond,
		Limits:      process.DefaultLimits(),
	})
	if err != nil {
		return err
	}
	defer pythonExec.Close()
	runner := scheduler.NewService(scheduler.Config{Workers: 1, QueueCapacity: 1, MaxRetry: appCfg.Scheduler.MaxRetry}, storageClient, pythonExec)
	started := time.Now()
	if err := runner.Run(ctx, task); err != nil {
		return err
	}
	elapsed := time.Since(started).Milliseconds()
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "task_id": task.TaskID, "status": "succeeded",
		"factor_count": len(factors),
		"start_time":   cfg.StartTime.UTC().Format(time.RFC3339Nano),
		"end_time":     cfg.EndTime.UTC().Format(time.RFC3339Nano),
		"elapsed_ms":   elapsed,
	})
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

func serviceAuth() *commonpb.AuthInfo {
	return &commonpb.AuthInfo{
		AppId: "moox-factor", Operator: "moox-factor",
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
func (m metadataAdapter) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return m.proxy.CheckDatasetActivation(ctx, req)
}
func (m metadataAdapter) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return m.proxy.ActivateDataset(ctx, req)
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
