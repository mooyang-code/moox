package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

// Executor executes one factor task against an input frame.
type Executor interface {
	Execute(context.Context, *FactorTask, *DataFrame) (*FactorResult, error)
	Close() error
}

// ExecutorStatus describes the local Python runtime.
type ExecutorStatus struct {
	Workers        int
	Ready          bool
	WorkerVersion  string
	PythonVersion  string
	RuntimeEnvHash string
}

// PythonWorkerPool owns the local pool of Python factor workers.
type PythonWorkerPool struct {
	workers int
	pool    *pool.Pool
	hello   protocol.Hello
}

func NewPythonWorkerPool(ctx context.Context, workers int, cfg process.Config) (*PythonWorkerPool, error) {
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
	p := pool.New(workers, func(start context.Context) (process.Worker, error) {
		return process.NewStdioWorker(start, cfg)
	})
	hello, err := p.WarmupOne(ctx)
	if err != nil {
		_ = p.Close()
		return nil, err
	}
	return &PythonWorkerPool{workers: workers, pool: p, hello: hello}, nil
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
	for i := 0; i+1 < len(cfg.Args); i++ {
		if cfg.Args[i] != "--factors-dir" {
			continue
		}
		if info, err := os.Stat(cfg.Args[i+1]); err != nil || !info.IsDir() {
			if err == nil {
				err = errors.New("not a directory")
			}
			return fmt.Errorf("factor factors-dir %q is invalid: %w", cfg.Args[i+1], err)
		}
	}
	return nil
}

func (e *PythonWorkerPool) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error) {
	meta, err := EncodeJSONRequestMeta(task, frame)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	run := process.RunRequest{
		RequestID:  task.TaskID,
		ModuleType: "factor",
		LogicalID:  task.TaskID,
		Encoding:   protocol.EncodingJSON,
		Meta:       raw,
	}
	factor := task.Factor
	var loads []process.LoadRequest
	if factor.SourcePath != "" {
		loads = append(loads, process.LoadRequest{
			LogicalID: factor.Name, SourceHash: factor.SourceHash,
			Path: factor.SourcePath, ModuleType: "factor",
		})
	}
	response, err := e.pool.RunAnyLoadedMany(ctx, loads, run)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(response.Meta, &out); err != nil {
		return nil, err
	}
	return DecodeJSONResponse(out)
}

func (e *PythonWorkerPool) Status() ExecutorStatus {
	if e == nil {
		return ExecutorStatus{}
	}
	return ExecutorStatus{
		Workers: e.workers, Ready: e.hello.WorkerVersion != "" && e.pool.ReadyStarted(),
		WorkerVersion: e.hello.WorkerVersion, PythonVersion: e.hello.PythonVersion,
		RuntimeEnvHash: e.hello.RuntimeEnvHash,
	}
}

func (e *PythonWorkerPool) Close() error {
	if e == nil || e.pool == nil {
		return nil
	}
	return e.pool.Close()
}
