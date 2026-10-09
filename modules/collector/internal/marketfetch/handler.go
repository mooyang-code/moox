package marketfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

// Handler is the short-lived SCF action handler. Dependencies are built per
// invocation so there is no resident worker, timer, or job lease in SCF.
type Handler struct {
	// NewStorage 按市场类型和写入来源创建 Storage 客户端；生产环境经外部接入访问 Storage。
	NewStorage func(marketType, writeSource string) (Storage, error)
	Publish    func(context.Context, Request, proto.Message) error
	// TimerRuntimeClient 为空时经外部接入领取 Timer 批次。
	TimerRuntimeClient TimerRuntimeClient
	// Execute is a test seam for the timer entrypoint. Production leaves it nil
	// and uses the market-specific common pipeline; tests can prove the Timer
	// contract without making an external exchange request.
	Execute                func(context.Context, Request, Storage) (*marketfetchpb.MarketFetchBatchCompleted, error)
	NewMarketKlinePipeline func(Storage, string, marketdata.InstrumentType, string, string) (*KlinePipeline, error)
	NewCryptoKlinePipeline func(Storage, marketdata.InstrumentType) (*KlinePipeline, error)
	NewStockKlinePipeline  func(Storage) (*KlinePipeline, error)
	ResolveSourceID        func(string, string) string
	ResolveSymbol          SymbolResolver
	Now                    func() time.Time
	Reporter               ItemReporter
	CLSReserve             time.Duration
	Metrics                *Metrics
	MetricsReporter        MetricsReporter
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
	// several seconds on a cold path, so leave a bounded reserve for one
	// reconnect attempt after the initial connection attempt.
	// Two cold connects and ACKs (3s each), plus a 300ms retry backoff.
	completionPublishReserve   = 13 * time.Second
	completionConnectTimeout   = 3 * time.Second
	completionConnectAttempts  = 2
	defaultStorageTimeout      = 5 * time.Second
	instrumentNamesReadTimeout = 250 * time.Millisecond
	metricsResponseReserve     = 750 * time.Millisecond
)

func NewHandler() *Handler {
	return &Handler{NewStorage: func(market, writeSource string) (Storage, error) {
		gateway, err := scfGateway()
		if err != nil {
			return nil, err
		}
		return NewMarketStorageForMarket(gateway.ClientOptions(), market, writeSource)
	}, Publish: publishCompletion}
}

// HandleWithFunctionName binds an Invoke request to the function identity
// supplied by the Tencent runtime. Payload function_name is only a hint.
func (h *Handler) HandleWithFunctionName(ctx context.Context, event model.CloudFunctionEvent, functionName string) (*model.Response, error) {
	return h.handleWithFunctionName(ctx, event, true, functionName)
}

// HandleWithFunctionNameWithoutCompletion handles a direct validation or
// diagnostic invocation without publishing a scheduler completion event.
func (h *Handler) HandleWithFunctionNameWithoutCompletion(ctx context.Context, event model.CloudFunctionEvent, functionName string) (*model.Response, error) {
	return h.handleWithFunctionName(ctx, event, false, functionName)
}

