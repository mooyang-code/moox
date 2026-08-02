// Package marketfetch contains the short-lived SCF execution path.  An
// invocation fetches one bounded batch, performs one aggregate Storage write,
// publishes one completion event, and then returns.
package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultConcurrency = 5
	MaxConcurrency     = 64
	// MaxRealtimeItems bounds the work accepted by one short-lived SCF. The
	// scheduler partitions a minute's symbols across the available functions;
	// with ten functions this permits 479 symbols to fan out as ten requests of
	// at most 48 symbols each. The function still caps its own HTTP concurrency
	// and performs one aggregate Storage write.
	MaxRealtimeItems = 64
	MaxRealtimeRows  = 3
	// Symbol metadata is intentionally an explicit, small manual task. The
	// Storage metadata endpoint refreshes its snapshot per subject, so a full
	// exchange-wide symbol dump does not belong in a 10-second SCF invocation.
	// Metadata registration is intentionally serial against Storage. Keep the
	// manual symbol task bounded so it can complete inside the fixed 10-second
	// SCF budget even when Storage is briefly slow.
	MaxSymbolTaskSymbols = 20
)

// Request is the JSON payload accepted by a market_fetch SCF invocation.
type Request struct {
	BatchID     string                  `json:"batch_id"`
	ScheduleID  string                  `json:"schedule_id,omitempty"`
	BatchKind   domain.BatchKind        `json:"batch_kind"`
	SpaceID     string                  `json:"space_id"`
	DatasetID   string                  `json:"dataset_id,omitempty"`
	Frequency   string                  `json:"frequency,omitempty"`
	Provider    string                  `json:"provider"`
	MarketType  string                  `json:"market_type"`
	Region      string                  `json:"region"`
	NodeID      string                  `json:"node_id"`
	RequestID   string                  `json:"request_id,omitempty"`
	ShardIndex  int                     `json:"shard_index,omitempty"`
	Concurrency int                     `json:"concurrency,omitempty"`
	Items       []domain.CollectionItem `json:"items"`
}

func (r *Request) validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if strings.TrimSpace(r.BatchID) == "" || strings.TrimSpace(r.SpaceID) == "" {
		return fmt.Errorf("batch_id and space_id are required")
	}
	if len(r.Items) == 0 {
		return fmt.Errorf("items must not be empty")
	}
	r.DatasetID = strings.TrimSpace(r.DatasetID)
	if r.DatasetID == "" {
		return fmt.Errorf("dataset_id is required")
	}
	if r.BatchKind == "" {
		r.BatchKind = domain.BatchKindRealtime
	}
	maxItems := MaxRealtimeItems
	switch r.BatchKind {
	case domain.BatchKindRealtime:
	case domain.BatchKindCatchup, domain.BatchKindSymbolSnapshot:
		maxItems = 1
	default:
		return fmt.Errorf("unsupported batch_kind %q", r.BatchKind)
	}
	if len(r.Items) > maxItems {
		return fmt.Errorf("items exceed maximum batch size %d for %s", maxItems, r.BatchKind)
	}
	seenTaskIDs := make(map[string]struct{}, len(r.Items))
	for index, item := range r.Items {
		if r.BatchKind != domain.BatchKindSymbolSnapshot && (strings.TrimSpace(item.SubjectID) == "" || strings.TrimSpace(item.Symbol) == "") {
			return fmt.Errorf("items[%d] subject_id and symbol are required", index)
		}
		if strings.TrimSpace(item.DatasetID) != r.DatasetID {
			return fmt.Errorf("items[%d] dataset_id differs from batch dataset", index)
		}
		if strings.TrimSpace(item.TaskID) != "" {
			if _, exists := seenTaskIDs[item.TaskID]; exists {
				return fmt.Errorf("items[%d] task_id %q is duplicated", index, item.TaskID)
			}
			seenTaskIDs[item.TaskID] = struct{}{}
		}
		if r.BatchKind == domain.BatchKindCatchup {
			if strings.TrimSpace(item.StartTime) == "" || item.BarLimit <= 0 || item.BarLimit > 1000 {
				return fmt.Errorf("catchup item requires start_time and bar_limit 1..1000")
			}
		} else if r.BatchKind == domain.BatchKindRealtime && item.BarLimit > MaxRealtimeRows {
			return fmt.Errorf("realtime bar_limit must be between 1 and %d", MaxRealtimeRows)
		} else if r.BatchKind == domain.BatchKindSymbolSnapshot && len(item.Allowlist) > MaxSymbolTaskSymbols {
			return fmt.Errorf("symbol allowlist exceeds maximum %d", MaxSymbolTaskSymbols)
		}
	}
	if r.Concurrency < 0 || r.Concurrency > MaxConcurrency {
		return fmt.Errorf("concurrency must be between 0 and %d", MaxConcurrency)
	}
	return nil
}

