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
	"net/url"
	"os"
	"strconv"
	"strings"
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
	tdxwire "github.com/mooyang-code/moox/packages/tdx"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
)

const defaultAction = model.EventActionMarketFetch

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
	for index, item := range request.Items {
		if strings.TrimSpace(item.SubjectID) == "" || strings.TrimSpace(item.ProviderSymbol) == "" {
			return fmt.Errorf("items[%d] subject_id and provider_symbol are required", index)
		}
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
			return newRouteGetter(httpclient.NewHTTPClient(), loadDNSRoutes(os.Getenv("MOOX_MARKET_FETCH_DNS_ROUTES_JSON")))
		},
		Now: time.Now,
	}
}

func RegisterCloudFunction() { cloudfunction.Start(NewHandler().HandleRequest) }

func (handler *Handler) HandleRequest(ctx context.Context, raw json.RawMessage) (interface{}, error) {
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
	request, err := decodeRequest(event.Data)
	if err != nil {
		return Response{Success: false, Errors: []string{err.Error()}, RequestID: event.RequestID, Timestamp: handler.timestamp()}, nil
	}
	if requestID == "" {
		requestID = event.RequestID
	}
	if request.SourceEventID == "" {
		request.SourceEventID = requestID
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
	if handler == nil || handler.NewStorage == nil || handler.NewGetter == nil {
		return nil, fmt.Errorf("market data handler is not initialized")
	}
	writer, err := handler.NewStorage(target, request.InstrumentType, "scf:"+requestID)
	if err != nil {
		return nil, err
	}
	providerID, sourceID := defaultSource(request)
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
		var tdxErr error
		normalTDX, tdxErr = tdxwire.NewNormalClient(host, port, time.Duration(envNumber("MOOX_TDX_TIMEOUT_SECONDS", 15))*time.Second)
		if tdxErr != nil {
			return nil, tdxErr
		}
		if err := normalTDX.Connect(ctx); err != nil {
			return Response{Success: false, Errors: []string{"connect tdx: " + err.Error()}, RequestID: requestID, Timestamp: handler.timestamp()}, nil
		}
		defer normalTDX.Close()
	}
	composition, err := markets.NewComposition(handler.NewGetter(), normalTDX, false)
	if err != nil {
		return nil, err
	}
	key := marketdata.SourceKey{ProviderID: providerID, SourceID: sourceID}
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
	for _, item := range request.Items {
		// Each invocation of the pipeline below is one Storage payload. Keep a
		// stable event id for retries of that item, but do not reuse one event id
		// across multiple payloads: Storage deduplicates by source event and
		// dataset before it sees the individual row keys.
		itemEventID := itemSourceEventID(request.SourceEventID, item.SubjectID)
		result, fetchErr := router.FetchAndWrite(ctx, marketfetch.PipelineRequest{
			SpaceID: request.SpaceID, DatasetID: request.DatasetID, SeriesTag: request.SeriesTag,
			SourceEventID: itemEventID, SourceKey: key,
			Request: marketdata.KlineRequest{MarketID: request.MarketID, InstrumentType: request.InstrumentType, SubjectID: item.SubjectID, ProviderSymbol: item.ProviderSymbol, Frequency: request.Frequency, Limit: request.Limit, StartTime: start, EndTime: end},
		})
		if fetchErr != nil {
			response.Failed++
			response.Errors = append(response.Errors, item.SubjectID+": "+fetchErr.Error())
			continue
		}
		response.RowsWritten += result.RowsWritten
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
		if strings.EqualFold(strings.TrimSpace(source.ProviderID), key.ProviderID) && strings.EqualFold(strings.TrimSpace(source.SourceID), key.SourceID) && containsString(source.Frequencies, frequency) {
			return true
		}
	}
	return false
}
