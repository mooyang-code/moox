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
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/clsreporter"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	DefaultConcurrency = 5
	MaxConcurrency     = 64
	// MaxRealtimeItems bounds the work accepted by one short-lived SCF. The
	// Collector assigns at most 30 symbols to one Timer function so the
	// fifteen-second budget remains usable with a bounded HTTP fan-out.
	MaxRealtimeItems = 30
	MaxRealtimeRows  = 3
)

// Request is the JSON payload accepted by a market_fetch SCF invocation.
type Request struct {
	BatchID string `json:"batch_id"`
	// SyncPointID is the stable logical catchup fence identity. Retry batches
	// get a new BatchID for outbox/write idempotency, but must keep the
	// original request ID so a later Recalc can wait on the same fence.
	SyncPointID  string                           `json:"sync_point_id,omitempty"`
	ScheduleID   string                           `json:"schedule_id,omitempty"`
	BatchKind    domain.BatchKind                 `json:"batch_kind"`
	SpaceID      string                           `json:"space_id"`
	DatasetID    string                           `json:"dataset_id,omitempty"`
	Frequency    string                           `json:"frequency,omitempty"`
	Provider     string                           `json:"provider"`
	MarketType   string                           `json:"market_type"`
	Region       string                           `json:"region"`
	NodeID       string                           `json:"node_id"`
	FunctionName string                           `json:"function_name,omitempty"`
	RequestID    string                           `json:"request_id,omitempty"`
	ShardIndex   int                              `json:"shard_index,omitempty"`
	Concurrency  int                              `json:"concurrency,omitempty"`
	DNSRoutes    map[string]sources.DNSResolution `json:"dns_routes,omitempty"`
	Items        []domain.CollectionItem          `json:"items"`
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
	case domain.BatchKindCatchup, domain.BatchKindBackfill, domain.BatchKindGapRepair, domain.BatchKindSymbolSnapshot:
		maxItems = 1
	default:
		return fmt.Errorf("unsupported batch_kind %q", r.BatchKind)
	}
	if len(r.Items) > maxItems && !(r.BatchKind == domain.BatchKindRealtime && strings.EqualFold(r.SpaceID, StockCNSpaceID)) {
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
		if isHistoricalBatchKind(r.BatchKind) {
			if strings.TrimSpace(item.StartTime) == "" || item.BarLimit <= 0 || item.BarLimit > 1000 {
				return fmt.Errorf("historical item requires start_time and bar_limit 1..1000")
			}
		} else if r.BatchKind == domain.BatchKindRealtime && item.BarLimit > MaxRealtimeRows {
			return fmt.Errorf("realtime bar_limit must be between 1 and %d", MaxRealtimeRows)
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

type syncPointStorage interface {
	AppendDatasetSyncPoint(context.Context, string, string, string, string) error
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
		FetchSymbolSnapshot(context.Context, *sources.CollectParams) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, string, error)
	}
	Storage Storage
	Now     func() time.Time
	// CommitReserve is the total tail budget reserved for Storage plus the
	// completion publish. StorageReserve is the portion used by the aggregate
	// Storage write; keeping the two separate prevents publish from starting
	// after the invocation deadline has already expired.
	CommitReserve  time.Duration
	StorageReserve time.Duration
	Reporter       ItemReporter
}

// ItemReporter receives final per-item outcomes. It is intentionally tiny so
// the executor can run outside SCF while the short-lived handler supplies CLS.
type ItemReporter interface{ Report(clsreporter.Entry) }

type execution struct {
	item  domain.CollectionItem
	rows  []*storagepb.RowFieldUpsert
	items []*exchangeItem
}

type itemExecution struct {
	rows          []*storagepb.RowFieldUpsert
	registrations []*storagepb.RegisterDataSubjectReq
	symbols       []*exchange.SymbolInfo
	elapsed       time.Duration
}