// Storage is deliberately the small batch boundary needed by the executor.
type Storage interface {
	UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error
	RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error
}

type sourceStorage interface {
	UpsertFieldsWithSource(context.Context, []*storagepb.RowFieldUpsert, string) error
}

type symbolReconciler interface {
	ReconcileSymbolSnapshot(context.Context, string, string, []*exchange.SymbolInfo) error
}

type Executor struct {
	Klines interface {
		FetchRealtimeRows(context.Context, *sources.CollectParams, int) ([]*storagepb.RowFieldUpsert, time.Time, error)
	}
	Catchup interface {
		FetchCatchupRows(context.Context, *sources.CollectParams, time.Time, int) ([]*storagepb.RowFieldUpsert, time.Time, error)
	}
	Symbols interface {
		FetchSymbolSnapshot(context.Context, *sources.CollectParams, []string) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error)
	}
	Storage Storage
	Now     func() time.Time
	// CommitReserve is the total tail budget reserved for Storage plus the
	// completion publish. StorageReserve is the portion used by the aggregate
	// Storage write; keeping the two separate prevents publish from starting
	// after the invocation deadline has already expired.
	CommitReserve  time.Duration
	StorageReserve time.Duration
}

type execution struct {
	item  domain.CollectionItem
	rows  []*storagepb.RowFieldUpsert
	items []*exchangeItem
}

// exchangeItem keeps symbol metadata private to this package while allowing
// the symbol path to perform metadata registration after the aggregate row
// write.
type exchangeItem struct {
	register []*storagepb.RegisterDataSubjectReq
}

