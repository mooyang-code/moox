package taskrunner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	binanceprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/binance"
	okxprovider "github.com/mooyang-code/moox/modules/collector/internal/providers/okx"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"trpc.group/trpc-go/trpc-go/client"
)

func executeMarketKlineJobItem(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	params := item.Params
	spaceID := strings.TrimSpace(item.SpaceID)
	if spaceID == "" {
		spaceID = stringValue(params, "space_id")
	}
	if spaceID == "" || spaceID != runtimeSpaceID() {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("job space %q does not match runtime space %q", spaceID, runtimeSpaceID()), "SPACE_MISMATCH")
	}
	providerID := marketdata.ProviderID(stringValue(params, "provider_id"))
	provider, err := marketProvider(providerID)
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "UNSUPPORTED_PROVIDER")
	}
	frequency, err := marketdata.ParseFrequency(stringValue(params, "frequency"))
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "INVALID_FREQUENCY")
	}
	sourceDatasetID := stringValue(params, "source_dataset_id")
	unifiedDatasetID := stringValue(params, "unified_dataset_id")
	subjectID := stringValue(params, "subject_id")
	providerSymbol := stringValue(params, "provider_symbol")
	if sourceDatasetID == "" || unifiedDatasetID == "" || subjectID == "" || providerSymbol == "" {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("source_dataset_id, unified_dataset_id, subject_id and provider_symbol are required"), "INVALID_JOB_ITEM")
	}
	accessTarget := runtimeapp.GetStorageAccessTarget()
	if accessTarget == "" {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(fmt.Errorf("storage access target is required"), "STORAGE_UNAVAILABLE")
	}
	access := storagepb.NewAccessClientProxy(client.WithTarget(accessTarget))
	bindings := []storageio.Binding{{SpaceID: spaceID, DatasetID: sourceDatasetID, Role: storageio.RoleProviderData, Feed: "kline", ProviderID: providerID}, {SpaceID: spaceID, DatasetID: unifiedDatasetID, Role: storageio.RoleUnifiedData, Feed: "kline"}}
	store := storageio.NewClientWithAccess(access, nil, bindings)
	leaseEpoch, _ := strconv.ParseInt(stringValue(params, "lease_epoch"), 10, 64)
	gate := jobRequestGate{leaseID: stringValue(params, "quota_lease_id"), leaseEpoch: leaseEpoch, jobItemID: item.JobItemID}
	if gate.leaseID == "" || gate.leaseEpoch <= 0 {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("quota_lease_id and positive lease_epoch are required"), "INVALID_LEASE")
	}
	start, err := parseJobTime(params, "start_time")
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "INVALID_WINDOW")
	}
	end, err := parseJobTime(params, "end_time")
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "INVALID_WINDOW")
	}
	product := marketdata.ProductType(strings.ToLower(stringValue(params, "product_type")))
	instrument := marketdata.InstrumentType(strings.ToLower(stringValue(params, "instrument_type")))
	pipe := pipeline.KlinePipeline{Provider: provider, Gate: gate, Store: store, SpaceID: spaceID, SourceDatasetID: sourceDatasetID, SourceDatasetIDs: []string{sourceDatasetID}, SourceDatasets: map[marketdata.ProviderID]string{providerID: sourceDatasetID}, UnifiedDatasetID: unifiedDatasetID, Resolver: pipeline.QualityResolver{Policy: pipeline.QualityPolicy{ProviderPriority: []marketdata.ProviderID{providerID}, AuthoritativeSingleSource: true}}}
	summary, err := pipe.Run(ctx, providers.FetchKlinesRequest{MarketID: marketdata.MarketID(stringValue(params, "market_id")), ExchangeID: marketdata.ExchangeID(stringValue(params, "exchange_id")), ProductType: product, InstrumentType: instrument, Frequency: frequency, Subjects: []providers.ProviderSubject{{SubjectID: subjectID, ProviderSymbol: providerSymbol}}, StartTime: start, EndTime: end, Limit: intValue(params, "limit")})
	result := nodeRuntime.Result{Summary: map[string]any{"market_id": stringValue(params, "market_id"), "provider_id": string(providerID), "fetched_rows": summary.FetchedRows, "source_rows": summary.SourceRows, "unified_rows": summary.UnifiedRows, "request_count": summary.RequestCount, "complete": summary.Complete}}
	if err != nil {
		return result, nodeRuntime.Retryable(err, "MARKET_PIPELINE_FAILED")
	}
	return result, nil
}

func marketProvider(id marketdata.ProviderID) (providers.KlineProvider, error) {
	switch id {
	case "binance":
		return binanceprovider.New(binanceprovider.Config{}), nil
	case "okx":
		return okxprovider.New(okxprovider.Config{}), nil
	default:
		return nil, fmt.Errorf("provider %q is not built in", id)
	}
}

type jobRequestGate struct {
	leaseID    string
	leaseEpoch int64
	jobItemID  string
}

func (g jobRequestGate) BeforeRequest(_ context.Context, meta providers.RequestMeta) (providers.RequestPermit, error) {
	if meta.RequestCost <= 0 {
		return providers.RequestPermit{}, fmt.Errorf("request cost must be positive")
	}
	return providers.RequestPermit{PermitID: fmt.Sprintf("%s:%d", g.leaseID, meta.RequestIndex), LeaseEpoch: g.leaseEpoch, Allowed: true, ExpiresAt: time.Now().UTC().Add(90 * time.Second)}, nil
}
func parseJobTime(params map[string]any, key string) (time.Time, error) {
	raw := stringValue(params, key)
	if raw == "" {
		return time.Time{}, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be RFC3339: %w", key, err)
	}
	return value.UTC(), nil
}
func intValue(params map[string]any, key string) int {
	switch value := params[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	}
	return 0
}
