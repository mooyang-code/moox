package pyexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/pyruntime/pool"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/pyruntime/protocol"
)

var sourceHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Pool struct {
	pool        *pool.Pool
	slots       chan struct{}
	taskTimeout time.Duration
	busy        atomic.Int32
	requests    atomic.Uint64
	mu          sync.RWMutex
	closed      bool
	closeErr    error
}

var _ Executor = (*Pool)(nil)

type batchResponse struct {
	ID       string         `json:"id"`
	OK       bool           `json:"ok"`
	Encoding string         `json:"encoding"`
	Items    []responseItem `json:"items"`
}

type responseItem struct {
	FactorID string          `json:"factor_id"`
	OK       bool            `json:"ok"`
	Results  responseResults `json:"results"`
	Error    *responseError  `json:"error"`
}

type responseResults struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

type responseError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func New(ctx context.Context, workers int, cfg process.Config) (*Pool, error) {
	if workers < 1 {
		workers = 1
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if cfg.TaskTimeout <= 0 {
		cfg.TaskTimeout = 30 * time.Second
	}
	if cfg.Hello.ProtocolVersion == "" {
		cfg.Hello.ProtocolVersion = protocol.VersionV1
	}
	if cfg.Hello.RequiredEncoding == "" {
		cfg.Hello.RequiredEncoding = protocol.EncodingJSON
	}
	processPool := pool.New(workers, func(start context.Context) (process.Worker, error) {
		return process.NewStdioWorker(start, cfg)
	})
	if _, err := processPool.WarmupOne(ctx); err != nil {
		_ = processPool.Close()
		return nil, fmt.Errorf("warm up factor Python worker: %w", err)
	}
	return &Pool{pool: processPool, slots: make(chan struct{}, workers), taskTimeout: cfg.TaskTimeout}, nil
}

func (p *Pool) Exec(ctx context.Context, req Request) ([]ItemResult, error) {
	if p == nil {
		return nil, errors.New("factor Python executor is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := fmt.Sprintf("factor-period-%d", p.requests.Add(1))
	meta, err := encodeRequest(requestID, req)
	if err != nil {
		return nil, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return nil, errors.New("factor Python executor is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	p.busy.Add(1)
	defer p.busy.Add(-1)
	timeout := p.taskTimeout * time.Duration(len(req.Factors))
	if timeout <= 0 {
		timeout = p.taskTimeout
	}
	response, err := p.pool.RunAnyLoadedMany(ctx, nil, process.RunRequest{
		RequestID: requestID, ModuleType: "factor", LogicalID: requestID,
		Encoding: protocol.EncodingJSON, Timeout: timeout, Meta: meta,
	})
	if err != nil {
		return nil, fmt.Errorf("execute factor batch: %w", err)
	}
	return decodeResponse(response.Meta, requestID, req.Factors)
}

func (p *Pool) Busy() int {
	if p == nil {
		return 0
	}
	return int(p.busy.Load())
}

func (p *Pool) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return p.closeErr
	}
	p.closed = true
	if p.pool != nil {
		p.closeErr = p.pool.Close()
	}
	return p.closeErr
}

func decodeResponse(raw json.RawMessage, requestID string, factors []FactorCall) ([]ItemResult, error) {
	var response batchResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decode factor batch response: %w", err)
	}
	if response.ID != requestID || !response.OK || response.Encoding != "json" || len(response.Items) != len(factors) {
		return nil, fmt.Errorf("%w: id=%q ok=%t encoding=%q items=%d", errInvalidResponse,
			response.ID, response.OK, response.Encoding, len(response.Items))
	}
	expected := make(map[string]FactorCall, len(factors))
	for _, factor := range factors {
		expected[factor.FactorID] = factor
	}
	seen := make(map[string]struct{}, len(response.Items))
	results := make([]ItemResult, 0, len(response.Items))
	for i, item := range response.Items {
		factor, ok := expected[item.FactorID]
		if !ok || item.FactorID == "" {
			return nil, fmt.Errorf("%w: item %d has unknown factor_id %q", errInvalidResponse, i, item.FactorID)
		}
		if _, ok := seen[item.FactorID]; ok {
			return nil, fmt.Errorf("%w: duplicate factor_id %q", errInvalidResponse, item.FactorID)
		}
		seen[item.FactorID] = struct{}{}
		result := ItemResult{FactorID: item.FactorID}
		if !item.OK {
			if item.Error == nil || strings.TrimSpace(item.Error.Message) == "" {
				return nil, fmt.Errorf("%w: factor %s has no error detail", errInvalidResponse, item.FactorID)
			}
			kind := item.Error.Type
			if kind == "" {
				kind = "factor error"
			}
			result.Err = fmt.Errorf("%s: %s", kind, item.Error.Message)
			results = append(results, result)
			continue
		}
		if item.Error != nil {
			return nil, fmt.Errorf("%w: factor %s has both result and error", errInvalidResponse, item.FactorID)
		}
		identity := []string{"data_time", "series_tag"}
		if factor.FactorType == "cross_section" {
			identity = append(identity, "subject_id")
		}
		wantColumns := append(identity, factor.Outputs...)
		if len(item.Results.Columns) != len(wantColumns) {
			return nil, fmt.Errorf("%w: factor %s output column count mismatch", errInvalidResponse, item.FactorID)
		}
		for i, column := range wantColumns {
			if item.Results.Columns[i] != column {
				return nil, fmt.Errorf("%w: factor %s output column %d = %q, want %q", errInvalidResponse,
					item.FactorID, i, item.Results.Columns[i], column)
			}
		}
		for row, values := range item.Results.Rows {
			if len(values) != len(wantColumns) {
				return nil, fmt.Errorf("%w: factor %s row %d width mismatch", errInvalidResponse, item.FactorID, row)
			}
		}
		result.Columns = append([]string(nil), item.Results.Columns...)
		result.Rows = item.Results.Rows
		results = append(results, result)
	}
	if len(seen) != len(expected) {
		return nil, fmt.Errorf("%w: received %d of %d factor results", errInvalidResponse, len(seen), len(expected))
	}
	return results, nil
}