// Execute fetches all items concurrently, writes all successful rows once,
// then returns a completion payload. No retry is performed in the function;
// retryable outcomes are handed back to the scheduler in the event payload.
func (e *Executor) Execute(ctx context.Context, req Request) (*marketfetchpb.MarketFetchBatchCompleted, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if e == nil || e.Klines == nil || e.Symbols == nil || e.Storage == nil {
		return nil, fmt.Errorf("market fetch executor is not initialized")
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	started := now()
	workCtx := ctx
	commitReserve := e.CommitReserve
	if commitReserve < 0 {
		commitReserve = 0
	}
	if commitReserve > 0 {
		if deadline, ok := ctx.Deadline(); ok {
			workDeadline := deadline.Add(-commitReserve)
			if workDeadline.Before(time.Now()) {
				workDeadline = time.Now().Add(time.Millisecond)
			}
			var cancel context.CancelFunc
			workCtx, cancel = context.WithDeadline(ctx, workDeadline)
			defer cancel()
		} else {
			var cancel context.CancelFunc
			workCtx, cancel = context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
		}
	}
	results := make([]domain.ItemResult, len(req.Items))
	rows := make([]*storagepb.RowFieldUpsert, 0)
	registrations := make([]*storagepb.RegisterDataSubjectReq, 0)
	rowCountByItem := make(map[int]int, len(req.Items))
	symbolsByItem := make(map[int][]*exchange.SymbolInfo, len(req.Items))
	concurrency := req.Concurrency
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > len(req.Items) {
		concurrency = len(req.Items)
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for index, item := range req.Items {
		index, item := index, item
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-workCtx.Done():
				results[index] = failureResult(item, domain.ItemOutcomeNetworkError, "deadline_exhausted", workCtx.Err())
				return
			}
			defer func() { <-sem }()
			result, itemRows, itemRegs, itemSymbols := e.executeItem(workCtx, req, item)
			mu.Lock()
			results[index] = result
			rows = append(rows, itemRows...)
			registrations = append(registrations, itemRegs...)
			rowCountByItem[index] = len(itemRows)
			symbolsByItem[index] = itemSymbols
			mu.Unlock()
		}()
	}
	wg.Wait()

	// A single Storage write is used for the complete successful row set. If it
	// fails, none of those items are reported as successful.
	storageReserve := e.StorageReserve
	if storageReserve <= 0 {
		storageReserve = commitReserve
	}
	commitCtx := ctx
	if storageReserve > 0 {
		var cancel context.CancelFunc
		commitCtx, cancel = context.WithTimeout(ctx, storageReserve)
		defer cancel()
	}
	commitCtx = binance.SingleAttempt(commitCtx)
	var writeErr error
	if len(rows) > 0 {
		if sourced, ok := e.Storage.(sourceStorage); ok {
			writeErr = sourced.UpsertFieldsWithSource(commitCtx, rows, req.BatchID)
		} else {
			writeErr = e.Storage.UpsertFields(commitCtx, rows)
		}
	}
	if writeErr != nil {
		for index := range results {
			if results[index].Outcome == domain.ItemOutcomeSuccess {
				results[index].Outcome = domain.ItemOutcomeStorageError
				results[index].ErrorType = "storage"
				results[index].ErrorSummary = truncateError(writeErr)
			}
		}
	}
	for index := range results {
		if results[index].Outcome == domain.ItemOutcomeSuccess && isKlineItem(results[index].DataType) && rowCountByItem[index] == 0 {
			results[index].Outcome = domain.ItemOutcomeStorageError
			results[index].ErrorType = "empty_data"
			results[index].ErrorSummary = "Binance returned no closed bars"
		}
	}
	if writeErr == nil {
		if err := registerSymbols(commitCtx, e.Storage, registrations); err != nil {
			for index := range results {
				if results[index].Outcome == domain.ItemOutcomeSuccess {
					results[index].Outcome = domain.ItemOutcomeStorageError
					results[index].ErrorType = "metadata"
					results[index].ErrorSummary = truncateError(err)
				}
			}
		}
		if reconciler, ok := e.Storage.(symbolReconciler); ok {
			for index, result := range results {
				if result.Outcome != domain.ItemOutcomeSuccess || !isSymbolItem(result.DataType) || result.DatasetID == "" {
					continue
				}
				if err := reconciler.ReconcileSymbolSnapshot(commitCtx, req.SpaceID, result.DatasetID, symbolsByItem[index]); err != nil {
					results[index].Outcome = domain.ItemOutcomeStorageError
					results[index].ErrorType = "metadata_reconcile"
					results[index].ErrorSummary = truncateError(err)
				}
			}
		}
	}

	payload := buildCompletion(req, results, now(), now().Sub(started))
	if payload.GetStatus() != "succeeded" {
		// Keep the only SCF completion log compact and actionable. Successful
		// batches are represented by their EventBus completion event; warning
		// storage is reserved for batches that need attention.
		log.WarnContextf(ctx, "market_fetch_result batch_id=%s status=%s results=%s", req.BatchID, payload.GetStatus(), compactResultOutcomes(results))
		waitForCLSWarnDispatch(ctx)
	}
	return payload, nil
}