func (h *Handler) HandleTimerAt(ctx context.Context, requestID, nodeID string, now time.Time) (*model.Response, error) {
	if h == nil {
		return nil, fmt.Errorf("market fetch handler is nil")
	}
	budgetCtx, cancel := executionContext(ctx)
	defer func() {
		h.reportMetrics(budgetCtx)
		cancel()
	}()
	invocation, err := TimerRequestFromEnv(requestID, nodeID, now)
	if err != nil {
		return &model.Response{Success: false, Message: err.Error(), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	runtimeClient := h.TimerRuntimeClient
	if runtimeClient == nil {
		gateway, gatewayErr := scfGateway()
		if gatewayErr != nil {
			return &model.Response{Success: false, Message: gatewayErr.Error(), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
		}
		runtimeClient = NewTimerRuntimeClient(gateway)
	}
	req, claimed, err := claimTimerRequest(budgetCtx, runtimeClient, invocation)
	if err != nil {
		return &model.Response{Success: false, Message: err.Error(), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	if !claimed {
		return &model.Response{Success: true, Message: "no_work", RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	if h.Publish == nil {
		return &model.Response{Success: false, Message: "completion publisher is not configured", RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	return h.handleRequest(budgetCtx, req, true)
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
	defer h.reportMetrics(ctx)
	return h.handleRequest(ctx, req, publish)
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

func (h *Handler) handleRequest(ctx context.Context, req Request, publish bool) (*model.Response, error) {
	if publish && h.Publish == nil {
		return nil, fmt.Errorf("completion publisher is not configured")
	}
	budgetCtx, cancel := executionContext(ctx)
	defer cancel()
	storage, err := h.NewStorage(req.MarketType, writeSourceForFunctionName(req.FunctionName))
	if err != nil {
		return nil, err
	}
	storageTimeout := time.Duration(envInt("MOOX_FETCH_STORAGE_TIMEOUT_MS", int(defaultStorageTimeout/time.Millisecond))) * time.Millisecond
	commitReserve, publishReserve := storageAndPublishReserves(storageTimeout, h.CLSReserve, publish)
	writeCtx, writeCancel := contextWithReserve(budgetCtx, commitReserve-storageTimeout)
	defer writeCancel()
	storage = &reservedDeadlineStorage{Storage: storage, parent: writeCtx, timeout: storageTimeout}
	var payload *marketfetchpb.MarketFetchBatchCompleted
	if h.Execute != nil {
		payload, err = h.Execute(budgetCtx, req, storage)
	} else if req.SpaceID == StockCNSpaceID &&
		(req.InstrumentType == "" || strings.EqualFold(req.InstrumentType, string(marketdata.InstrumentEquity))) {
		workCtx, workCancel := contextWithReserve(budgetCtx, commitReserve)
		defer workCancel()
		newPipeline := h.NewStockKlinePipeline
		if newPipeline == nil {
			return nil, fmt.Errorf("stock kline pipeline factory is not configured")
		}
		pipeline, pipelineErr := newPipeline(storage)
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		pipeline.DatasetID = strings.TrimSpace(req.DatasetID)
		pipeline.Now = h.Now
		pipeline.Metrics = h.Metrics
		payload, err = pipeline.Execute(workCtx, req)
	} else {
		marketID := firstNonEmptyString(req.MarketID, req.SpaceID)
		instrumentType := marketdata.InstrumentType(firstNonEmptyString(req.InstrumentType, req.MarketType))
		var (
			pipeline    *KlinePipeline
			pipelineErr error
		)
		if cryptoProduct, ok := cryptoKlineProduct(req.SpaceID, marketID, req.MarketType, instrumentType, req.DatasetID); ok {
			if h.NewCryptoKlinePipeline == nil {
				return nil, fmt.Errorf("crypto kline pipeline factory is not configured")
			}
			alignCryptoRequest(&req, cryptoProduct)
			pipeline, pipelineErr = h.NewCryptoKlinePipeline(storage, cryptoProduct)
		} else {
			if h.NewMarketKlinePipeline == nil {
				return nil, fmt.Errorf("market kline pipeline factory is not configured")
			}
			pipeline, pipelineErr = h.NewMarketKlinePipeline(storage, marketID, instrumentType, req.Provider, req.SourceID)
		}
		if pipelineErr != nil {
			return nil, pipelineErr
		}
		pipeline.DatasetID = strings.TrimSpace(req.DatasetID)
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
	reserve := h.CLSReserve
	if reserve < metricsResponseReserve {
		reserve = metricsResponseReserve
	}
	timeout := time.Duration(envInt("MOOX_METRICS_REPORT_TIMEOUT_MS", int(reserve/time.Millisecond))) * time.Millisecond
	if timeout > reserve {
		timeout = reserve
	}
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
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
	parent        context.Context
	timeout       time.Duration
	mu            sync.Mutex
	writeDeadline time.Time
}

var _ periodStorage = (*reservedDeadlineStorage)(nil)

func (s *reservedDeadlineStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	ctx, cancel := s.storageContext()
	defer cancel()
	return s.Storage.UpsertFields(ctx, rows)
}

func (s *reservedDeadlineStorage) UpsertFieldsWithSource(_ context.Context, rows []*storagepb.RowFieldUpsert, source string) error {
	ctx, cancel := s.storageContext()
	defer cancel()
	if storage, ok := s.Storage.(sourceStorage); ok {
		return storage.UpsertFieldsWithSource(ctx, rows, source)
	}
	return s.Storage.UpsertFields(ctx, rows)
}

func (s *reservedDeadlineStorage) EnsureDatasetPeriod(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	storage, err := s.periodStorage("EnsureDatasetPeriod")
	if err != nil {
		return domain.PeriodStorageState{}, err
	}
	ctx, cancel := s.storageContext()
	defer cancel()
	return storage.EnsureDatasetPeriod(ctx, expectation)
}

func (s *reservedDeadlineStorage) CommitTimeSeriesBatch(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, rows []*storagepb.TimeSeriesBatchRow, sourceEventID string) error {
	storage, err := s.periodStorage("CommitTimeSeriesBatch")
	if err != nil {
		return err
	}
	ctx, cancel := s.storageContext()
	defer cancel()
	return storage.CommitTimeSeriesBatch(ctx, expectation, rows, sourceEventID)
}

func (s *reservedDeadlineStorage) RecordDatasetPeriodFailures(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, indexes []uint32) ([]*storagepb.DatasetPeriodFailureResult, error) {
	storage, err := s.periodStorage("RecordDatasetPeriodFailures")
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.storageContext()
	defer cancel()
	return storage.RecordDatasetPeriodFailures(ctx, expectation, indexes)
}

func (s *reservedDeadlineStorage) GetDatasetPeriodStatus(_ context.Context, expectation *storagepb.DatasetPeriodExpectation) (domain.PeriodStorageState, error) {
	storage, err := s.periodStorage("GetDatasetPeriodStatus")
	if err != nil {
		return domain.PeriodStorageState{}, err
	}
	ctx, cancel := s.storageContext()
	defer cancel()
	return storage.GetDatasetPeriodStatus(ctx, expectation)
}

func (s *reservedDeadlineStorage) periodStorage(method string) (periodStorage, error) {
	storage, ok := s.Storage.(periodStorage)
	if !ok {
		return nil, fmt.Errorf("%s requires a Storage client with the complete period commit contract", method)
	}
	return storage, nil
}

func (s *reservedDeadlineStorage) ListInstrumentNames(ctx context.Context, spaceID string, subjectIDs []string) (map[string]string, error) {
	reader, ok := s.Storage.(instrumentNameReader)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, instrumentNamesReadTimeout)
	defer cancel()
	return reader.ListInstrumentNames(ctx, spaceID, subjectIDs)
}

func (s *reservedDeadlineStorage) storageContext() (context.Context, context.CancelFunc) {
	// Provider work has an earlier deadline than this reserved write phase.
	// Keep parent cancellation, but share one write deadline across all RPCs.
	parent := s.parent
	if parent == nil {
		parent = context.Background()
	}
	if s.timeout <= 0 {
		return context.WithCancel(parent)
	}
	s.mu.Lock()
	if s.writeDeadline.IsZero() {
		s.writeDeadline = time.Now().Add(s.timeout)
	}
	deadline := s.writeDeadline
	s.mu.Unlock()
	return context.WithDeadline(parent, deadline)
}

// storageAndPublishReserves keeps the Storage RPC's full configured timeout,
// then reserves a small fixed window to publish the completion event. Storage
// crosses SCF -> Gateway -> Storage Primary, so it must not share a timeout
// budget with EventBus publication.
func storageAndPublishReserves(storage time.Duration, cls time.Duration, completion bool) (commit, publish time.Duration) {
	if storage <= 0 {
		storage = defaultStorageTimeout
	}
	if cls < metricsResponseReserve {
		cls = metricsResponseReserve
	}
	if !completion {
		return storage + cls, 0
	}
	return storage + completionPublishReserve + cls, completionPublishReserve
}

func executionContext(parent context.Context) (context.Context, context.CancelFunc) {
	seconds := envInt("MOOX_FETCH_TIMEOUT_SECONDS", envInt("MOOX_MARKET_FETCH_TIMEOUT_SECONDS", tencent.CollectorTimerTimeoutSeconds))
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
	return publishCompletionWithClient(ctx, req, payload, func(ctx context.Context, config jetstream.Config) (completionClient, error) {
		return jetstream.Connect(ctx, config)
	}, 2*completionConnectTimeout, 300*time.Millisecond)
}

type completionClient interface {
	events.RawPublisher
	Close() error
}

func publishCompletionWithClient(ctx context.Context, req Request, payload proto.Message, connect func(context.Context, jetstream.Config) (completionClient, error), attemptTimeout, backoff time.Duration) error {
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
	config := marketFetchEventBusConfig("moox-collector-market-fetch")
	config.ConnectTimeout = completionConnectTimeout
	var lastErr error
	for attempt := 1; attempt <= completionConnectAttempts; attempt++ {
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		client, connectErr := connect(attemptCtx, config)
		if connectErr == nil {
			publisher, publisherErr := events.NewPublisher(client, registry)
			if publisherErr == nil {
				_, lastErr = publisher.Publish(attemptCtx, events.MarketFetchBatchCompleted, payload, events.PublishOptions{EventID: req.BatchID, OccurredAt: time.Now().UTC(), SpaceID: req.SpaceID, SubjectID: subjectID})
			} else {
				lastErr = publisherErr
			}
			_ = client.Close()
			if lastErr == nil {
				cancelAttempt()
				return nil
			}
		} else {
			lastErr = connectErr
		}
		cancelAttempt()
		if attempt < completionConnectAttempts {
			timer := time.NewTimer(backoff)
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

func alignCryptoRequest(req *Request, product marketdata.InstrumentType) {
	if req == nil {
		return
	}
	sourceID, marketType, instrumentType := "spot_http", "spot", string(marketdata.InstrumentSpot)
	if product == marketdata.InstrumentSwap {
		sourceID, marketType, instrumentType = "swap_http", "swap", string(marketdata.InstrumentSwap)
	}
	req.SourceID = sourceID
	req.MarketType = marketType
	req.InstrumentType = instrumentType
	for index := range req.Items {
		req.Items[index].SourceID = sourceID
		req.Items[index].MarketType = marketType
	}
}

func cryptoKlineProduct(spaceID, marketID, marketType string, instrumentType marketdata.InstrumentType, datasetID string) (marketdata.InstrumentType, bool) {
	if !strings.EqualFold(strings.TrimSpace(spaceID), "crypto") && !strings.EqualFold(strings.TrimSpace(marketID), "crypto") {
		return "", false
	}
	dataset := strings.ToLower(strings.TrimSpace(datasetID))
	if strings.EqualFold(strings.TrimSpace(marketType), "swap") || strings.EqualFold(string(instrumentType), string(marketdata.InstrumentSwap)) || strings.Contains(dataset, "_swap_") || strings.Contains(dataset, "swap_kline") {
		return marketdata.InstrumentSwap, true
	}
	return marketdata.InstrumentSpot, true
}
