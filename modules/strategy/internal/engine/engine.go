package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

type Engine struct {
	pool      *pool.Pool
	publisher *moduleregistry.SourcePublisher
	mu        sync.RWMutex
	versions  map[string]process.LoadRequest
}

func New(ctx context.Context, python, workerPath string) (*Engine, error) {
	root := filepath.Join(os.TempDir(), "moox-strategy")
	factory := func(start context.Context) (process.Worker, error) {
		return process.NewStdioWorker(start, process.Config{PythonBin: python, WorkerPath: workerPath, Hello: protocol.HelloExpectation{ProtocolVersion: protocol.VersionV1}, TaskTimeout: 30 * 1e9})
	}
	p := pool.New(4, func(start context.Context) (process.Worker, error) { return factory(start) })
	return &Engine{pool: p, publisher: moduleregistry.NewSourcePublisher(root), versions: make(map[string]process.LoadRequest)}, nil
}

func versionKey(strategyID, version string) string { return strategyID + "/" + version }

func (e *Engine) Load(ctx context.Context, d domain.StrategyDefinition) error {
	if e == nil || e.pool == nil {
		return errors.New("strategy engine unavailable")
	}
	v, err := e.publisher.Publish(ctx, moduleregistry.ModuleSource{Type: "strategy", LogicalID: d.StrategyID + "-" + d.Version, Source: []byte(d.SourceCode)})
	if err != nil {
		return err
	}
	req := process.LoadRequest{LogicalID: d.StrategyID + "/" + d.Version, SourceHash: v.SourceHash, Path: v.Path, ModuleType: "strategy"}
	if err := e.pool.BroadcastLoad(ctx, req); err != nil {
		return err
	}
	e.mu.Lock()
	e.versions[versionKey(d.StrategyID, d.Version)] = req
	e.mu.Unlock()
	return nil
}

func (e *Engine) Run(ctx context.Context, t domain.Task, d domain.StrategyDefinition) (domain.Output, string, error) {
	e.mu.RLock()
	req, loaded := e.versions[versionKey(d.StrategyID, d.Version)]
	e.mu.RUnlock()
	if !loaded || req.SourceHash == "" {
		return domain.Output{}, "", errors.New("strategy source is not loaded")
	}
	state := map[string]any{}
	if t.PreviousState.StateJSON != "" {
		if err := json.Unmarshal([]byte(t.PreviousState.StateJSON), &state); err != nil {
			return domain.Output{}, "", fmt.Errorf("decode state: %w", err)
		}
	}
	raw, err := json.Marshal(map[string]any{"context": map[string]any{"api_version": "moox.strategy/v1", "trigger_bar_time": t.TriggerBarTime, "data_revision": t.DataRevision, "previous_targets": t.PreviousTargets}, "data": t.Data, "params": t.Params, "state": state})
	if err != nil {
		return domain.Output{}, "", fmt.Errorf("encode strategy input: %w", err)
	}
	resp, err := e.pool.RunLoaded(ctx, t.BindingID, req, process.RunRequest{RequestID: t.RunID, ModuleType: "strategy", LogicalID: req.LogicalID, SourceHash: req.SourceHash, Encoding: protocol.EncodingJSON, Meta: raw})
	if err != nil {
		return domain.Output{}, "", err
	}
	var envelope struct {
		Result domain.Output `json:"result"`
	}
	if err := json.Unmarshal(resp.Meta, &envelope); err != nil {
		return domain.Output{}, "", err
	}
	if err := Validate(envelope.Result); err != nil {
		return domain.Output{}, "", err
	}
	sum := sha256.Sum256(raw)
	return envelope.Result, hex.EncodeToString(sum[:]), nil
}

func Validate(o domain.Output) error {
	if o.Action != domain.ActionHold && o.Action != domain.ActionRebalance {
		return fmt.Errorf("invalid action %q", o.Action)
	}
	seen := map[string]bool{}
	for _, target := range o.Targets {
		if target.InstrumentID == "" || seen[target.InstrumentID] {
			return errors.New("duplicate or empty instrument")
		}
		seen[target.InstrumentID] = true
	}
	if o.NextState == nil {
		return errors.New("next_state is required")
	}
	return nil
}

func (e *Engine) Close() error {
	if e == nil || e.pool == nil {
		return nil
	}
	return e.pool.Close()
}