func waitForCLSWarnDispatch(ctx context.Context) {
	// trpc-log-cls is asynchronous. Keep a short, bounded handoff window so a
	// short-lived SCF does not return before the 100ms CLS batch timer runs.
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func compactResultOutcomes(results []domain.ItemResult) string {
	parts := make([]string, 0, len(results))
	for _, result := range results {
		symbol := strings.TrimSpace(result.SubjectID)
		if symbol == "" {
			symbol = strings.TrimSpace(result.Symbol)
		}
		if symbol == "" {
			symbol = "unknown"
		}
		parts = append(parts, symbol+":"+string(result.Outcome))
	}
	return strings.Join(parts, ",")
}

// registerSymbols deliberately stays serial because each metadata write
// refreshes the Storage metadata snapshot and the backing store is SQLite.
// The scheduler limits this manual task to a small explicit allowlist.
func registerSymbols(ctx context.Context, storage Storage, registrations []*storagepb.RegisterDataSubjectReq) error {
	if len(registrations) == 0 {
		return nil
	}
	for _, registration := range registrations {
		if err := storage.RegisterDataSubject(ctx, registration); err != nil {
			return err
		}
	}
	return nil
}

func (e *Executor) executeItem(ctx context.Context, req Request, item domain.CollectionItem) (domain.ItemResult, []*storagepb.RowFieldUpsert, []*storagepb.RegisterDataSubjectReq, []*exchange.SymbolInfo) {
	if strings.TrimSpace(item.Provider) == "" {
		item.Provider = req.Provider
	}
	if strings.TrimSpace(item.MarketType) == "" {
		item.MarketType = req.MarketType
	}
	if strings.TrimSpace(item.DatasetID) == "" {
		item.DatasetID = req.DatasetID
	}
	if strings.TrimSpace(item.Frequency) == "" {
		item.Frequency = req.Frequency
	}
	if !strings.EqualFold(item.Provider, "binance") {
		return failureResult(item, domain.ItemOutcomeInvalid, "provider", fmt.Errorf("unsupported provider %q", item.Provider)), nil, nil, nil
	}
	instType, err := binance.InstTypeForMarket(item.MarketType)
	if err != nil {
		return failureResult(item, domain.ItemOutcomeInvalid, "market_type", err), nil, nil, nil
	}
	params := &sources.CollectParams{SpaceID: req.SpaceID, DatasetID: item.DatasetID, InstType: instType, Symbol: item.Symbol, SubjectID: item.SubjectID, Interval: item.Frequency, Live: req.BatchKind == domain.BatchKindRealtime}
	requestCtx, cancel := requestContext(ctx)
	defer cancel()
	if req.BatchKind == domain.BatchKindCatchup {
		if e.Catchup == nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "catchup", fmt.Errorf("catchup collector is not initialized")), nil, nil, nil
		}
		start, parseErr := time.Parse(time.RFC3339Nano, item.StartTime)
		if parseErr != nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "start_time", parseErr), nil, nil, nil
		}
		rows, latest, fetchErr := e.Catchup.FetchCatchupRows(requestCtx, params, start, item.BarLimit)
		if fetchErr != nil {
			return failureResult(item, classifyError(fetchErr), errorType(fetchErr), fetchErr), nil, nil, nil
		}
		if len(rows) == 0 || latest.IsZero() {
			return failureResult(item, domain.ItemOutcomeStorageError, "empty_data", fmt.Errorf("Binance returned no closed bars")), nil, nil, nil
		}
		return successResult(item), rows, nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(item.DataType)) {
	case "kline", "candles", "candle":
		limit := item.BarLimit
		if limit <= 0 {
			limit = MaxRealtimeRows
		}
		rows, latest, err := e.Klines.FetchRealtimeRows(requestCtx, params, limit)
		if err != nil {
			return failureResult(item, classifyError(err), errorType(err), err), nil, nil, nil
		}
		if len(rows) == 0 || latest.IsZero() {
			return failureResult(item, domain.ItemOutcomeStorageError, "empty_data", fmt.Errorf("Binance returned no closed bars")), nil, nil, nil
		}
		if target, parseErr := time.Parse(time.RFC3339Nano, item.TargetDataTime); item.TargetDataTime != "" && parseErr == nil && latest.Before(target) {
			return failureResult(item, domain.ItemOutcomeStorageError, "stale_data", fmt.Errorf("latest closed bar %s is older than target %s", latest.UTC().Format(time.RFC3339Nano), target.UTC().Format(time.RFC3339Nano))), nil, nil, nil
		}
		return successResult(item), rows, nil, nil
	case "symbol", "symbols":
		rows, symbols, _, err := e.Symbols.FetchSymbolSnapshot(requestCtx, params, item.Allowlist)
		if err != nil {
			return failureResult(item, classifyError(err), errorType(err), err), nil, nil, nil
		}
		register, err := binance.BuildSymbolRegisterRequests(req.SpaceID, item.DatasetID, instType, symbols)
		if err != nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "metadata", err), nil, nil, nil
		}
		return successResult(item), rows, register, symbols
	default:
		return failureResult(item, domain.ItemOutcomeInvalid, "data_type", fmt.Errorf("unsupported data_type %q", item.DataType)), nil, nil, nil
	}
}