// exchangeItem keeps symbol metadata private to this package while allowing
// the symbol path to perform metadata registration after the aggregate row
// write.
type exchangeItem struct {
	register []*storagepb.RegisterDataSubjectReq
}

// Execute fetches all items concurrently, writes all successful rows once with
// a bounded Storage retry, then returns a completion payload. Remaining
// retryable outcomes are handed back to the scheduler in the event payload.
func (e *Executor) Execute(ctx context.Context, req Request) (*marketfetchpb.MarketFetchBatchCompleted, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	if e == nil || e.Klines == nil || e.Symbols == nil || e.Storage == nil {
		return nil, fmt.Errorf("market fetch executor is not initialized")
	}
	runtimeExecutor, err := e.withCryptoRuntime(req)
	if err != nil {
		return nil, err
	}
	e = runtimeExecutor
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
	executions := make([]itemExecution, len(req.Items))
	concurrency := req.Concurrency
	if concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	if concurrency > len(req.Items) {
		concurrency = len(req.Items)
	}
	for start := 0; start < len(req.Items); start += concurrency {
		end := min(start+concurrency, len(req.Items))
		handlers := make([]func() error, 0, end-start)
		for index := start; index < end; index++ {
			index, item := index, req.Items[index]
			handlers = append(handlers, func() error {
				if err := workCtx.Err(); err != nil {
					results[index] = failureResult(item, domain.ItemOutcomeNetworkError, "deadline_exhausted", err)
					return nil
				}
				itemStarted := time.Now()
				result, itemRows, itemRegs, itemSymbols := e.executeItem(workCtx, req, item)
				results[index] = result
				executions[index] = itemExecution{rows: itemRows, registrations: itemRegs, symbols: itemSymbols, elapsed: time.Since(itemStarted)}
				return nil
			})
		}
		if err := trpc.GoAndWait(handlers...); err != nil {
			return nil, err
		}
	}
	rows := make([]*storagepb.RowFieldUpsert, 0)
	registrations := make([]*storagepb.RegisterDataSubjectReq, 0)
	rowCountByItem := make([]int, len(req.Items))
	symbolsByItem := make([][]*exchange.SymbolInfo, len(req.Items))
	for index := range executions {
		rows = append(rows, executions[index].rows...)
		registrations = append(registrations, executions[index].registrations...)
		rowCountByItem[index] = len(executions[index].rows)
		symbolsByItem[index] = executions[index].symbols
	}

	// A single idempotent Storage write is used for the complete successful row
	// set. Its bounded internal retries preserve that atomic batch outcome while
	// tolerating a transient Gateway response timeout.
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
				// CLS rows measures rows committed to Storage, not rows merely
				// returned by the upstream exchange.
				rowCountByItem[index] = 0
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
		if isHistoricalBatchKind(req.BatchKind) {
			// A catchup fence is meaningful only after at least one row from
			// every item was committed. Empty or failed upstream results must
			// remain retryable and cannot make View report imported data ready.
			committed := len(rows) > 0
			for index, result := range results {
				if result.Outcome != domain.ItemOutcomeSuccess || rowCountByItem[index] == 0 {
					committed = false
					break
				}
			}
			if committed {
				syncStorage, ok := e.Storage.(syncPointStorage)
				if !ok {
					return nil, fmt.Errorf("historical storage does not support dataset sync points")
				}
				syncPointID := strings.TrimSpace(req.SyncPointID)
				if syncPointID == "" {
					syncPointID = req.BatchID
				}
				if err := syncStorage.AppendDatasetSyncPoint(commitCtx, req.SpaceID, req.DatasetID, syncPointID, string(req.BatchKind)); err != nil {
					return nil, fmt.Errorf("append historical dataset sync point: %w", err)
				}
			}
		}
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
				// Each exchange snapshot shard sees only part of the catalogue.
				// Item zero retains the complete exchange list specifically for
				// reconciliation; all other shards must only activate their slice.
				if req.Items[index].SnapshotShardCount > 1 && req.Items[index].SnapshotShardIndex != 0 {
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
		log.WarnContextf(ctx, "market_fetch_result batch_id=%s status=%s error=%q results=%s", req.BatchID, payload.GetStatus(), resultErrorSummary(results), compactResultOutcomes(results))
	}
	e.reportResults(req, results, executions, rowCountByItem)
	return payload, nil
}

