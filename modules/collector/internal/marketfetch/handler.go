package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// Handler is the short-lived SCF action handler. Dependencies are built per
// invocation so there is no resident worker, timer, or job lease in SCF.
type Handler struct {
	NewStorage func(string, string, string) (Storage, error)
	Publish    func(context.Context, Request, proto.Message) error
	// Execute is a test seam for the timer entrypoint. Production leaves it nil
	// and uses the market-specific common pipeline; tests can prove the Timer
	// contract without making an external exchange request.
	Execute func(context.Context, Request, Storage) (*marketfetchpb.MarketFetchBatchCompleted, error)
	// NewInstrumentPipeline is the market-agnostic InstrumentPipeline factory.
	// The composition root selects the provider registry and metadata for the
	// requested market; the Handler owns neither provider protocol nor Storage
	// mutation details.
	NewInstrumentPipeline  func(InstrumentStorage, string, marketdata.ProductType) (*InstrumentPipeline, error)
	NewMarketKlinePipeline func(Storage, string, marketdata.InstrumentType, string, string) (*KlinePipeline, error)
	// NewCryptoInstrumentPipeline is kept as a narrow test seam for callers that
	// construct a crypto-only Handler. NewHandler uses NewInstrumentPipeline.
	NewCryptoInstrumentPipeline func(InstrumentStorage, marketdata.ProductType) (*InstrumentPipeline, error)
	NewStockKlinePipeline       func(Storage) (*KlinePipeline, error)
	NewCryptoKlinePipeline      func(Storage, marketdata.ProductType) (*KlinePipeline, error)
	Now                         func() time.Time
	Reporter                    ItemReporter
	CLSReserve                  time.Duration
	Metrics                     *Metrics
	MetricsReporter             MetricsReporter
}

// MetricsReporter is the common one-shot observability sink used by long-lived
// Collector and short-lived SCF runtimes. It is intentionally optional: market
// writes must not fail just because the monitoring bus is unavailable.
type MetricsReporter interface {
	Handle(context.Context) error
}

const (
	// A fresh SCF invocation establishes a TLS connection to EventBus before
	// publishing the only completion fact. The first connection can take
	// several seconds on a cold path, so leave a ten-second reserve and allow
	// one bounded reconnect attempt. Invoke functions use the longer instrument
	// timeout; Timer invocations do not publish completion events.
	completionPublishReserve  = 10 * time.Second
	completionConnectTimeout  = 4 * time.Second
	completionConnectAttempts = 2
	defaultStorageTimeout     = 5 * time.Second
	metricsResponseReserve    = 500 * time.Millisecond
)

func NewHandler() *Handler {
	return &Handler{NewStorage: func(target, market, writeSource string) (Storage, error) {
		return NewMarketStorageForMarket(target, market, writeSource)
	}, Publish: publishCompletion, NewInstrumentPipeline: NewMarketInstrumentPipeline, NewCryptoInstrumentPipeline: NewCryptoInstrumentPipeline,
		NewMarketKlinePipeline: NewMarketKlinePipeline, NewStockKlinePipeline: NewStockKlinePipeline, NewCryptoKlinePipeline: NewCryptoKlinePipeline}
}

func (h *Handler) Handle(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	return h.handleWithFunctionName(ctx, event, true, "")
}

// HandleWithFunctionName binds an Invoke request to the function identity
// supplied by the Tencent runtime. Payload function_name is only a hint.
func (h *Handler) HandleWithFunctionName(ctx context.Context, event model.CloudFunctionEvent, functionName string) (*model.Response, error) {
	return h.handleWithFunctionName(ctx, event, true, functionName)
}

// HandleWithFunctionNameWithoutCompletion handles a direct validation or
// diagnostic invocation without publishing a scheduler completion event.
// Timer-backed canaries use this path because their runtime deliberately has
// no EventBus material.
func (h *Handler) HandleWithFunctionNameWithoutCompletion(ctx context.Context, event model.CloudFunctionEvent, functionName string) (*model.Response, error) {
	return h.handleWithFunctionName(ctx, event, false, functionName)
}

