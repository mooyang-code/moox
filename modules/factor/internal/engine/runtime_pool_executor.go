package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

type RuntimePoolExecutor struct {
	workers int
	pool    *pool.Pool
	arrow   bool
	hello   protocol.Hello
}

func NewRuntimePoolExecutor(ctx context.Context, workers int, cfg process.Config) (*RuntimePoolExecutor, error) {
	if workers < 1 {
		workers = 1
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.PythonBin == "" {
		cfg.PythonBin = "python3"
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		return nil, err
	}
	p := pool.New(workers, func(start context.Context) (process.Worker, error) { return process.NewStdioWorker(start, cfg) })
	hello, err := p.Warmup(ctx)
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	arrow := false
	arrow = exec.Command(cfg.PythonBin, "-c", "import pyarrow").Run() == nil
	return &RuntimePoolExecutor{workers: workers, pool: p, arrow: arrow, hello: hello}, nil
}

func validateRuntimeConfig(cfg process.Config) error {
	if _, err := exec.LookPath(cfg.PythonBin); err != nil {
		return fmt.Errorf("factor python_bin %q is not executable: %w", cfg.PythonBin, err)
	}
	workerPath := filepath.Clean(cfg.WorkerPath)
	if workerPath == "." || workerPath == "" {
		return errors.New("factor python worker path is required")
	}
	if info, err := os.Stat(workerPath); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return fmt.Errorf("factor python worker path %q is invalid: %w", workerPath, err)
	}
	for _, flag := range []string{"--factors-dir", "--sections-dir"} {
		for i := 0; i+1 < len(cfg.Args); i++ {
			if cfg.Args[i] != flag {
				continue
			}
			info, err := os.Stat(cfg.Args[i+1])
			if err != nil || !info.IsDir() {
				if err == nil {
					err = errors.New("not a directory")
				}
				return fmt.Errorf("factor %s %q is invalid: %w", strings.TrimPrefix(flag, "--"), cfg.Args[i+1], err)
			}
		}
	}
	return nil
}
func (e *RuntimePoolExecutor) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error) {
	prepared := *task
	if !e.arrow {
		// A worker without pyarrow cannot open the mmap snapshot. Keep the
		// documented JSON fallback explicit and include the frame payload.
		prepared.SnapshotID, prepared.SnapshotHash, prepared.SnapshotPath = "", "", ""
	}
	meta, err := EncodeJSONRequestMeta(&prepared, frame)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	run := process.RunRequest{RequestID: task.TaskID, ModuleType: "factor", LogicalID: task.Kind, Encoding: protocol.EncodingJSON, Meta: raw}
	if prepared.SnapshotPath != "" {
		run.Encoding = protocol.EncodingArrowMMap
	}
	if hasSourcePath(prepared.Factors) {
		loads := make([]process.LoadRequest, 0, len(task.Factors))
		for _, factor := range task.Factors {
			if factor.SourcePath == "" {
				continue
			}
			loads = append(loads, process.LoadRequest{LogicalID: factor.Name, SourceHash: factor.SourceHash, Path: factor.SourcePath, ModuleType: "factor"})
		}
		resp, err := e.pool.RunLoadedMany(ctx, prepared.SubjectID, loads, run)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal(resp.Meta, &out); err != nil {
			return nil, err
		}
		return DecodeJSONResponse(out)
	}
	resp, err := e.pool.Run(ctx, pool.Request{ShardKey: prepared.SubjectID, Run: run})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Meta, &out); err != nil {
		return nil, err
	}
	return DecodeJSONResponse(out)
}

func hasSourcePath(specs []FactorSpec) bool {
	for _, spec := range specs {
		if spec.SourcePath != "" {
			return true
		}
	}
	return false
}
func (e *RuntimePoolExecutor) Status() WorkerPoolStatus {
	if e == nil {
		return WorkerPoolStatus{}
	}
	return WorkerPoolStatus{Workers: e.workers, Ready: e.hello.WorkerVersion != "" && e.pool.Ready(), WorkerVersion: e.hello.WorkerVersion, PythonVersion: e.hello.PythonVersion, RuntimeEnvHash: e.hello.RuntimeEnvHash, ArrowAvailable: e.arrow}
}
func (e *RuntimePoolExecutor) Close() error {
	if e == nil || e.pool == nil {
		return nil
	}
	return e.pool.Close()
}
