package engine

import (
	"context"
	"encoding/json"
	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
	"os/exec"
)

type RuntimePoolExecutor struct {
	workers int
	pool    *pool.Pool
	arrow   bool
}

func NewRuntimePoolExecutor(ctx context.Context, workers int, cfg process.Config) (*RuntimePoolExecutor, error) {
	_ = ctx
	if workers < 1 {
		workers = 1
	}
	p := pool.New(workers, func(start context.Context) (process.Worker, error) { return process.NewStdioWorker(start, cfg) })
	arrow := false
	if cfg.PythonBin != "" {
		arrow = exec.Command(cfg.PythonBin, "-c", "import pyarrow").Run() == nil
	}
	return &RuntimePoolExecutor{workers: workers, pool: p, arrow: arrow}, nil
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
	return WorkerPoolStatus{Workers: e.workers}
}
func (e *RuntimePoolExecutor) Close() error {
	if e == nil || e.pool == nil {
		return nil
	}
	return e.pool.Close()
}