// withCryptoRuntime upgrades the concrete dependencies created by the legacy
// Handler factory into the common provider composition. Tests and non-Binance
// callers keep their explicitly injected fetchers unchanged.
func (e *Executor) withCryptoRuntime(req Request) (*Executor, error) {
	if e == nil || !strings.EqualFold(strings.TrimSpace(req.SpaceID), "crypto_market") {
		return e, nil
	}
	klines, ok := e.Klines.(*binance.KlineCollector)
	if !ok {
		return e, nil
	}
	symbols, ok := e.Symbols.(*binance.SymbolCollector)
	if !ok {
		return e, nil
	}
	productType := marketdata.ProductType(strings.ToLower(strings.TrimSpace(req.MarketType)))
	pipeline, err := binance.NewRuntimePipeline(productType, klines, symbols)
	if err != nil {
		return nil, err
	}
	clone := *e
	clone.Klines = pipeline
	clone.Symbols = pipeline
	if catchup, ok := e.Catchup.(*binance.KlineCollector); ok {
		catchupPipeline, pipelineErr := binance.NewRuntimePipeline(productType, catchup, symbols)
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		clone.Catchup = catchupPipeline
	}
	return &clone, nil
}

func (e *Executor) reportResults(req Request, results []domain.ItemResult, executions []itemExecution, rowCountByItem []int) {
	if e == nil || e.Reporter == nil {
		return
	}
	for index, result := range results {
		fields := map[string]string{
			"event_type": "market_fetch_item", "batch_id": req.BatchID, "space_id": req.SpaceID,
			"request_id": req.RequestID, "region": req.Region, "function_node_id": req.NodeID, "symbol": result.Symbol,
			"dataset_id": result.DatasetID, "frequency": result.Frequency, "success": strconv.FormatBool(result.Outcome == domain.ItemOutcomeSuccess),
			"error_kind": result.ErrorType, "error_message": truncateErrorString(result.ErrorSummary),
			"elapsed_ms": strconv.FormatInt(executions[index].elapsed.Milliseconds(), 10), "rows": strconv.Itoa(rowCountByItem[index]), "latest_data_time": result.TargetDataTime,
		}
		for key, value := range dnsReportFields(req) {
			fields[key] = value
		}
		e.Reporter.Report(clsreporter.Entry{Timestamp: time.Now().UTC(), Fields: fields})
	}
}

func dnsReportFields(req Request) map[string]string {
	fields := map[string]string{"dns_source": "system", "dns_route_count": "0"}
	host := "api.binance.com"
	if strings.EqualFold(strings.TrimSpace(req.MarketType), "swap") {
		host = "fapi.binance.com"
	}
	var route sources.DNSResolution
	found := false
	for rawHost, candidate := range req.DNSRoutes {
		if sources.NormalizeDNSHost(rawHost) == host {
			route, found = candidate, true
			break
		}
	}
	if !found || len(route.IPs) == 0 {
		return fields
	}
	fields["dns_source"] = "scf_snapshot"
	fields["dns_route_count"] = strconv.Itoa(len(route.IPs))
	if !route.ResolvedAt.IsZero() {
		age := time.Since(route.ResolvedAt.UTC()).Seconds()
		if age < 0 {
			age = 0
		}
		fields["dns_route_age_seconds"] = strconv.FormatInt(int64(age), 10)
	}
	return fields
}

func resultErrorSummary(results []domain.ItemResult) string {
	for _, result := range results {
		if summary := strings.TrimSpace(result.ErrorSummary); summary != "" {
			return summary
		}
	}
	return ""
}

