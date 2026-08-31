// Package marketdata contains the generic, one-shot market-data SCF entrypoint.
// It is deliberately separate from the legacy crypto entrypoint: a request
// names a canonical Market/Instrument and a concrete SourceKey, then the
// shared pipeline owns normalization and Storage writes.
package marketdata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	markethttp "github.com/mooyang-code/moox/modules/collector/internal/sources/markethttp/eastmoney"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"github.com/mooyang-code/moox/packages/routeprobe"
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
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
}

// Request is the public event data contract for stock/index/bond SCF calls.
// ProviderID and SourceID are optional only when the canonical default source
// for the requested market is used; callers cannot substitute an arbitrary
// provider after composition has been built.
type Request struct {
	SpaceID        string `json:"space_id"`
	DatasetID      string `json:"dataset_id"`
	MarketID       string `json:"market_id"`
	InstrumentType string `json:"instrument_type"`
	ProviderID     string `json:"provider_id,omitempty"`
	SourceID       string `json:"source_id,omitempty"`
	SeriesTag      string `json:"series_tag,omitempty"`
	SourceEventID  string `json:"source_event_id"`
	Frequency      string `json:"frequency"`
	Limit          int    `json:"limit,omitempty"`
	StartTime      string `json:"start_time,omitempty"`
	EndTime        string `json:"end_time,omitempty"`
	Items          []Item `json:"items"`
}

