package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/bootstrap"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

type runOnceRuntime struct {
	DBPath          string
	FactorsDir      string
	PythonBin       string
	WorkerPath      string
	GatewayTarget   string
	GatewayNodeID   string
	Credentials     gatewayauth.Credentials
	PythonWorkers   int
	ViewReadWorkers int
	ViewReadTimeout time.Duration
	TaskTimeout     time.Duration
}

func resolveRunOnceRuntime(cli cliConfig) (runOnceRuntime, error) {
	appCfg, err := bootstrap.Load(cli.ConfigPath)
	if err != nil {
		return runOnceRuntime{}, err
	}
	if cli.DBPath != "" {
		appCfg.Database.Path = cli.DBPath
	}
	if cli.FactorsDir != "" {
		appCfg.Engine.FactorsDir = cli.FactorsDir
	}
	configPath, err := filepath.Abs(cli.ConfigPath)
	if err != nil {
		return runOnceRuntime{}, fmt.Errorf("resolve config path: %w", err)
	}
	runtimeRoot := filepath.Dir(filepath.Dir(configPath))
	resolveRuntimePath := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Clean(filepath.Join(runtimeRoot, path))
	}
	credentials, err := gatewayauth.ResolveCredentials(appCfg.Storage.KeyID, appCfg.Storage.HMACKeyFile)
	if err != nil {
		return runOnceRuntime{}, err
	}
	return runOnceRuntime{
		DBPath:          resolveRuntimePath(appCfg.Database.Path),
		FactorsDir:      resolveRuntimePath(appCfg.Engine.FactorsDir),
		PythonBin:       appCfg.Engine.PythonBin,
		WorkerPath:      resolveRuntimePath(appCfg.Engine.WorkerPath),
		GatewayTarget:   appCfg.Storage.GatewayTarget,
		GatewayNodeID:   appCfg.Storage.GatewayNodeID,
		Credentials:     credentials,
		PythonWorkers:   1,
		ViewReadWorkers: 1,
		ViewReadTimeout: time.Duration(appCfg.Engine.ViewReadTimeoutMS) * time.Millisecond,
		TaskTimeout:     time.Duration(appCfg.Engine.TaskTimeoutMS) * time.Millisecond,
	}, nil
}

