package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/packages/pyruntime/moduleregistry"
	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

type Engine struct {
	pool       *pool.Pool
	factory    pool.Factory
	publisher  *moduleregistry.SourcePublisher
	mu         sync.RWMutex
	strategies map[string]process.LoadRequest
}

const (
	maxTargetQuantityLength = 256
	maxDebugInfoBytes       = 16 * 1024
)

var decimalQuantity = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func New(ctx context.Context, python, workerPath string) (*Engine, error) {
	return NewWithWorkers(ctx, python, workerPath, 4)
}

func NewWithWorkers(ctx context.Context, python, workerPath string, workers int) (*Engine, error) {
	_ = ctx
	root := filepath.Join(os.TempDir(), "moox-strategy")
	factory := func(start context.Context) (process.Worker, error) {
		return process.NewStdioWorker(start, process.Config{
			PythonBin: python, WorkerPath: workerPath,
			Hello:       protocol.HelloExpectation{ProtocolVersion: protocol.VersionV1},
			TaskTimeout: 30 * time.Second,
		})
	}
	return &Engine{
		pool: pool.New(workers, factory), factory: factory,
		publisher:  moduleregistry.NewSourcePublisher(root),
		strategies: make(map[string]process.LoadRequest),
	}, nil
}

func (e *Engine) Probe(ctx context.Context) error {
	if e == nil || e.factory == nil {
		return errors.New("strategy engine unavailable")
	}
	worker, err := e.factory(ctx)
	if err != nil {
		return fmt.Errorf("strategy worker handshake: %w", err)
	}
	return worker.Close()
}

func strategyKey(strategyID string) string {
	return strategyID
}

func (e *Engine) Load(ctx context.Context, strategy domain.Strategy) error {
	if e == nil || e.pool == nil {
		return errors.New("strategy engine unavailable")
	}
	if strings.TrimSpace(strategy.ID) == "" || strategy.SourceCode == "" {
		return errors.New("strategy id and source are required")
	}
	manifest, err := registry.Parse(strategy.ManifestYAML)
	if err != nil {
		return err
	}
	published, err := e.publisher.Publish(ctx, moduleregistry.ModuleSource{
		Type: "strategy", LogicalID: strategy.ID, Source: []byte(strategy.SourceCode),
	})
	if err != nil {
		return err
	}
	if strategy.SourceHash != "" && strategy.SourceHash != published.SourceHash {
		return fmt.Errorf(
			"strategy source hash mismatch: expected=%s actual=%s",
			strategy.SourceHash,
			published.SourceHash,
		)
	}
	request := process.LoadRequest{
		LogicalID: strategy.ID, SourceHash: published.SourceHash, Path: published.Path,
		ModuleType: "strategy", EntryPoint: manifest.Entrypoint,
	}
	if err := e.pool.BroadcastLoad(ctx, request); err != nil {
		return err
	}
	e.mu.Lock()
	e.strategies[strategyKey(strategy.ID)] = request
	e.mu.Unlock()
	return nil
}

func (e *Engine) Run(
	ctx context.Context,
	request domain.ExecutionRequest,
	strategy domain.Strategy,
) (domain.Output, string, error) {
	e.mu.RLock()
	loaded, ok := e.strategies[strategyKey(strategy.ID)]
	e.mu.RUnlock()
	if !ok || loaded.SourceHash == "" {
		return domain.Output{}, "", errors.New("strategy source is not loaded")
	}
	if request.StrategyID != strategy.ID {
		return domain.Output{}, "", errors.New("execution strategy id does not match loaded artifact")
	}
	manifest, err := registry.Parse(strategy.ManifestYAML)
	if err != nil {
		return domain.Output{}, "", err
	}
	if historyLength(request.Data) != manifest.Input.HistoryBars {
		return domain.Output{}, "", fmt.Errorf(
			"strategy input requires exactly %d history bars",
			manifest.Input.HistoryBars,
		)
	}
	meta, inputHash, err := buildInput(request)
	if err != nil {
		return domain.Output{}, "", err
	}
	response, err := e.pool.RunLoaded(ctx, request.RunnerID, loaded, process.RunRequest{
		RequestID: request.RequestID, ModuleType: "strategy", LogicalID: loaded.LogicalID,
		SourceHash: loaded.SourceHash, Encoding: protocol.EncodingJSON, Meta: meta,
	})
	if err != nil {
		return domain.Output{}, "", err
	}
	var envelope struct {
		Result domain.Output `json:"result"`
	}
	if err := json.Unmarshal(response.Meta, &envelope); err != nil {
		return domain.Output{}, "", err
	}
	if err := Validate(envelope.Result); err != nil {
		return domain.Output{}, "", err
	}
	return envelope.Result, inputHash, nil
}

