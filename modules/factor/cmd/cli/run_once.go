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
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

type runOnceRuntime struct {
	DBPath        string
	FactorsDir    string
	PythonBin     string
	WorkerPath    string
	GatewayTarget string
	GatewayNodeID string
	Credentials   gatewayauth.Credentials
	Workers       int
	TaskTimeout   time.Duration
	MaxRetry      int
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
		DBPath:        resolveRuntimePath(appCfg.Database.Path),
		FactorsDir:    resolveRuntimePath(appCfg.Engine.FactorsDir),
		PythonBin:     appCfg.Engine.PythonBin,
		WorkerPath:    resolveRuntimePath(appCfg.Engine.WorkerPath),
		GatewayTarget: appCfg.Storage.GatewayTarget,
		GatewayNodeID: appCfg.Storage.GatewayNodeID,
		Credentials:   credentials,
		Workers:       1,
		TaskTimeout:   time.Duration(appCfg.Engine.TaskTimeoutMS) * time.Millisecond,
		MaxRetry:      appCfg.Scheduler.MaxRetry,
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
	)
	pythonExec, err := engine.NewPythonExecutor(ctx, runtimeCfg.Workers, process.Config{
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
	runner := scheduler.NewService(scheduler.Config{
		Workers: runtimeCfg.Workers, QueueCapacity: 1, MaxRetry: runtimeCfg.MaxRetry,
	}, storageClient, pythonExec)
	started := time.Now()
	targets := make([]string, 0, len(groups))
	for target := range groups {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	factorCount := 0
	for i, target := range targets {
		currentTaskID := taskID
		if len(targets) > 1 {
			currentTaskID = fmt.Sprintf("%s-%d", taskID, i+1)
		}
		task, buildErr := scheduler.BuildTask(scheduler.TaskScope{
			TaskID: currentTaskID, TriggerType: "manual", SpaceID: cfg.SpaceID,
			SourceDataset: cfg.DatasetID, TargetDataset: target,
			SubjectID: cfg.SubjectID, Freq: cfg.Freq,
			StartTime: cfg.StartTime, EndTime: cfg.EndTime,
		}, groups[target], runtimeCfg.FactorsDir)
		if buildErr != nil {
			return buildErr
		}
		if runErr := runner.Run(ctx, task); runErr != nil {
			return runErr
		}
		factorCount += len(groups[target])
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
		if binding.SpaceID != cfg.SpaceID ||
			binding.SourceDataset != cfg.DatasetID ||
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
			target = registry.ResultDataset(cfg.DatasetID)
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
	if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}
