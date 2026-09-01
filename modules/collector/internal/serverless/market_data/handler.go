// Package marketdata contains the generic, one-shot market-data SCF entrypoint.
// A request names a canonical Market/Instrument and a concrete SourceKey, then
// the shared pipeline owns normalization and Storage writes for stocks, crypto,
// indices, and bonds.
package marketdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/routeprobe"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const defaultAction = model.EventActionMarketFetch

const (
	// CloudNode owns the shared Timer trigger contract for every Collector
	// fleet. Market identity is carried by the static environment, not by a
	// second trigger name/message that CloudNode could not reconcile.
	timerTriggerName    = "moox-market-fetch-timer"
	timerTriggerMessage = "market_fetch_timer_v1"
)

type Item struct {
	SubjectID      string `json:"subject_id"`
	ProviderSymbol string `json:"provider_symbol"`
	TaskID         string `json:"task_id,omitempty"`
	SourceEventID  string `json:"source_event_id,omitempty"`
}

// Request is the public event data contract for stock/index/bond SCF calls.
// ProviderID and SourceID are optional only when the canonical default source
// for the requested market is used; callers cannot substitute an arbitrary
// provider after composition has been built.
type Request struct {
	SpaceID            string `json:"space_id"`
	BatchID            string `json:"batch_id,omitempty"`
	DatasetID          string `json:"dataset_id"`
	MarketID           string `json:"market_id"`
	InstrumentType     string `json:"instrument_type"`
	ProviderID         string `json:"provider_id,omitempty"`
	SourceID           string `json:"source_id,omitempty"`
	SeriesTag          string `json:"series_tag,omitempty"`
	DataType           string `json:"data_type,omitempty"`
	BatchKind          string `json:"batch_kind,omitempty"`
	ScheduleID         string `json:"schedule_id,omitempty"`
	SourceEventID      string `json:"source_event_id"`
	Frequency          string `json:"frequency"`
	Region             string `json:"region,omitempty"`
	NodeID             string `json:"node_id,omitempty"`
	Limit              int    `json:"limit,omitempty"`
	StartTime          string `json:"start_time,omitempty"`
	EndTime            string `json:"end_time,omitempty"`
	SnapshotShardIndex int    `json:"snapshot_shard_index,omitempty"`
	SnapshotShardCount int    `json:"snapshot_shard_count,omitempty"`
	Items              []Item `json:"items"`
}

func (request *Request) normalize() {
	if request == nil {
		return
	}
	request.SpaceID = strings.TrimSpace(request.SpaceID)
	request.BatchID = strings.TrimSpace(request.BatchID)
	request.DatasetID = strings.TrimSpace(request.DatasetID)
	request.MarketID = strings.ToLower(strings.TrimSpace(request.MarketID))
	request.InstrumentType = strings.ToLower(strings.TrimSpace(request.InstrumentType))
	request.ProviderID = strings.ToLower(strings.TrimSpace(request.ProviderID))
	request.SourceID = strings.ToLower(strings.TrimSpace(request.SourceID))
	request.SeriesTag = strings.TrimSpace(request.SeriesTag)
	request.DataType = strings.ToLower(strings.TrimSpace(request.DataType))
	if request.DataType == "" {
		request.DataType = "kline"
	}
	request.BatchKind = strings.ToLower(strings.TrimSpace(request.BatchKind))
	request.ScheduleID = strings.TrimSpace(request.ScheduleID)
	request.SourceEventID = strings.TrimSpace(request.SourceEventID)
	if request.BatchID == "" {
		request.BatchID = request.SourceEventID
	}
	if request.SourceEventID == "" {
		request.SourceEventID = request.BatchID
	}
	request.Frequency = strings.TrimSpace(request.Frequency)
	if request.MarketID == "crypto" {
		if canonical, err := report.NormalizeDatasetFrequency(request.Frequency); err == nil {
			request.Frequency = canonical
		}
	}
	for index := range request.Items {
		request.Items[index].SubjectID = strings.TrimSpace(request.Items[index].SubjectID)
		request.Items[index].ProviderSymbol = strings.TrimSpace(request.Items[index].ProviderSymbol)
		request.Items[index].TaskID = strings.TrimSpace(request.Items[index].TaskID)
		request.Items[index].SourceEventID = strings.TrimSpace(request.Items[index].SourceEventID)
	}
}