// HandleTimer executes a Tencent Timer invocation. Timer work intentionally
// skips EventBus completion publication; Storage freshness and CLS are the
// runtime evidence, and the next timer naturally retries the latest bars.
func (h *Handler) HandleTimer(ctx context.Context, requestID, nodeID string) (*model.Response, error) {
	now := time.Now().UTC()
	if h != nil && h.Now != nil {
		now = h.Now().UTC()
	}
	return h.HandleTimerAt(ctx, requestID, nodeID, now)
}

func (h *Handler) HandleTimerAt(ctx context.Context, requestID, nodeID string, now time.Time) (*model.Response, error) {
	if h == nil {
		return nil, fmt.Errorf("market fetch handler is nil")
	}
	defer h.reportMetrics(ctx)
	req, storageTarget, err := TimerRequestFromEnv(requestID, nodeID, now)
	if err != nil {
		return &model.Response{Success: false, Message: err.Error(), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	return h.handleRequest(ctx, req, storageTarget, false)
}

func (h *Handler) handle(ctx context.Context, event model.CloudFunctionEvent, publish bool) (*model.Response, error) {
	return h.handleWithFunctionName(ctx, event, publish, "")
}

func (h *Handler) handleWithFunctionName(ctx context.Context, event model.CloudFunctionEvent, publish bool, runtimeFunctionName string) (*model.Response, error) {
	if h == nil {
		return nil, fmt.Errorf("market fetch handler is nil")
	}
	var req Request
	if err := decodeRequest(event.Data, &req); err != nil {
		return &model.Response{Success: false, Message: err.Error(), RequestID: event.RequestID, Timestamp: time.Now().UTC()}, nil
	}
	// Invoke functions do not necessarily receive the timer reconciler's
	// managed environment. When an invocation payload is old, retried, or was
	// produced by a caller that omitted DNSRoutes, use the SCF environment
	// snapshot as the primary route source and keep payload routes as a
	// secondary fallback. The HTTP client still falls back to the hostname when
	// every snapshot address fails.
	if envRoutes, routeErr := parseDNSRoutes(os.Getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON")); routeErr == nil && len(envRoutes) > 0 {
		req.DNSRoutes = mergeDNSRoutes(envRoutes, req.DNSRoutes)
		log.InfoContextf(ctx, "market_fetch_dns_snapshot_loaded source=scf_environment route_count=%d dns_hash=%s", len(envRoutes), strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_DNS_HASH")))
	}
	if req.RequestID == "" {
		req.RequestID = event.RequestID
	}
	// The runtime identity is authoritative. Keep the environment/payload
	// fallback only for direct library callers without a Tencent context.
	if runtimeFunctionName = strings.TrimSpace(runtimeFunctionName); runtimeFunctionName != "" {
		req.FunctionName = runtimeFunctionName
	} else if strings.TrimSpace(req.FunctionName) == "" {
		req.FunctionName = strings.TrimSpace(os.Getenv("MOOX_SCF_FUNCTION_NAME"))
	}
	if expectedSpaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); expectedSpaceID == "" {
		return &model.Response{Success: false, Message: "MOOX_SPACE_ID is required", RequestID: req.RequestID, Timestamp: time.Now().UTC()}, nil
	} else if req.SpaceID != expectedSpaceID {
		return &model.Response{Success: false, Message: fmt.Sprintf("market_fetch space_id %q does not match function space %q", req.SpaceID, expectedSpaceID), RequestID: req.RequestID, Timestamp: time.Now().UTC()}, nil
	}
	if req.Concurrency == 0 {
		req.Concurrency = envInt("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", envInt("MOOX_MARKET_FETCH_MAX_INFLIGHT", DefaultConcurrency))
	}
	storageTarget := strings.TrimSpace(event.StorageRPCGatewayTarget)
	if storageTarget == "" {
		return nil, fmt.Errorf("storage_rpc_gateway_target is required")
	}
	defer h.reportMetrics(ctx)
	return h.handleRequest(ctx, req, storageTarget, publish)
}

// mergeDNSRoutes puts the preferred route map first and appends unique
// fallback addresses from the secondary map. This keeps the environment
// snapshot authoritative while allowing an invocation payload captured just
// before a refresh to contribute an address if the environment is incomplete.
func mergeDNSRoutes(preferred, fallback map[string]sources.DNSResolution) map[string]sources.DNSResolution {
	if len(preferred) == 0 && len(fallback) == 0 {
		return nil
	}
	merged := make(map[string]sources.DNSResolution, len(preferred)+len(fallback))
	add := func(routes map[string]sources.DNSResolution) {
		for rawHost, route := range routes {
			host := sources.NormalizeDNSHost(rawHost)
			if host == "" {
				continue
			}
			current := merged[host]
			seen := make(map[string]struct{}, len(current.IPs)+len(route.IPs))
			for _, ip := range current.IPs {
				seen[ip] = struct{}{}
			}
			for _, ip := range route.IPs {
				if _, exists := seen[ip]; exists {
					continue
				}
				current.IPs = append(current.IPs, ip)
				seen[ip] = struct{}{}
			}
			if current.ResolvedAt.IsZero() {
				current.ResolvedAt = route.ResolvedAt
			}
			if len(current.LatencyMS) == 0 && len(route.LatencyMS) > 0 {
				current.LatencyMS = make(map[string]uint32, len(route.LatencyMS))
				for ip, latency := range route.LatencyMS {
					current.LatencyMS[ip] = latency
				}
			}
			merged[host] = current
		}
	}
	add(preferred)
	add(fallback)
	return merged
}

func (h *Handler) handleRequest(ctx context.Context, req Request, storageTarget string, publish bool) (*model.Response, error) {
	storageTarget = strings.TrimSpace(storageTarget)
	if storageTarget == "" {
		return nil, fmt.Errorf("storage_rpc_gateway_target is required")
	}
	budgetCtx, cancel := executionContext(ctx)
	defer cancel()
	storage, err := h.NewStorage(storageTarget, req.MarketType, writeSourceForFunctionName(req.FunctionName))
	if err != nil {
		return nil, err
	}
	storageTimeout := time.Duration(envInt("MOOX_FETCH_STORAGE_TIMEOUT_MS", int(defaultStorageTimeout/time.Millisecond))) * time.Millisecond
	commitReserve, publishReserve := storageAndPublishReserves(storageTimeout, h.CLSReserve, publish)
	var payload *marketfetchpb.MarketFetchBatchCompleted
	if h.Execute != nil {
		payload, err = h.Execute(budgetCtx, req, storage)
	} else if req.BatchKind == domain.BatchKindInstrumentSnapshot && (h.NewInstrumentPipeline != nil || h.NewCryptoInstrumentPipeline != nil) {
		if len(req.Items) != 1 {
			return nil, fmt.Errorf("instrument snapshot requires exactly one item")
		}
		instrumentStorage, ok := storage.(InstrumentStorage)
		if !ok {
			return nil, fmt.Errorf("instrument snapshot storage does not support active-set operations")
		}
		productType := marketdata.ProductType(strings.ToLower(strings.TrimSpace(req.MarketType)))
		var pipeline *InstrumentPipeline
		var pipelineErr error
		if h.NewInstrumentPipeline != nil {
			pipeline, pipelineErr = h.NewInstrumentPipeline(instrumentStorage, req.SpaceID, productType)
		} else {
			pipeline, pipelineErr = h.NewCryptoInstrumentPipeline(instrumentStorage, productType)
		}
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		pipeline.Metrics = h.Metrics
		snapshotAt := time.Now().UTC()
		if h.Now != nil {
			snapshotAt = h.Now().UTC()
		}
		if rawSnapshotAt := strings.TrimSpace(req.Items[0].SnapshotAt); rawSnapshotAt != "" {
			parsed, parseErr := time.Parse(time.RFC3339Nano, rawSnapshotAt)
			if parseErr != nil {
				return nil, fmt.Errorf("parse instrument snapshot_at: %w", parseErr)
			}
			snapshotAt = parsed.UTC()
		}
		startedAt := time.Now()
		_, pipelineErr = pipeline.Execute(budgetCtx, InstrumentPipelineRequest{
			RequestID: req.RequestID, SnapshotAt: snapshotAt,
			SnapshotShardIndex: req.Items[0].SnapshotShardIndex,
			SnapshotShardCount: req.Items[0].SnapshotShardCount,
		})
		if pipelineErr != nil {
			result := failureResult(req.Items[0], domain.ItemOutcomeStorageError, "instrument_snapshot", pipelineErr)
			payload = buildCompletion(req, []domain.ItemResult{result}, snapshotAt, time.Since(startedAt))
		} else {
			payload = buildCompletion(req, []domain.ItemResult{successResult(req.Items[0])}, snapshotAt, time.Since(startedAt))
		}
	} else if req.SpaceID == StockCNSpaceID &&
		(req.InstrumentType == "" || strings.EqualFold(req.InstrumentType, string(marketdata.InstrumentEquity))) {
		workCtx, workCancel := contextWithReserve(budgetCtx, commitReserve)
		defer workCancel()
		stockStorage := &reservedDeadlineStorage{Storage: storage, parent: budgetCtx, timeout: storageTimeout}
		newPipeline := h.NewStockKlinePipeline
		if newPipeline == nil {
			newPipeline = NewStockKlinePipeline
		}
		pipeline, pipelineErr := newPipeline(stockStorage)
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		pipeline.Now = h.Now
		pipeline.Metrics = h.Metrics
		payload, err = pipeline.Execute(workCtx, req)
	} else if strings.EqualFold(strings.TrimSpace(req.SpaceID), "crypto") ||
		strings.EqualFold(strings.TrimSpace(req.MarketType), "spot") ||
		strings.EqualFold(strings.TrimSpace(req.MarketType), "swap") {
		newPipeline := h.NewCryptoKlinePipeline
		if newPipeline == nil {
			newPipeline = NewCryptoKlinePipeline
		}
		pipeline, pipelineErr := newPipeline(storage, marketdata.ProductType(strings.ToLower(strings.TrimSpace(req.MarketType))))
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		pipeline.Now = h.Now
		pipeline.Metrics = h.Metrics
		workCtx, workCancel := contextWithReserve(budgetCtx, commitReserve)
		defer workCancel()
		payload, err = pipeline.Execute(workCtx, req)
	} else {
		if h.NewMarketKlinePipeline == nil {
			return nil, fmt.Errorf("market kline pipeline factory is not configured")
		}
		marketID := firstNonEmptyString(req.MarketID, req.SpaceID)
		instrumentType := marketdata.InstrumentType(firstNonEmptyString(req.InstrumentType, req.MarketType))
		pipeline, pipelineErr := h.NewMarketKlinePipeline(storage, marketID, instrumentType, req.Provider, req.SourceID)
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		pipeline.Now = h.Now
		pipeline.Metrics = h.Metrics
		workCtx, workCancel := contextWithReserve(budgetCtx, commitReserve)
		defer workCancel()
		payload, err = pipeline.Execute(workCtx, req)
	}
	if err != nil {
		return nil, err
	}
	if publish {
		publishCtx, publishCancel := context.WithTimeout(budgetCtx, publishReserve)
		defer publishCancel()
		if err := h.Publish(publishCtx, req, payload); err != nil {
			return nil, fmt.Errorf("publish market fetch completion: %w", err)
		}
	}
	return &model.Response{Success: payload.GetStatus() == "succeeded" || payload.GetStatus() == "partial_failed", Message: payload.GetStatus(), Data: payload, RequestID: req.RequestID, Timestamp: time.Now().UTC()}, nil
}

func (h *Handler) reportMetrics(parent context.Context) {
	if h == nil || h.MetricsReporter == nil {
		return
	}
	timeout := time.Duration(envInt("MOOX_METRICS_REPORT_TIMEOUT_MS", 750)) * time.Millisecond
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline) - metricsResponseReserve
		if remaining <= 0 {
			return
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	if err := h.MetricsReporter.Handle(ctx); err != nil {
		log.WarnContextf(parent, "market_fetch_metrics_report_failed: %v", err)
	}
}

func contextWithReserve(parent context.Context, reserve time.Duration) (context.Context, context.CancelFunc) {
	if reserve <= 0 {
		return context.WithCancel(parent)
	}
	if deadline, ok := parent.Deadline(); ok {
		workDeadline := deadline.Add(-reserve)
		if workDeadline.Before(time.Now()) {
			workDeadline = time.Now().Add(time.Millisecond)
		}
		return context.WithDeadline(parent, workDeadline)
	}
	return context.WithTimeout(parent, 8*time.Second)
}

type reservedDeadlineStorage struct {
	Storage
	parent  context.Context
	timeout time.Duration
}

func (s *reservedDeadlineStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	ctx, cancel := s.storageContext()
	defer cancel()
	return s.Storage.UpsertFields(ctx, rows)
}

func (s *reservedDeadlineStorage) RegisterDataSubject(_ context.Context, req *storagepb.RegisterDataSubjectReq) error {
	ctx, cancel := s.storageContext()
	defer cancel()
	return s.Storage.RegisterDataSubject(ctx, req)
}

func (s *reservedDeadlineStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	ctx, cancel := s.storageContext()
	defer cancel()
	if storage, ok := s.Storage.(sourceStorage); ok {
		return storage.UpsertFieldsWithSource(ctx, rows, source)
	}
	return s.Storage.UpsertFields(ctx, rows)
}

func (s *reservedDeadlineStorage) ListInstrumentNames(_ context.Context, spaceID string, subjectIDs []string) (map[string]string, error) {
	reader, ok := s.Storage.(instrumentNameReader)
	if !ok {
		return nil, nil
	}
	ctx, cancel := s.storageContext()
	defer cancel()
	return reader.ListInstrumentNames(ctx, spaceID, subjectIDs)
}

func (s *reservedDeadlineStorage) storageContext() (context.Context, context.CancelFunc) {
	parent := s.parent
	if parent == nil {
		parent = context.Background()
	}
	if s.timeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, s.timeout)
}

// storageAndPublishReserves keeps the Storage RPC's full configured timeout,
// then reserves a small fixed window to publish the completion event. Storage
// crosses SCF -> Gateway -> Storage Primary, so it must not share a timeout
// budget with EventBus publication.
func storageAndPublishReserves(storage time.Duration, cls time.Duration, completion bool) (commit, publish time.Duration) {
	if storage <= 0 {
		storage = defaultStorageTimeout
	}
	if cls < 0 {
		cls = 0
	}
	if !completion {
		// Timer invocations do not publish a completion event. Do not reserve
		// three seconds for a path that is deliberately absent; that budget is
		// needed by the 30-symbol HTTP fan-out before the five-second Storage
		// write and best-effort CLS flush.
		return storage + cls, 0
	}
	return storage + completionPublishReserve + cls, completionPublishReserve
}

func executionContext(parent context.Context) (context.Context, context.CancelFunc) {
	seconds := envInt("MOOX_FETCH_TIMEOUT_SECONDS", envInt("MOOX_MARKET_FETCH_TIMEOUT_SECONDS", 15))
	budget := time.Duration(seconds) * time.Second
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		budget = time.Millisecond
	}
	return context.WithTimeout(parent, budget)
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func envIntAllowZero(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func decodeRequest(data map[string]interface{}, request *Request) error {
	if len(data) == 0 {
		return fmt.Errorf("market_fetch data is required")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode market_fetch data: %w", err)
	}
	if len(raw) > 128*1024 {
		return fmt.Errorf("market_fetch data exceeds 128KB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return fmt.Errorf("decode market_fetch data: %w", err)
	}
	return request.validate()
}

func publishCompletion(ctx context.Context, req Request, payload proto.Message) error {
	if strings.TrimSpace(req.SpaceID) == "" {
		return fmt.Errorf("space_id is required")
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		return err
	}
	subjectID := strings.TrimSpace(req.DatasetID)
	if subjectID == "" {
		subjectID = req.BatchID
	}
	config := jetstream.ConfigFromEnv(nil, "moox-collector-market-fetch")
	config.ConnectTimeout = completionConnectTimeout
	var lastErr error
	for attempt := 1; attempt <= completionConnectAttempts; attempt++ {
		client, connectErr := jetstream.Connect(ctx, config)
		if connectErr == nil {
			publisher, publisherErr := events.NewPublisher(client, registry)
			if publisherErr == nil {
				_, lastErr = publisher.Publish(ctx, events.MarketFetchBatchCompleted, payload, events.PublishOptions{EventID: req.BatchID, OccurredAt: time.Now().UTC(), SpaceID: req.SpaceID, SubjectID: subjectID})
			} else {
				lastErr = publisherErr
			}
			_ = client.Close()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = connectErr
		}
		if attempt < completionConnectAttempts {
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr != nil {
		log.ErrorContextf(ctx, "publish market fetch completion failed: batch_id=%s err=%v", req.BatchID, lastErr)
	}
	return lastErr
}