func truncateErrorString(value string) string {
	if len(value) <= 512 {
		return value
	}
	return value[:512]
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
// Each full exchange snapshot invokes this path once per refresh.
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
	params := &sources.CollectParams{SpaceID: req.SpaceID, DatasetID: item.DatasetID, InstType: instType, Symbol: item.Symbol, SubjectID: item.SubjectID, Interval: item.Frequency, Live: req.BatchKind == domain.BatchKindRealtime, DNSRoutes: req.DNSRoutes}
	if isHistoricalBatchKind(req.BatchKind) {
		if e.Catchup == nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "history", fmt.Errorf("historical collector is not initialized")), nil, nil, nil
		}
		start, parseErr := time.Parse(time.RFC3339Nano, item.StartTime)
		if parseErr != nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "start_time", parseErr), nil, nil, nil
		}
		log.InfoContextf(ctx, "market_fetch_kline_start batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=%s", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, req.BatchKind)
		fetchStarted := time.Now()
		rows, latest, fetchErr := e.Catchup.FetchCatchupRows(ctx, params, start, item.BarLimit)
		elapsedMS := time.Since(fetchStarted).Milliseconds()
		if fetchErr != nil {
			log.WarnContextf(ctx, "market_fetch_kline_failed batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=%s elapsed_ms=%d success=false error=%v", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, req.BatchKind, elapsedMS, fetchErr)
			return failureResult(item, classifyError(fetchErr), errorType(fetchErr), fetchErr), nil, nil, nil
		}
		if len(rows) == 0 || latest.IsZero() {
			emptyErr := fmt.Errorf("Binance returned no closed bars")
			log.WarnContextf(ctx, "market_fetch_kline_failed batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=%s elapsed_ms=%d success=false error=%v", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, req.BatchKind, elapsedMS, emptyErr)
			return failureResult(item, domain.ItemOutcomeStorageError, "empty_data", emptyErr), nil, nil, nil
		}
		item.TargetDataTime = latest.UTC().Format(time.RFC3339Nano)
		log.InfoContextf(ctx, "market_fetch_kline_success batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=%s elapsed_ms=%d success=true rows=%d latest=%s", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, req.BatchKind, elapsedMS, len(rows), latest.UTC().Format(time.RFC3339Nano))
		return successResult(item), rows, nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(item.DataType)) {
	case "kline", "candles", "candle":
		limit := item.BarLimit
		if limit <= 0 {
			limit = MaxRealtimeRows
		}
		log.InfoContextf(ctx, "market_fetch_kline_start batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=realtime", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency)
		fetchStarted := time.Now()
		rows, latest, err := e.Klines.FetchRealtimeRows(ctx, params, limit)
		elapsedMS := time.Since(fetchStarted).Milliseconds()
		if err != nil {
			log.WarnContextf(ctx, "market_fetch_kline_failed batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=realtime elapsed_ms=%d success=false error=%v", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, elapsedMS, err)
			return failureResult(item, classifyError(err), errorType(err), err), nil, nil, nil
		}
		if len(rows) == 0 || latest.IsZero() {
			emptyErr := fmt.Errorf("Binance returned no closed bars")
			log.WarnContextf(ctx, "market_fetch_kline_failed batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=realtime elapsed_ms=%d success=false error=%v", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, elapsedMS, emptyErr)
			return failureResult(item, domain.ItemOutcomeStorageError, "empty_data", emptyErr), nil, nil, nil
		}
		if target, parseErr := time.Parse(time.RFC3339Nano, item.TargetDataTime); item.TargetDataTime != "" && parseErr == nil && latest.Before(target) {
			staleErr := fmt.Errorf("latest closed bar %s is older than target %s", latest.UTC().Format(time.RFC3339Nano), target.UTC().Format(time.RFC3339Nano))
			log.WarnContextf(ctx, "market_fetch_kline_failed batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=realtime elapsed_ms=%d success=false error=%v", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, elapsedMS, staleErr)
			return failureResult(item, domain.ItemOutcomeStorageError, "stale_data", staleErr), nil, nil, nil
		}
		item.TargetDataTime = latest.UTC().Format(time.RFC3339Nano)
		log.InfoContextf(ctx, "market_fetch_kline_success batch_id=%s subject_id=%s symbol=%s dataset_id=%s frequency=%s kind=realtime elapsed_ms=%d success=true rows=%d latest=%s", req.BatchID, item.SubjectID, item.Symbol, item.DatasetID, item.Frequency, elapsedMS, len(rows), latest.UTC().Format(time.RFC3339Nano))
		return successResult(item), rows, nil, nil
	case "symbol", "symbols":
		requestCtx, cancel := symbolRequestContext(ctx, true)
		defer cancel()
		rows, symbols, _, err := e.Symbols.FetchSymbolSnapshot(requestCtx, params)
		if err != nil {
			return failureResult(item, classifyError(err), errorType(err), err), nil, nil, nil
		}
		shardRows, shardSymbols, err := selectSymbolSnapshotShard(rows, symbols, item.SnapshotShardIndex, item.SnapshotShardCount)
		if err != nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "symbol_shard", err), nil, nil, nil
		}
		register, err := binance.BuildSymbolRegisterRequests(req.SpaceID, item.DatasetID, instType, shardSymbols)
		if err != nil {
			return failureResult(item, domain.ItemOutcomeInvalid, "metadata", err), nil, nil, nil
		}
		if item.SnapshotShardCount > 0 && item.SnapshotShardIndex != 0 {
			symbols = nil
		}
		return successResult(item), shardRows, register, symbols
	default:
		return failureResult(item, domain.ItemOutcomeInvalid, "data_type", fmt.Errorf("unsupported data_type %q", item.DataType)), nil, nil, nil
	}
}