func buildInput(request domain.ExecutionRequest) ([]byte, string, error) {
	if strings.TrimSpace(request.StrategyID) == "" ||
		strings.TrimSpace(request.RunnerID) == "" ||
		strings.TrimSpace(request.TriggerBarTime) == "" ||
		strings.TrimSpace(request.Namespace) == "" {
		return nil, "", errors.New("strategy, runner, trigger time and namespace are required")
	}
	if err := validateHistoryWindow(request.Data, request.TriggerBarTime); err != nil {
		return nil, "", err
	}
	contextValue := struct {
		StrategyID     string `json:"strategy_id"`
		RunnerID       string `json:"runner_id"`
		TriggerBarTime string `json:"trigger_bar_time"`
	}{
		StrategyID: request.StrategyID, RunnerID: request.RunnerID,
		TriggerBarTime: request.TriggerBarTime,
	}
	workerInput := struct {
		Context any `json:"context"`
		Data    any `json:"data"`
		Params  any `json:"params"`
	}{
		Context: contextValue, Data: request.Data, Params: request.Params,
	}
	meta, err := json.Marshal(workerInput)
	if err != nil {
		return nil, "", fmt.Errorf("encode strategy input: %w", err)
	}
	hashInput := struct {
		StrategyID string `json:"strategy_id"`
		Namespace  string `json:"namespace"`
		Input      any    `json:"input"`
	}{
		StrategyID: request.StrategyID, Namespace: request.Namespace, Input: workerInput,
	}
	rawHashInput, err := json.Marshal(hashInput)
	if err != nil {
		return nil, "", fmt.Errorf("encode strategy hash input: %w", err)
	}
	sum := sha256.Sum256(rawHashInput)
	return meta, hex.EncodeToString(sum[:]), nil
}

func validateHistoryWindow(data any, triggerBarTime string) error {
	trigger, err := time.Parse(time.RFC3339Nano, triggerBarTime)
	if err != nil {
		return fmt.Errorf("invalid trigger_bar_time: %w", err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode strategy history: %w", err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return errors.New("strategy history must be an array of objects")
	}
	if len(rows) == 0 {
		return errors.New("strategy history must not be empty")
	}
	var previous time.Time
	for index, row := range rows {
		rawTime, ok := row["time"]
		if !ok {
			return fmt.Errorf("strategy history row %d is missing time", index)
		}
		var value string
		if err := json.Unmarshal(rawTime, &value); err != nil {
			return fmt.Errorf("strategy history row %d time must be an RFC3339 string", index)
		}
		current, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return fmt.Errorf("strategy history row %d has invalid time: %w", index, err)
		}
		if index > 0 && !current.After(previous) {
			return fmt.Errorf("strategy history times must be strictly increasing at row %d", index)
		}
		previous = current
	}
	if !previous.Equal(trigger) {
		if previous.After(trigger) {
			return errors.New("strategy history contains a future final bar")
		}
		return errors.New("strategy history final bar is stale")
	}
	return nil
}

func historyLength(data any) int {
	value := reflect.ValueOf(data)
	if !value.IsValid() {
		return 0
	}
	if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
		return value.Len()
	}
	return -1
}

func Validate(output domain.Output) error {
	if output.Action != domain.ActionHold && output.Action != domain.ActionRebalance {
		return fmt.Errorf("invalid action %q", output.Action)
	}
	seen := make(map[string]struct{}, len(output.Targets))
	for _, target := range output.Targets {
		instrumentID := strings.TrimSpace(target.InstrumentID)
		if instrumentID == "" || instrumentID != target.InstrumentID {
			return errors.New("target instrument_id is required without surrounding whitespace")
		}
		if _, exists := seen[instrumentID]; exists {
			return fmt.Errorf("duplicate target instrument_id %q", instrumentID)
		}
		if len(target.Quantity) > maxTargetQuantityLength ||
			strings.TrimSpace(target.Quantity) != target.Quantity ||
			!decimalQuantity.MatchString(target.Quantity) {
			return fmt.Errorf("invalid target quantity %q", target.Quantity)
		}
		if _, ok := new(big.Rat).SetString(target.Quantity); !ok {
			return fmt.Errorf("invalid target quantity %q", target.Quantity)
		}
		seen[instrumentID] = struct{}{}
	}
	debugInfo, err := json.Marshal(output.DebugInfo)
	if err != nil {
		return fmt.Errorf("encode debug_info: %w", err)
	}
	if len(debugInfo) > maxDebugInfoBytes {
		return fmt.Errorf("debug_info exceeds %d bytes", maxDebugInfoBytes)
	}
	return nil
}

func (e *Engine) Close() error {
	if e == nil || e.pool == nil {
		return nil
	}
	return e.pool.Close()
}