func runOnce(ctx context.Context, cfg cliConfig, out io.Writer) error {
	if cfg.SpaceID == "" || cfg.DatasetID == "" || cfg.SubjectID == "" || cfg.Freq == "" ||
		cfg.StartTime.IsZero() || cfg.EndTime.IsZero() || !cfg.StartTime.Before(cfg.EndTime) {
		return fmt.Errorf("--space, --dataset, --subject, --freq, --start-time and --end-time are required")
	}
	runtimeCfg, err := resolveRunOnceRuntime(cfg)
	if err != nil {
		return err
	}
	db, err := store.Open(&store.Options{Path: runtimeCfg.DBPath})
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
	for i := range factors {
		if override := cfg.FactorSourcePaths[factors[i].FactorID]; override != "" {
			factors[i].SourcePath = override
		}
	}
	bindings, err := db.Bindings().ListExecutable(ctx)
	if err != nil {
		return err
	}
	groups := executableFactorGroups(factors, bindings, cfg)
	if len(groups) == 0 {
		return fmt.Errorf("no executable factors selected for the requested scope")
	}

	auth := serviceAuth()
	taskID := cfg.TaskID
	if taskID == "" {
		taskID = fmt.Sprintf("manual-%d", time.Now().UnixNano())
	}
	storageClient := storageio.NewClientWithCredentials(
		runtimeCfg.GatewayTarget, runtimeCfg.GatewayNodeID, runtimeCfg.Credentials, auth,
	).WithViewAuth(serviceViewAuth()).WithOutputManifests(db.OutputManifests())
	pythonExec, err := engine.NewPythonWorkerPool(ctx, runtimeCfg.PythonWorkers, process.Config{
		PythonBin:   runtimeCfg.PythonBin,
		WorkerPath:  runtimeCfg.WorkerPath,
		Args:        []string{"--factors-dir", runtimeCfg.FactorsDir},
		TaskTimeout: runtimeCfg.TaskTimeout,
		Limits:      process.DefaultLimits(),
	})
	if err != nil {
		return err
	}
	defer pythonExec.Close()
	runner := taskrunner.NewService(runtimeCfg.PythonWorkers, storageClient, pythonExec,
		taskrunner.WithViewReadConfig(runtimeCfg.ViewReadWorkers, runtimeCfg.ViewReadTimeout))
	started := time.Now()
	targets := make([]string, 0, len(groups))
	for target := range groups {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	totalFactors := 0
	for _, factors := range groups {
		totalFactors += len(factors)
	}
	factorCount := 0
	taskIndex := 0
	for _, target := range targets {
		for _, factor := range groups[target] {
			taskIndex++
			currentTaskID := taskID
			if totalFactors > 1 {
				currentTaskID = fmt.Sprintf("%s-%d", taskID, taskIndex)
			}
			sourceViewID := cfg.ViewID
			if sourceViewID == "" {
				sourceViewID = cfg.DatasetID
			}
			bindingID := executableBindingID(bindings, factor.FactorID, target, cfg)
			if bindingID == "" {
				bindingID = fmt.Sprintf("manual:%s:%s:%s:%s", factor.FactorID, cfg.SpaceID, sourceViewID, cfg.Freq)
			}
			task, buildErr := taskrunner.BuildTask(taskrunner.TaskScope{
				TaskID: currentTaskID, BindingID: bindingID,
				TriggerType: "manual", SpaceID: cfg.SpaceID,
				SourceViewID:  cfg.ViewID,
				SourceDataset: cfg.DatasetID, TargetDataset: target,
				SubjectID: cfg.SubjectID, Freq: cfg.Freq,
				TriggerEventID: taskID, TriggeredAt: time.Now().UTC(),
				StartTime: cfg.StartTime, EndTime: cfg.EndTime,
			}, factor, runtimeCfg.FactorsDir)
			if buildErr != nil {
				return buildErr
			}
			if runErr := runner.Run(ctx, task); runErr != nil {
				return runErr
			}
			factorCount++
		}
	}
	elapsed := time.Since(started).Milliseconds()
	return json.NewEncoder(out).Encode(map[string]any{
		"ok": true, "task_id": taskID, "status": "succeeded",
		"factor_count": factorCount,
		"start_time":   cfg.StartTime.UTC().Format(time.RFC3339Nano),
		"end_time":     cfg.EndTime.UTC().Format(time.RFC3339Nano),
		"elapsed_ms":   elapsed,
	})
}

func executableBindingID(bindings []domain.FactorBinding, factorID, target string, cfg cliConfig) string {
	sourceScope := cfg.ViewID
	if sourceScope == "" {
		sourceScope = cfg.DatasetID
	}
	for _, binding := range bindings {
		bindingSource := binding.SourceViewID
		if bindingSource == "" {
			bindingSource = binding.SourceDataset
		}
		bindingTarget := binding.ResultDatasetID
		if bindingTarget == "" {
			bindingTarget = binding.TargetDataset
		}
		if binding.SpaceID == cfg.SpaceID && bindingSource == sourceScope && binding.Freq == cfg.Freq &&
			binding.FactorID == factorID && bindingTarget == target && domain.BindingAllowsSubject(binding, cfg.SubjectID) {
			return binding.BindingID
		}
	}
	return ""
}

func executableFactorGroups(
	factors []domain.FactorDef,
	bindings []domain.FactorBinding,
	cfg cliConfig,
) map[string][]domain.FactorDef {
	selected := map[string]struct{}{}
	for _, id := range cfg.FactorIDs {
		selected[id] = struct{}{}
	}
	byID := make(map[string]domain.FactorDef, len(factors))
	for _, factor := range factors {
		byID[factor.FactorID] = factor
	}
	groups := map[string][]domain.FactorDef{}
	for _, binding := range bindings {
		sourceScope := cfg.ViewID
		if sourceScope == "" {
			sourceScope = cfg.DatasetID
		}
		bindingSource := binding.SourceViewID
		if bindingSource == "" {
			bindingSource = binding.SourceDataset
		}
		if binding.SpaceID != cfg.SpaceID ||
			bindingSource != sourceScope ||
			binding.Freq != cfg.Freq ||
			!domain.BindingAllowsSubject(binding, cfg.SubjectID) {
			continue
		}
		if len(selected) > 0 {
			if _, ok := selected[binding.FactorID]; !ok {
				continue
			}
		}
		factor, ok := byID[binding.FactorID]
		if !ok {
			continue
		}
		target := binding.TargetDataset
		if target == "" {
			target = registry.ResultDataset(strings.TrimPrefix(strings.TrimSpace(sourceScope), "view_"))
		}
		groups[target] = append(groups[target], factor)
	}
	return groups
}

func serviceAuth() *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{
		AppId: "moox-factor", Operator: "moox-factor",
		RequestId: fmt.Sprintf("factor-%d", time.Now().UnixNano()),
	}
	if secret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"); strings.TrimSpace(secret) != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}

func serviceViewAuth() *commonpb.AuthInfo {
	auth := serviceAuth()
	if secret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET"); strings.TrimSpace(secret) != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}