func (request Request) validate() error {
	dataType := strings.ToLower(strings.TrimSpace(request.DataType))
	if dataType == "" {
		dataType = "kline"
	}
	for name, value := range map[string]string{
		"space_id": request.SpaceID, "dataset_id": request.DatasetID,
		"market_id": request.MarketID, "instrument_type": request.InstrumentType,
		"frequency": request.Frequency, "source_event_id": request.SourceEventID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(request.Items) == 0 {
		return fmt.Errorf("items are required")
	}
	if len(request.Items) > 1000 {
		return fmt.Errorf("items exceed one invocation limit")
	}
	if dataType != "kline" && dataType != "symbol" {
		return fmt.Errorf("unsupported data_type %q", request.DataType)
	}
	if dataType == "symbol" && len(request.Items) != 1 {
		return fmt.Errorf("symbol requests require exactly one item")
	}
	if strings.TrimSpace(request.SourceID) != "" && strings.TrimSpace(request.ProviderID) == "" {
		return fmt.Errorf("provider_id is required when source_id is specified")
	}
	if request.Limit < 0 {
		return fmt.Errorf("limit cannot be negative")
	}
	seenSubjects := make(map[string]struct{}, len(request.Items))
	seenSourceEvents := make(map[string]int, len(request.Items))
	for index, item := range request.Items {
		if strings.TrimSpace(item.SubjectID) == "" {
			return fmt.Errorf("items[%d] subject_id is required", index)
		}
		if dataType == "kline" && strings.TrimSpace(item.ProviderSymbol) == "" {
			return fmt.Errorf("items[%d] provider_symbol is required for kline", index)
		}
		subjectID := strings.ToUpper(strings.TrimSpace(item.SubjectID))
		if _, exists := seenSubjects[subjectID]; exists {
			return fmt.Errorf("items[%d] subject_id %q is duplicated", index, item.SubjectID)
		}
		seenSubjects[subjectID] = struct{}{}
		eventID := strings.TrimSpace(item.SourceEventID)
		if eventID == "" {
			eventID = itemSourceEventID(request.SourceEventID, item.SubjectID)
		}
		if previous, exists := seenSourceEvents[eventID]; exists {
			return fmt.Errorf("items[%d] source_event_id duplicates items[%d]", index, previous)
		}
		seenSourceEvents[eventID] = index
	}
	return nil
}

type Response struct {
	Success     bool     `json:"success"`
	RowsWritten int      `json:"rows_written"`
	Failed      int      `json:"failed"`
	Errors      []string `json:"errors,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	Timestamp   string   `json:"timestamp"`
}

// Handler builds all source dependencies for one invocation. NewStorage is a
// seam for tests and lets deployments provide their Storage auth policy without
// putting credentials in event payloads.
type Handler struct {
	NewStorage        func(string, string, string) (marketfetch.KlineRowWriter, error)
	NewGetter         func() markethttp.Getter
	ProbeEgress       func(context.Context, string, string) (*model.Response, error)
	Now               func() time.Time
	RunSymbolSnapshot func(context.Context, Request, marketfetch.Storage, *markets.Composition, string) (*model.Response, error)
	PublishCompletion func(context.Context, Request, string, []itemResult) error
}

type itemResult struct {
	rows int
	last time.Time
	err  error
}

func NewHandler() *Handler {
	return &Handler{
		NewStorage: func(target, _, writeSource string) (marketfetch.KlineRowWriter, error) {
			return binance.NewMarketDataStorage(target, writeSource)
		},
		NewGetter: func() markethttp.Getter {
			timeoutMS := envPositiveInt("MOOX_FETCH_REQUEST_TIMEOUT_MS", 5000)
			return newRouteGetter(httpclient.NewHTTPClientWithTimeout(time.Duration(timeoutMS)*time.Millisecond), loadDNSRoutes(os.Getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON")))
		},
		ProbeEgress:       marketfetch.EgressProbe,
		Now:               time.Now,
		RunSymbolSnapshot: runSymbolSnapshot,
		PublishCompletion: publishGenericCompletion,
	}
}

func RegisterCloudFunction() { cloudfunction.Start(NewHandler().HandleRequest) }

func (handler *Handler) HandleRequest(ctx context.Context, raw json.RawMessage) (interface{}, error) {
	if handler == nil || handler.NewStorage == nil || handler.NewGetter == nil {
		return nil, fmt.Errorf("market data handler is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := ""
	if function, _ := functioncontext.FromContext(ctx); function != nil {
		requestID = function.RequestID
	}
	var event model.CloudFunctionEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return Response{Success: false, Errors: []string{fmt.Sprintf("decode event: %v", err)}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if event.Action == model.EventActionEgressProbe {
		probe := handler.ProbeEgress
		if probe == nil {
			probe = marketfetch.EgressProbe
		}
		probeResponse, probeErr := probe(ctx, eventDataString(event.Data, "provider"), eventDataString(event.Data, "market_type"))
		if probeErr != nil {
			return Response{Success: false, Errors: []string{probeErr.Error()}, RequestID: firstNonEmpty(event.RequestID, requestID), Timestamp: handler.timestamp()}, nil
		}
		if probeResponse == nil {
			return Response{Success: false, Errors: []string{"egress probe returned no response"}, RequestID: firstNonEmpty(event.RequestID, requestID), Timestamp: handler.timestamp()}, nil
		}
		return probeResponse, nil
	}
	if event.Action != "" && event.Action != defaultAction {
		return Response{Success: false, Errors: []string{"unsupported action"}, RequestID: event.RequestID, Timestamp: handler.timestamp()}, nil
	}
	if requestID == "" {
		requestID = event.RequestID
	}
	var request Request
	var err error
	if timerConfigured := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SUBJECTS")) != ""; timerConfigured && strings.EqualFold(strings.TrimSpace(event.Type), "timer") {
		if err := validateTimerEvent(event); err != nil {
			return Response{Success: false, Errors: []string{err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		now := handler.now()
		if event.Time != "" {
			parsed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(event.Time))
			if parseErr != nil {
				return Response{Success: false, Errors: []string{"timer event time is invalid: " + parseErr.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
			}
			now = parsed
		}
		request, err = requestFromTimerEnv(requestID, now)
	} else {
		request, err = decodeRequest(event.Data)
		if err == nil && request.SourceEventID == "" {
			request.SourceEventID = requestID
		}
	}
	if err != nil {
		return Response{Success: false, Errors: []string{err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	request.normalize()
	if err := request.validate(); err != nil {
		return Response{Success: false, Errors: []string{err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if expected := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); expected != "" && request.SpaceID != expected {
		return Response{Success: false, Errors: []string{"space_id does not match MOOX_SPACE_ID"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	invocationTimeout := envPositiveInt("MOOX_FETCH_TIMEOUT_SECONDS", 15)
	invocationCtx, invocationCancel := context.WithTimeout(ctx, time.Duration(invocationTimeout)*time.Second)
	defer invocationCancel()
	target := strings.TrimSpace(event.StorageRPCGatewayTarget)
	if target == "" {
		target = strings.TrimSpace(os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET"))
	}
	if target == "" {
		return Response{Success: false, Errors: []string{"storage_rpc_gateway_target is required"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	writer, err := handler.NewStorage(target, request.InstrumentType, "scf:"+requestID)
	if err != nil {
		return nil, err
	}
	providerID, sourceID := defaultSource(request)
	request.ProviderID = providerID
	request.SourceID = sourceID
	key := marketdata.SourceKey{ProviderID: providerID, SourceID: sourceID}
	if request.DataType == "symbol" {
		storage, ok := writer.(marketfetch.Storage)
		if !ok {
			return Response{Success: false, Errors: []string{"symbol snapshot storage does not implement metadata writes"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		run := handler.RunSymbolSnapshot
		if run == nil {
			run = runSymbolSnapshot
		}
		composition, compositionErr := markets.NewComposition(handler.NewGetter(), nil, true, marketfetch.NewHTTPRouteProviderFromEnvironment())
		if compositionErr != nil {
			return nil, compositionErr
		}
		// Keep the final three seconds of the fixed SCF invocation available for
		// publishing the completion fact even when exchange-info or Storage is
		// slow. Symbol snapshots are not allowed to consume the whole deadline.
		symbolWorkTimeout := time.Duration(invocationTimeout)*time.Second - 3*time.Second
		if symbolWorkTimeout <= 0 {
			return Response{Success: false, Errors: []string{"symbol snapshot has no completion-event deadline budget"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		symbolCtx, symbolCancel := context.WithTimeout(invocationCtx, symbolWorkTimeout)
		defer symbolCancel()
		legacyResponse, runErr := run(symbolCtx, request, storage, composition, requestID)
		if runErr != nil {
			return nil, runErr
		}
		if legacyResponse == nil {
			return nil, fmt.Errorf("symbol snapshot returned no response")
		}
		completionResults := []itemResult{{rows: legacyResponse.RowsWritten}}
		if !legacyResponse.Success {
			message := strings.TrimSpace(legacyResponse.Message)
			if message == "" {
				message = "symbol snapshot failed"
			}
			completionResults[0].err = errors.New(message)
		}
		if shouldPublishCompletion(event, request) {
			publish := handler.PublishCompletion
			if publish == nil {
				publish = publishGenericCompletion
			}
			publishCtx, publishCancel := context.WithTimeout(ctx, 3*time.Second)
			defer publishCancel()
			if err := publish(publishCtx, request, requestID, completionResults); err != nil {
				return nil, fmt.Errorf("publish market fetch completion: %w", err)
			}
		}
		return symbolResponse(legacyResponse, requestID, handler.timestamp()), nil
	}
	var normalTDX *tdxwire.NormalClient
	var tdxRoutes []routeprobe.Candidate
	tdxRouteIndex := 0
	tdxPort := 0
	tdxTimeout := 0 * time.Second
	if providerID == "tdx" && sourceID == "normal_7709" {
		host := strings.TrimSpace(os.Getenv("MOOX_TDX_HOST"))
		if host == "" {
			return Response{Success: false, Errors: []string{"MOOX_TDX_HOST is required for tdx/normal_7709"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		port := envPort("MOOX_TDX_PORT", 7709)
		if port != 7709 {
			return Response{Success: false, Errors: []string{"MOOX_TDX_PORT must be 7709 for tdx/normal_7709"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		addresses, routeErr := parseTDXRouteAddresses(os.Getenv("MOOX_TDX_ROUTES_JSON"))
		if routeErr != nil {
			return Response{Success: false, Errors: []string{routeErr.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		snapshot, routeErr := loadTDXRouteSnapshot(os.Getenv("MOOX_TDX_ROUTE_SNAPSHOT_JSON"))
		if routeErr != nil {
			return Response{Success: false, Errors: []string{routeErr.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		timeout := time.Duration(envNumber("MOOX_TDX_TIMEOUT_SECONDS", 1)) * time.Second
		tdxTimeout = timeout
		selection, routeErr := marketfetch.SelectRoute(invocationCtx, marketfetch.RouteSelectionOptions{
			SCFRegion:   firstNonEmpty(os.Getenv("MOOX_SCF_REGION"), "unknown"),
			SourceKey:   routeprobe.SourceKey{ProviderID: providerID, SourceID: sourceID},
			Transport:   routeprobe.TransportTCP,
			Host:        host,
			Port:        port,
			Addresses:   addresses,
			Snapshot:    snapshot,
			Prober:      tdxwire.RouteProber{Timeout: timeout},
			Probe:       routeprobe.ProbeOptions{Concurrency: envPositiveInt("MOOX_TDX_ROUTE_PROBE_CONCURRENCY", 1), Attempts: envPositiveInt("MOOX_TDX_ROUTE_PROBE_ATTEMPTS", 1), AttemptTimeout: time.Duration(envNumber("MOOX_TDX_ROUTE_PROBE_TIMEOUT_SECONDS", 1)) * time.Second},
			MaxFallback: envNonNegativeInt("MOOX_TDX_ROUTE_MAX_FALLBACK", 1),
		})
		if routeErr != nil {
			return Response{Success: false, Errors: []string{"select tdx route: " + routeErr.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		if len(selection.Routes) == 0 {
			return Response{Success: false, Errors: []string{"select tdx route: no route returned"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		tdxRoutes = append([]routeprobe.Candidate(nil), selection.Routes...)
		tdxPort = port
		var tdxErr error
		for index, route := range selection.Routes {
			host = route.Address
			normalTDX, tdxErr = tdxwire.NewNormalClient(host, port, timeout)
			if tdxErr != nil {
				continue
			}
			if tdxErr = normalTDX.Connect(invocationCtx); tdxErr == nil {
				tdxRouteIndex = index
				break
			}
			_ = normalTDX.Close()
			normalTDX = nil
		}
		if tdxErr != nil || normalTDX == nil {
			if tdxErr == nil {
				tdxErr = fmt.Errorf("no TDX route could be connected")
			}
			return Response{Success: false, Errors: []string{"connect tdx: " + tdxErr.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		defer normalTDX.Close()
	}
	composition, err := markets.NewComposition(handler.NewGetter(), normalTDX, true, marketfetch.NewHTTPRouteProviderFromEnvironment())
	if err != nil {
		return nil, err
	}
	manifest, ok := composition.Catalog.Lookup(request.MarketID, request.InstrumentType)
	if !ok || !manifest.Enabled {
		return Response{Success: false, Errors: []string{"market/instrument is not enabled"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if !manifest.SupportsDatasetFrequency(request.DatasetID, request.Frequency) {
		return Response{Success: false, Errors: []string{"dataset_id/frequency does not match the canonical market manifest"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if !manifestHasSource(manifest, key, request.Frequency) {
		return Response{Success: false, Errors: []string{"source is not declared for the market/frequency"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if _, ok := composition.Registry.Lookup(key); !ok {
		return Response{Success: false, Errors: []string{"source is not registered"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	start, err := parseOptionalTime(request.StartTime)
	if err != nil {
		return Response{Success: false, Errors: []string{err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	end, err := parseOptionalTime(request.EndTime)
	if err != nil {
		return Response{Success: false, Errors: []string{err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	settleDelay := time.Duration(envNonNegativeInt("MOOX_MARKET_FETCH_SETTLE_DELAY_SECONDS", 10)) * time.Second
	router := &marketfetch.ProviderRouter{Registry: composition.Registry, Writer: writer, Now: handler.Now, SettleDelay: settleDelay}
	response := Response{RequestID: requestID, Timestamp: handler.timestamp()}
	workTimeout := time.Duration(invocationTimeout) * time.Second
	if normalTDX != nil {
		// A failed route attempt consumes one request timeout, and a reconnect may
		// consume another timeout for dial plus normal-protocol setup. Reserve the
		// worst-case budget for every item because the TDX stream is serialized.
		minimum := tdxInvocationBudget(tdxTimeout, len(tdxRoutes), len(request.Items))
		remaining := time.Duration(invocationTimeout) * time.Second
		if deadline, ok := invocationCtx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		available := remaining - 3*time.Second
		if minimum > available {
			return Response{Success: false, Errors: []string{fmt.Sprintf("tdx request budget %s exceeds the remaining SCF invocation budget %s; reduce items, fallback routes, or TDX timeout", minimum, available)}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		workTimeout = available
	}
	workCtx, cancel := context.WithTimeout(invocationCtx, workTimeout)
	defer cancel()
	concurrency := envPositiveInt("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", 10)
	if normalTDX != nil {
		// One NormalClient owns one ordered TCP stream. Serializing its requests
		// also makes route reconnect/retry deterministic after a broken frame.
		concurrency = 1
	}
	if concurrency > len(request.Items) {
		concurrency = len(request.Items)
	}
	if concurrency > 30 {
		concurrency = 30
	}
	results := make([]itemResult, len(request.Items))
	sem := make(chan struct{}, concurrency)
	var waitGroup sync.WaitGroup
	for index, item := range request.Items {
		index, item := index, item
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			select {
			case sem <- struct{}{}:
			case <-workCtx.Done():
				results[index].err = workCtx.Err()
				return
			}
			defer func() { <-sem }()
			// Each pipeline call is one Storage payload. Keep a stable event id
			// for retries of that item, but do not reuse one event id across
			// multiple payloads: Storage deduplicates by source event and dataset.
			itemEventID := sourceEventIDForItem(request.SourceEventID, item)
			result, fetchErr := router.FetchAndWrite(workCtx, marketfetch.PipelineRequest{
				SpaceID: request.SpaceID, DatasetID: request.DatasetID, SeriesTag: request.SeriesTag,
				SourceEventID: itemEventID, SourceKey: key,
				Request: marketdata.KlineRequest{MarketID: request.MarketID, InstrumentType: request.InstrumentType, SubjectID: item.SubjectID, ProviderSymbol: item.ProviderSymbol, Frequency: request.Frequency, Limit: request.Limit, StartTime: start, EndTime: end},
			})
			if shouldReconnectTDX(fetchErr) && normalTDX != nil && len(tdxRoutes) > 0 {
				// A failed frame poisons the stream. Reconnect through each selected
				// route at most once for this item, then continue with the route that
				// succeeded for the remaining serialized items.
				currentRoute := tdxRouteIndex
				for offset := 1; offset <= len(tdxRoutes); offset++ {
					nextRoute := (currentRoute + offset) % len(tdxRoutes)
					if reconnectErr := normalTDX.Reconnect(workCtx, tdxRoutes[nextRoute].Address, tdxPort); reconnectErr != nil {
						continue
					}
					tdxRouteIndex = nextRoute
					result, fetchErr = router.FetchAndWrite(workCtx, marketfetch.PipelineRequest{
						SpaceID: request.SpaceID, DatasetID: request.DatasetID, SeriesTag: request.SeriesTag,
						SourceEventID: itemEventID, SourceKey: key,
						Request: marketdata.KlineRequest{MarketID: request.MarketID, InstrumentType: request.InstrumentType, SubjectID: item.SubjectID, ProviderSymbol: item.ProviderSymbol, Frequency: request.Frequency, Limit: request.Limit, StartTime: start, EndTime: end},
					})
					if fetchErr == nil {
						break
					}
					if !shouldReconnectTDX(fetchErr) {
						break
					}
				}
			}
			results[index] = itemResult{err: fetchErr, last: result.LastBar}
			if fetchErr == nil {
				results[index].rows = result.RowsWritten
			}
		}()
	}
	waitGroup.Wait()
	for index, result := range results {
		if result.err != nil {
			response.Failed++
			response.Errors = append(response.Errors, request.Items[index].SubjectID+": "+result.err.Error())
			continue
		}
		response.RowsWritten += result.rows
	}
	response.Success = response.Failed == 0
	if shouldPublishCompletion(event, request) {
		publish := handler.PublishCompletion
		if publish == nil {
			publish = publishGenericCompletion
		}
		publishCtx, publishCancel := context.WithTimeout(ctx, 3*time.Second)
		defer publishCancel()
		if err := publish(publishCtx, request, requestID, results); err != nil {
			return nil, fmt.Errorf("publish market fetch completion: %w", err)
		}
	}
	return response, nil
}

func eventDataString(data map[string]interface{}, key string) string {
	value, _ := data[key].(string)
	return strings.TrimSpace(value)
}

func tdxInvocationBudget(perAttempt time.Duration, routeCount, itemCount int) time.Duration {
	if perAttempt <= 0 || routeCount <= 0 || itemCount <= 0 {
		return 0
	}
	const maxDuration = time.Duration(1<<63 - 1)
	maxInt := int(^uint(0) >> 1)
	if routeCount > (maxInt-2)/2 {
		return maxDuration
	}
	// Include the initial connection setup and every selected-route reconnect.
	// The handler rejects work whose worst-case wire budget cannot leave time for
	// the completion event inside the fixed 15-second SCF invocation.
	perItemAttempts := 2 + 2*routeCount
	if itemCount > maxInt/perItemAttempts {
		return maxDuration
	}
	attempts := time.Duration(perItemAttempts * itemCount)
	if perAttempt > (maxDuration-time.Second)/attempts {
		return maxDuration
	}
	return perAttempt*attempts + time.Second
}

func shouldReconnectTDX(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, tdxwire.ErrTransport) || errors.Is(err, tdxwire.ErrProtocol)
}

func itemSourceEventID(batchEventID, subjectID string) string {
	digest := sha256.Sum256([]byte(batchEventID + "\x00" + subjectID))
	return batchEventID + ":" + hex.EncodeToString(digest[:8])
}

func sourceEventIDForItem(batchEventID string, item Item) string {
	if sourceEventID := strings.TrimSpace(item.SourceEventID); sourceEventID != "" {
		return sourceEventID
	}
	return itemSourceEventID(batchEventID, item.SubjectID)
}

// runSymbolSnapshot routes Binance exchange-info through the generic
// InstrumentPipeline. Unlike KlinePipeline, a symbol snapshot also mutates
// Metadata memberships and must return the completion fact so the scheduler can
// advance its durable batch state.
func runSymbolSnapshot(ctx context.Context, request Request, storage marketfetch.Storage, composition *markets.Composition, requestID string) (*model.Response, error) {
	if composition == nil || composition.Registry == nil {
		return nil, fmt.Errorf("symbol snapshot market composition is not initialized")
	}
	if request.InstrumentType != binance.InstTypeSPOT && request.InstrumentType != binance.InstTypeSWAP {
		return &model.Response{Success: false, Message: fmt.Sprintf("symbol snapshot instrument_type %q is not supported", request.InstrumentType), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	expectedSource, expectedDataset := "spot_http", "binance_spot_symbols"
	if request.InstrumentType == binance.InstTypeSWAP {
		expectedSource, expectedDataset = "swap_http", "binance_swap_symbols"
	}
	if request.SourceID != expectedSource || request.DatasetID != expectedDataset {
		return &model.Response{Success: false, Message: fmt.Sprintf("symbol snapshot source/dataset %s/%s does not match %s/%s", request.SourceID, request.DatasetID, expectedSource, expectedDataset), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	registration, ok := composition.Registry.Lookup(marketdata.SourceKey{ProviderID: request.ProviderID, SourceID: request.SourceID})
	if !ok || registration.Instruments == nil {
		return &model.Response{Success: false, Message: fmt.Sprintf("symbol snapshot source %s/%s is not registered", request.ProviderID, request.SourceID), RequestID: requestID, Timestamp: time.Now().UTC()}, nil
	}
	binding, err := binance.ResolveStorageBinding(request.InstrumentType)
	if err != nil {
		return nil, err
	}
	shardCount := request.SnapshotShardCount
	if shardCount <= 0 {
		shardCount = 1
	}
	pipeline := &marketfetch.InstrumentPipeline{Fetcher: registration.Instruments, Storage: storage}
	result, err := pipeline.FetchAndWrite(ctx, marketfetch.InstrumentPipelineRequest{
		SpaceID: request.SpaceID, DatasetID: request.DatasetID, SourceEventID: request.SourceEventID,
		SourceKey:  marketdata.SourceKey{ProviderID: request.ProviderID, SourceID: request.SourceID},
		Request:    marketdata.InstrumentRequest{MarketID: request.MarketID, InstrumentType: request.InstrumentType},
		WriteSpec:  marketfetch.InstrumentWriteSpec{DataSourceID: binding.DataSourceID, SubjectType: binding.SubjectType, SubjectMarket: binding.SubjectMarket, SeriesTag: request.SeriesTag},
		ShardIndex: request.SnapshotShardIndex, ShardCount: shardCount,
	})
	if err != nil {
		return nil, err
	}
	payload := &marketfetchpb.MarketFetchBatchCompleted{
		BatchId: firstNonEmpty(request.BatchID, request.SourceEventID), ScheduleId: request.ScheduleID, BatchKind: string(domain.BatchKindSymbolSnapshot), DatasetId: request.DatasetID,
		Frequency: request.Frequency, Region: request.Region, NodeId: request.NodeID, RequestId: requestID, PlannedCount: 1, SuccessCount: 1,
		Status: "succeeded", CompletedAt: timestamppb.New(time.Now().UTC()),
		Items: []*marketfetchpb.MarketFetchItemResult{{TaskId: request.Items[0].TaskID, SubjectId: request.Items[0].SubjectID, Outcome: string(domain.ItemOutcomeSuccess), SourceEventId: sourceEventIDForItem(request.SourceEventID, request.Items[0])}},
	}
	if result.Instruments == 0 {
		payload.Items[0].ErrorSummary = "symbol snapshot shard is empty"
	}
	return &model.Response{Success: true, RowsWritten: result.RowsWritten, Message: payload.Status, Data: payload, RequestID: requestID, Timestamp: time.Now().UTC()}, nil
}

func symbolResponse(response *model.Response, requestID, timestamp string) Response {
	result := Response{RequestID: requestID, Timestamp: timestamp}
	if response == nil {
		result.Errors = []string{"symbol snapshot returned no response"}
		return result
	}
	result.Success = response.Success
	result.RowsWritten = response.RowsWritten
	if payload, ok := response.Data.(*marketfetchpb.MarketFetchBatchCompleted); ok && payload != nil {
		for _, item := range payload.Items {
			if item == nil || item.Outcome == string(domain.ItemOutcomeSuccess) {
				continue
			}
			result.Failed++
			message := strings.TrimSpace(item.ErrorSummary)
			if message == "" {
				message = "symbol snapshot shard failed"
			}
			result.Errors = append(result.Errors, message)
		}
	}
	if !result.Success && len(result.Errors) == 0 && strings.TrimSpace(response.Message) != "" {
		result.Failed = 1
		result.Errors = []string{response.Message}
	}
	return result
}

func shouldPublishCompletion(event model.CloudFunctionEvent, request Request) bool {
	return !strings.EqualFold(strings.TrimSpace(event.Type), "timer") && strings.TrimSpace(request.BatchKind) != ""
}

func publishGenericCompletion(ctx context.Context, request Request, requestID string, results []itemResult) error {
	completed := time.Now().UTC()
	payload := &marketfetchpb.MarketFetchBatchCompleted{
		BatchId:      firstNonEmpty(request.BatchID, request.SourceEventID),
		BatchKind:    firstNonEmpty(request.BatchKind, string(domain.BatchKindRealtime)),
		ScheduleId:   request.ScheduleID,
		DatasetId:    request.DatasetID,
		Frequency:    request.Frequency,
		Region:       request.Region,
		NodeId:       firstNonEmpty(request.NodeID, os.Getenv("MOOX_SCF_FUNCTION_NAME")),
		RequestId:    requestID,
		PlannedCount: int32(len(results)),
		CompletedAt:  timestamppb.New(completed),
	}
	var firstError string
	for index, result := range results {
		item := request.Items[index]
		outcome := domain.ItemOutcomeSuccess
		errorType := ""
		errorSummary := ""
		if result.err != nil {
			outcome, errorType = genericItemOutcome(result.err)
			errorSummary = result.err.Error()
			if firstError == "" {
				firstError = errorSummary
			}
			if outcome == domain.ItemOutcomeInvalid {
				payload.PermanentFailedCount++
			} else {
				payload.RetryCount++
			}
		} else {
			payload.SuccessCount++
		}
		target := ""
		if !result.last.IsZero() {
			target = result.last.UTC().Format(time.RFC3339Nano)
		}
		payload.Items = append(payload.Items, &marketfetchpb.MarketFetchItemResult{
			TaskId: item.TaskID, SubjectId: item.SubjectID, Symbol: item.ProviderSymbol, TargetDataTime: target,
			Outcome: string(outcome), ErrorType: errorType, ErrorSummary: errorSummary,
			SourceEventId: sourceEventIDForItem(request.SourceEventID, item),
		})
	}
	switch {
	case payload.SuccessCount == payload.PlannedCount:
		payload.Status = "succeeded"
	case payload.SuccessCount > 0:
		payload.Status = "partial_failed"
	default:
		payload.Status = "failed"
	}
	payload.ErrorSummary = firstError
	return marketfetch.PublishCompletion(ctx, marketfetch.Request{
		BatchID: firstNonEmpty(request.BatchID, request.SourceEventID), ScheduleID: request.ScheduleID,
		BatchKind: domain.BatchKind(request.BatchKind), SpaceID: request.SpaceID,
		DatasetID: request.DatasetID, Frequency: request.Frequency, RequestID: requestID,
	}, payload)
}

func genericItemOutcome(err error) (domain.ItemOutcome, string) {
	if err == nil {
		return domain.ItemOutcomeSuccess, ""
	}
	if errors.Is(err, marketdata.ErrRateLimited) {
		return domain.ItemOutcomeHTTP429, "rate_limited"
	}
	if errors.Is(err, marketdata.ErrNotSupported) || errors.Is(err, marketdata.ErrOutOfRange) {
		return domain.ItemOutcomeInvalid, "unsupported_request"
	}
	if errors.Is(err, tdxwire.ErrProtocol) {
		return domain.ItemOutcomeNetworkError, "tdx_protocol"
	}
	if errors.Is(err, tdxwire.ErrTransport) {
		return domain.ItemOutcomeNetworkError, "tdx_transport"
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"required", "invalid", "unsupported", "does not support", "cannot be negative"} {
		if strings.Contains(message, marker) {
			return domain.ItemOutcomeInvalid, "invalid_request"
		}
	}
	return domain.ItemOutcomeNetworkError, "fetch"
}

func decodeRequest(data map[string]interface{}) (Request, error) {
	if len(data) == 0 {
		return Request{}, fmt.Errorf("market data request is required")
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return Request{}, fmt.Errorf("encode market data request: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, fmt.Errorf("decode market data request: %w", err)
	}
	return request, nil
}

func defaultSource(request Request) (string, string) {
	providerID := strings.ToLower(strings.TrimSpace(request.ProviderID))
	sourceID := strings.ToLower(strings.TrimSpace(request.SourceID))
	explicitProvider := providerID != ""
	if sourceID != "" {
		if providerID == "" {
			providerID = "eastmoney"
		}
		return providerID, sourceID
	}
	switch request.MarketID + "/" + request.InstrumentType {
	case "crypto/spot":
		if explicitProvider && !strings.EqualFold(providerID, "binance") {
			return providerID, ""
		}
		return "binance", "spot_http"
	case "crypto/swap":
		if explicitProvider && !strings.EqualFold(providerID, "binance") {
			return providerID, ""
		}
		return "binance", "swap_http"
	case "stock_cn/equity":
		if providerID == "" {
			providerID = "eastmoney"
		}
		switch providerID {
		case "tdx":
			return providerID, "normal_7709"
		case "tencent":
			return providerID, "stock_cn_http"
		case "eastmoney":
			return providerID, "stock_cn_http"
		case "ths":
			return providerID, "daily_http"
		case "sina":
			return providerID, "stock_cn_http"
		default:
			return providerID, ""
		}
	case "stock_hk/equity":
		if providerID == "" {
			providerID = "eastmoney"
		}
		if providerID == "eastmoney" {
			return providerID, "stock_hk_http"
		}
		if providerID == "sina" {
			return providerID, "stock_hk_http"
		}
		return providerID, ""
	case "stock_us/equity":
		if providerID == "" {
			providerID = "eastmoney"
		}
		if providerID == "eastmoney" {
			return providerID, "stock_us_http"
		}
		if providerID == "sina" {
			return providerID, "stock_us_http"
		}
		return providerID, ""
	case "stock_cn/index":
		if providerID == "" {
			providerID = "eastmoney"
		}
		if providerID == "tdx" {
			return providerID, "normal_7709"
		}
		if providerID == "eastmoney" {
			return providerID, "index_http"
		}
		if providerID == "cni" {
			return providerID, "index_cni_http"
		}
		if providerID == "sw" {
			return providerID, "index_sw_http"
		}
		return providerID, ""
	case "stock_cn/convertible_bond":
		if providerID == "" {
			providerID = "eastmoney"
		}
		if providerID == "tdx" {
			return providerID, "normal_7709"
		}
		if providerID == "eastmoney" {
			return providerID, "convertible_bond_http"
		}
		return providerID, ""
	default:
		if providerID == "" {
			providerID = "eastmoney"
		}
		return providerID, ""
	}
}

func requestFromTimerEnv(requestID string, now time.Time) (Request, error) {
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	marketID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_MARKET_ID"))
	instrumentType := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_INSTRUMENT_TYPE"))
	datasetID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_DATASET_ID"))
	frequency := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_FREQUENCY"))
	providerID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_PROVIDER"))
	sourceID := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SOURCE_ID"))
	for name, value := range map[string]string{
		"MOOX_SPACE_ID":                     spaceID,
		"MOOX_MARKET_FETCH_MARKET_ID":       marketID,
		"MOOX_MARKET_FETCH_INSTRUMENT_TYPE": instrumentType,
		"MOOX_MARKET_FETCH_DATASET_ID":      datasetID,
		"MOOX_MARKET_FETCH_FREQUENCY":       frequency,
	} {
		if value == "" {
			return Request{}, fmt.Errorf("timer market data environment requires %s", name)
		}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if sourceID == "" {
		_, sourceID = defaultSource(Request{MarketID: marketID, InstrumentType: instrumentType, ProviderID: providerID})
	}
	if providerID == "" {
		providerID, _ = defaultSource(Request{MarketID: marketID, InstrumentType: instrumentType})
	}
	if sourceID == "" {
		return Request{}, fmt.Errorf("timer market data environment requires MOOX_MARKET_FETCH_SOURCE_ID for %s/%s", marketID, instrumentType)
	}
	assignmentHash := strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_ASSIGNMENT_HASH"))
	if assignmentHash == "" {
		assignmentHash = "unassigned"
	}
	bucket := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
	sourceEventID := "timer:" + assignmentHash + ":" + bucket
	barLimit := envPositiveInt("MOOX_FETCH_REALTIME_BAR_LIMIT", envPositiveInt("MOOX_MARKET_FETCH_BAR_LIMIT", 3))
	if barLimit > 3 {
		barLimit = 3
	}
	externalSymbols, err := parseTimerSymbols(os.Getenv("MOOX_MARKET_FETCH_SYMBOLS_JSON"))
	if err != nil {
		return Request{}, err
	}
	subjects := strings.Split(os.Getenv("MOOX_MARKET_FETCH_SUBJECTS"), "|")
	items := make([]Item, 0, len(subjects))
	seen := make(map[string]struct{}, len(subjects))
	for _, rawSubject := range subjects {
		subject := strings.TrimSpace(rawSubject)
		if subject == "" {
			continue
		}
		if _, exists := seen[subject]; exists {
			continue
		}
		seen[subject] = struct{}{}
		symbol := strings.TrimSpace(externalSymbols[subject])
		if symbol == "" {
			return Request{}, fmt.Errorf("timer subject %s has no external symbol", subject)
		}
		items = append(items, Item{SubjectID: subject, ProviderSymbol: symbol})
	}
	if len(items) == 0 || len(items) > 30 {
		return Request{}, fmt.Errorf("timer market data subjects must contain 1..30 values")
	}
	return Request{
		SpaceID: spaceID, DatasetID: datasetID, MarketID: marketID, InstrumentType: instrumentType,
		ProviderID: providerID, SourceID: sourceID, SeriesTag: strings.TrimSpace(os.Getenv("MOOX_MARKET_FETCH_SERIES_TAG")),
		SourceEventID: sourceEventID, Frequency: frequency, Region: os.Getenv("MOOX_SCF_REGION"), NodeID: os.Getenv("MOOX_SCF_FUNCTION_NAME"), Limit: barLimit, Items: items,
	}, nil
}

func parseTimerSymbols(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("timer market data environment requires MOOX_MARKET_FETCH_SYMBOLS_JSON")
	}
	var symbols map[string]string
	if err := json.Unmarshal([]byte(raw), &symbols); err != nil {
		return nil, fmt.Errorf("decode timer market data symbols: %w", err)
	}
	return symbols, nil
}

func envPositiveInt(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envNonNegativeInt(name string, fallback int) int {
	raw, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func validateTimerEvent(event model.CloudFunctionEvent) error {
	if !strings.EqualFold(strings.TrimSpace(event.Type), "timer") {
		return fmt.Errorf("timer event type must be Timer")
	}
	if strings.TrimSpace(event.TriggerName) != timerTriggerName {
		return fmt.Errorf("timer trigger name must be %q", timerTriggerName)
	}
	if strings.TrimSpace(event.Message) != timerTriggerMessage {
		return fmt.Errorf("timer trigger message must be %q", timerTriggerMessage)
	}
	if strings.TrimSpace(event.Time) == "" {
		return fmt.Errorf("timer event time is required")
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimSpace(event.Time)); err != nil {
		return fmt.Errorf("timer event time is invalid: %w", err)
	}
	return nil
}

func parseOptionalTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return time.Time{}, fmt.Errorf("time %q must be RFC3339: %w", value, err)
	}
	return parsed.UTC(), nil
}

func (handler *Handler) timestamp() string {
	now := time.Now
	if handler != nil && handler.Now != nil {
		now = handler.Now
	}
	return now().UTC().Format(time.RFC3339Nano)
}

func (handler *Handler) now() time.Time {
	if handler != nil && handler.Now != nil {
		return handler.Now().UTC()
	}
	return time.Now().UTC()
}

type routeGetter struct {
	client *httpclient.HTTPClient
	routes map[string][]string
}

func newRouteGetter(client *httpclient.HTTPClient, routes map[string][]string) markethttp.Getter {
	return routeGetter{client: client, routes: routes}
}

func (getter routeGetter) Get(ctx context.Context, domain, path string, query url.Values, result interface{}) error {
	if getter.client == nil {
		return fmt.Errorf("HTTP client is not initialized")
	}
	return getter.client.GetWithIPs(ctx, domain, getter.routes[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))], path, query, result)
}

func (getter routeGetter) GetStream(ctx context.Context, domain, path string, query url.Values, consume func(io.Reader) error) error {
	if getter.client == nil {
		return fmt.Errorf("HTTP client is not initialized")
	}
	return getter.client.GetStreamWithIPs(ctx, domain, getter.routes[strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))], path, query, consume)
}

func loadDNSRoutes(raw string) map[string][]string {
	var decoded map[string]sources.DNSResolution
	if strings.TrimSpace(raw) == "" || json.Unmarshal([]byte(raw), &decoded) != nil {
		return nil
	}
	routes := make(map[string][]string, len(decoded))
	for host, route := range decoded {
		host = sources.NormalizeDNSHost(host)
		if host == "" || len(route.IPs) == 0 {
			continue
		}
		routes[host] = append([]string(nil), route.IPs...)
	}
	return routes
}

func parseTDXRouteAddresses(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var addresses []string
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&addresses); err != nil {
		return nil, fmt.Errorf("decode MOOX_TDX_ROUTES_JSON: %w", err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("MOOX_TDX_ROUTES_JSON must contain at least one route")
	}
	if len(addresses) > 64 {
		return nil, fmt.Errorf("MOOX_TDX_ROUTES_JSON must contain at most 64 routes")
	}
	seen := make(map[string]struct{}, len(addresses))
	result := make([]string, 0, len(addresses))
	for index, address := range addresses {
		ip := net.ParseIP(strings.TrimSpace(address))
		if ip == nil {
			return nil, fmt.Errorf("MOOX_TDX_ROUTES_JSON[%d] must be an IP address", index)
		}
		canonical := ip.String()
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}
	return result, nil
}

func loadTDXRouteSnapshot(raw string) (*routeprobe.Snapshot, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	snapshot, err := routeprobe.UnmarshalSnapshot([]byte(raw))
	if err != nil {
		return nil, fmt.Errorf("decode MOOX_TDX_ROUTE_SNAPSHOT_JSON: %w", err)
	}
	return &snapshot, nil
}

func envPort(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value < 1 || value > 65535 {
		return fallback
	}
	return value
}

func envNumber(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
}

func manifestHasSource(manifest marketmanifest.Manifest, key marketdata.SourceKey, frequency string) bool {
	for _, source := range manifest.Sources {
		if source.IsEnabled() && strings.EqualFold(strings.TrimSpace(source.ProviderID), key.ProviderID) && strings.EqualFold(strings.TrimSpace(source.SourceID), key.SourceID) && containsString(source.Frequencies, frequency) {
			return true
		}
	}
	return false
}