func isHistoricalBatchKind(kind domain.BatchKind) bool {
	switch kind {
	case domain.BatchKindCatchup, domain.BatchKindBackfill, domain.BatchKindGapRepair:
		return true
	default:
		return false
	}
}

func selectSymbolSnapshotShard(rows []*storagepb.RowFieldUpsert, symbols []*exchange.SymbolInfo, index, count int) ([]*storagepb.RowFieldUpsert, []*exchange.SymbolInfo, error) {
	if count <= 0 {
		return rows, symbols, nil
	}
	if index < 0 || index >= count {
		return nil, nil, fmt.Errorf("symbol snapshot shard %d is outside [0,%d)", index, count)
	}
	if len(rows) != len(symbols) {
		return nil, nil, fmt.Errorf("symbol snapshot rows (%d) do not match symbols (%d)", len(rows), len(symbols))
	}
	start := len(symbols) * index / count
	end := len(symbols) * (index + 1) / count
	return rows[start:end], symbols[start:end], nil
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
	return context.WithTimeout(parent, requestTimeout("MOOX_FETCH_REQUEST_TIMEOUT_MS", 2000))
}

// symbolRequestContext gives the complete ExchangeInfo response its own
// bounded timeout. The normal 2-second K-line timeout is too short to read
// Binance's full active-symbol catalogue from an overseas SCF.
func symbolRequestContext(parent context.Context, fullSnapshot bool) (context.Context, context.CancelFunc) {
	if fullSnapshot {
		return context.WithTimeout(parent, requestTimeout("MOOX_FETCH_SYMBOL_SNAPSHOT_TIMEOUT_MS", 5000))
	}
	return requestContext(parent)
}

func requestTimeout(name string, fallback int) time.Duration {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Millisecond
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
	if errors.Is(err, marketdata.ErrRateLimited) {
		return domain.ItemOutcomeHTTP429
	}
	if errors.Is(err, marketdata.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return domain.ItemOutcomeNetworkError
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
