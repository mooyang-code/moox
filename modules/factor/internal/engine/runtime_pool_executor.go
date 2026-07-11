package engine

import (
	"context"
	"encoding/json"
	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

type RuntimePoolExecutor struct {
	workers int
	pool    *pool.Pool
}

func NewRuntimePoolExecutor(ctx context.Context, workers int, cfg process.Config) (*RuntimePoolExecutor, error) {
	_ = ctx
	if workers < 1 {
		workers = 1
	}
	p := pool.New(workers, func(start context.Context) (process.Worker, error) { return process.NewStdioWorker(start, cfg) })
	return &RuntimePoolExecutor{workers: workers, pool: p}, nil
}
func (e *RuntimePoolExecutor) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error) {
	meta, err := EncodeJSONRequestMeta(task, frame)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	run := process.RunRequest{RequestID: task.TaskID, ModuleType: "factor", LogicalID: task.Kind, Encoding: protocol.EncodingJSON, Meta: raw}
	if len(task.Factors) > 0 && task.Factors[0].SourcePath != "" {
		loads := make([]process.LoadRequest, 0, len(task.Factors))
		for _, factor := range task.Factors {
			if factor.SourcePath == "" {
				continue
			}
			loads = append(loads, process.LoadRequest{LogicalID: factor.Name, SourceHash: factor.SourceHash, Path: factor.SourcePath, ModuleType: "factor"})
		}
		resp, err := e.pool.RunLoadedMany(ctx, task.SubjectID, loads, run)
		if err != nil {
			return nil, err
		}
		var out map[string]any
		if err := json.Unmarshal(resp.Meta, &out); err != nil {
			return nil, err
		}
		return DecodeJSONResponse(out)
	}
	resp, err := e.pool.Run(ctx, pool.Request{ShardKey: task.SubjectID, Run: run})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Meta, &out); err != nil {
		return nil, err
	}
	return DecodeJSONResponse(out)
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