func (request Request) validate() error {
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
	if request.Limit < 0 {
		return fmt.Errorf("limit cannot be negative")
	}
	seenSubjects := make(map[string]struct{}, len(request.Items))
	for index, item := range request.Items {
		if strings.TrimSpace(item.SubjectID) == "" || strings.TrimSpace(item.ProviderSymbol) == "" {
			return fmt.Errorf("items[%d] subject_id and provider_symbol are required", index)
		}
		subjectID := strings.ToUpper(strings.TrimSpace(item.SubjectID))
		if _, exists := seenSubjects[subjectID]; exists {
			return fmt.Errorf("items[%d] subject_id %q is duplicated", index, item.SubjectID)
		}
		seenSubjects[subjectID] = struct{}{}
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
	NewStorage func(string, string, string) (marketfetch.KlineRowWriter, error)
	NewGetter  func() markethttp.Getter
	Now        func() time.Time
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
		Now: time.Now,
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
	if err := request.validate(); err != nil {
		return Response{Success: false, Errors: []string{err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if expected := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); expected != "" && request.SpaceID != expected {
		return Response{Success: false, Errors: []string{"space_id does not match MOOX_SPACE_ID"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
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
	key := marketdata.SourceKey{ProviderID: providerID, SourceID: sourceID}
	var normalTDX *tdxwire.NormalClient
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
		timeout := time.Duration(envNumber("MOOX_TDX_TIMEOUT_SECONDS", 15)) * time.Second
		selection, routeErr := marketfetch.SelectRoute(ctx, marketfetch.RouteSelectionOptions{
			SCFRegion:   firstNonEmpty(os.Getenv("MOOX_SCF_REGION"), "unknown"),
			SourceKey:   routeprobe.SourceKey{ProviderID: providerID, SourceID: sourceID},
			Transport:   routeprobe.TransportTCP,
			Host:        host,
			Port:        port,
			Addresses:   addresses,
			Snapshot:    snapshot,
			Prober:      tdxwire.RouteProber{Timeout: timeout},
			Probe:       routeprobe.ProbeOptions{Concurrency: envPositiveInt("MOOX_TDX_ROUTE_PROBE_CONCURRENCY", 1), Attempts: envPositiveInt("MOOX_TDX_ROUTE_PROBE_ATTEMPTS", 1), AttemptTimeout: time.Duration(envNumber("MOOX_TDX_ROUTE_PROBE_TIMEOUT_SECONDS", 5)) * time.Second},
			MaxFallback: envPositiveInt("MOOX_TDX_ROUTE_MAX_FALLBACK", 2),
		})
		if routeErr != nil {
			return Response{Success: false, Errors: []string{"select tdx route: " + routeErr.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		if len(selection.Routes) == 0 {
			return Response{Success: false, Errors: []string{"select tdx route: no route returned"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		var tdxErr error
		for _, route := range selection.Routes {
			host = route.Address
			normalTDX, tdxErr = tdxwire.NewNormalClient(host, port, timeout)
			if tdxErr != nil {
				continue
			}
			if tdxErr = normalTDX.Connect(ctx); tdxErr == nil {
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
	composition, err := markets.NewComposition(handler.NewGetter(), normalTDX, false)
	if err != nil {
		return nil, err
	}
	manifest, ok := composition.Catalog.Lookup(request.MarketID, request.InstrumentType)
	if !ok || !manifest.Enabled {
		return Response{Success: false, Errors: []string{"market/instrument is not enabled"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if request.DatasetID != manifest.DatasetID {
		return Response{Success: false, Errors: []string{"dataset_id does not match the canonical market manifest"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
	}
	if !containsString(manifest.Frequencies, request.Frequency) {
		return Response{Success: false, Errors: []string{"frequency is not declared by the market manifest"}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
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
	router := &marketfetch.ProviderRouter{Registry: composition.Registry, Writer: writer}
	response := Response{RequestID: requestID, Timestamp: handler.timestamp()}
	invocationTimeout := envPositiveInt("MOOX_FETCH_TIMEOUT_SECONDS", 15)
	workCtx, cancel := context.WithTimeout(ctx, time.Duration(invocationTimeout)*time.Second)
	defer cancel()
	concurrency := envPositiveInt("MOOX_FETCH_MAX_INFLIGHT_REQUESTS", 10)
	if concurrency > len(request.Items) {
		concurrency = len(request.Items)
	}
	if concurrency > 30 {
		concurrency = 30
	}
	type itemResult struct {
		rows int
		err  error
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
			itemEventID := itemSourceEventID(request.SourceEventID, item.SubjectID)
			result, fetchErr := router.FetchAndWrite(workCtx, marketfetch.PipelineRequest{
				SpaceID: request.SpaceID, DatasetID: request.DatasetID, SeriesTag: request.SeriesTag,
				SourceEventID: itemEventID, SourceKey: key,
				Request: marketdata.KlineRequest{MarketID: request.MarketID, InstrumentType: request.InstrumentType, SubjectID: item.SubjectID, ProviderSymbol: item.ProviderSymbol, Frequency: request.Frequency, Limit: request.Limit, StartTime: start, EndTime: end},
			})
			results[index] = itemResult{err: fetchErr}
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
	return response, nil
}

func itemSourceEventID(batchEventID, subjectID string) string {
	digest := sha256.Sum256([]byte(batchEventID + "\x00" + subjectID))
	return batchEventID + ":" + hex.EncodeToString(digest[:8])
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
	providerID := strings.TrimSpace(request.ProviderID)
	sourceID := strings.TrimSpace(request.SourceID)
	if providerID == "" {
		providerID = "eastmoney"
	}
	if sourceID != "" {
		return providerID, sourceID
	}
	switch request.MarketID + "/" + request.InstrumentType {
	case "stock_cn/equity":
		return providerID, "stock_cn_http"
	case "stock_hk/equity":
		return providerID, "stock_hk_http"
	case "stock_us/equity":
		return providerID, "stock_us_http"
	case "stock_cn/index":
		return providerID, "index_http"
	case "stock_cn/convertible_bond":
		return providerID, "convertible_bond_http"
	default:
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
	if providerID == "" {
		providerID = "eastmoney"
	}
	if sourceID == "" {
		_, sourceID = defaultSource(Request{MarketID: marketID, InstrumentType: instrumentType, ProviderID: providerID})
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
		SourceEventID: sourceEventID, Frequency: frequency, Limit: barLimit, Items: items,
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
