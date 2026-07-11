package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

type RuntimeExecutor struct {
	worker *process.StdioWorker
	cfg    process.Config
}

func NewRuntimeExecutor(ctx context.Context, cfg process.Config) (*RuntimeExecutor, error) {
	w, err := process.NewStdioWorker(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &RuntimeExecutor{worker: w, cfg: cfg}, nil
}
func (e *RuntimeExecutor) Execute(ctx context.Context, task *FactorTask, frame *DataFrame) (*FactorResult, error) {
	meta, err := EncodeJSONRequestMeta(task, frame)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	resp, err := e.worker.Run(ctx, process.RunRequest{RequestID: task.TaskID, ModuleType: "factor", LogicalID: task.Kind, Encoding: protocol.EncodingJSON, Meta: raw})
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(resp.Meta, &out); err != nil {
		return nil, err
	}
	result, err := DecodeJSONResponse(out)
	if err != nil {
		return nil, err
	}
	if len(result.Columns) == 0 {
		return nil, fmt.Errorf("factor worker returned no result columns")
	}
	return result, nil
}
func (e *RuntimeExecutor) Status() WorkerPoolStatus {
	if e == nil || e.worker == nil {
		return WorkerPoolStatus{}
	}
	n := 0
	if e.worker.State() == process.StateReady {
		n = 1
	}
	return WorkerPoolStatus{Workers: n}
}
func (e *RuntimeExecutor) Close() error {
	if e == nil || e.worker == nil {
		return nil
	}
	return e.worker.Close()
}