func isKlineItem(dataType string) bool {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "kline", "candles", "candle":
		return true
	default:
		return false
	}
}

func isSymbolItem(dataType string) bool {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "symbol", "symbols":
		return true
	default:
		return false
	}
}

func requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("MOOX_FETCH_REQUEST_TIMEOUT_MS")))
	if err != nil || value <= 0 {
		value = 2000
	}
	return context.WithTimeout(parent, time.Duration(value)*time.Millisecond)
}

func successResult(item domain.CollectionItem) domain.ItemResult {
	return domain.ItemResult{CollectionItem: item, Outcome: domain.ItemOutcomeSuccess}
}

func failureResult(item domain.CollectionItem, outcome domain.ItemOutcome, errorType string, err error) domain.ItemResult {
	return domain.ItemResult{CollectionItem: item, Outcome: outcome, ErrorType: errorType, ErrorSummary: truncateError(err)}
}

func buildCompletion(req Request, results []domain.ItemResult, completed time.Time, duration time.Duration) *marketfetchpb.MarketFetchBatchCompleted {
	payload := &marketfetchpb.MarketFetchBatchCompleted{BatchId: req.BatchID, ScheduleId: req.ScheduleID, BatchKind: string(req.BatchKind), DatasetId: req.DatasetID, Frequency: req.Frequency, Region: req.Region, NodeId: req.NodeID, RequestId: req.RequestID, PlannedCount: int32(len(results)), DurationMs: duration.Milliseconds(), CompletedAt: timestamppb.New(completed.UTC())}
	var firstError string
	for _, result := range results {
		if result.Outcome == domain.ItemOutcomeSuccess {
			payload.SuccessCount++
		} else {
			if isRetryable(result.Outcome) {
				payload.RetryCount++
			} else {
				payload.PermanentFailedCount++
			}
			if firstError == "" {
				firstError = result.ErrorSummary
			}
		}
		payload.Items = append(payload.Items, &marketfetchpb.MarketFetchItemResult{SubjectId: result.SubjectID, Symbol: result.Symbol, TargetDataTime: result.TargetDataTime, Outcome: string(result.Outcome), ErrorType: result.ErrorType, ErrorSummary: result.ErrorSummary, SourceEventId: result.SourceEventID, TaskId: result.TaskID})
	}
	switch {
	case payload.SuccessCount == payload.PlannedCount:
		payload.Status = "succeeded"
	case payload.SuccessCount > 0:
		payload.Status = "partial_failed"
	default:
		payload.Status = "failed"
	}
	payload.ErrorSummary = truncateString(firstError, 256)
	return payload
}

func classifyError(err error) domain.ItemOutcome {
	if err == nil {
		return domain.ItemOutcomeSuccess
	}
	var statusErr *httpclient.StatusError
	if errors.As(err, &statusErr) {
		switch {
		case statusErr.StatusCode == 429:
			return domain.ItemOutcomeHTTP429
		case statusErr.StatusCode >= 500:
			return domain.ItemOutcomeHTTP5xx
		default:
			return domain.ItemOutcomeInvalid
		}
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "429") {
		return domain.ItemOutcomeHTTP429
	}
	if strings.Contains(message, "500") || strings.Contains(message, "502") || strings.Contains(message, "503") || strings.Contains(message, "504") || strings.Contains(message, "5xx") {
		return domain.ItemOutcomeHTTP5xx
	}
	if strings.Contains(message, "deadline") || strings.Contains(message, "timeout") || strings.Contains(message, "connection") {
		return domain.ItemOutcomeNetworkError
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(message, "eof") {
		return domain.ItemOutcomeNetworkError
	}
	return domain.ItemOutcomeInvalid
}

func errorType(err error) string {
	if err == nil {
		return ""
	}
	return string(classifyError(err))
}

func isRetryable(outcome domain.ItemOutcome) bool {
	return outcome == domain.ItemOutcomeHTTP429 || outcome == domain.ItemOutcomeHTTP5xx || outcome == domain.ItemOutcomeNetworkError || outcome == domain.ItemOutcomeStorageError
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	return truncateString(err.Error(), 256)
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
