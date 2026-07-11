package taskrunner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/builtin"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
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
	qualityDatasetID := firstString(stringValue(params, "quality_dataset_id"), "kline_quality_event")
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
	requiresTradingValues := stringValue(params, "instrument_type") != "index"
	sourceDatasets := stringMap(params["source_datasets"])
	sourceDatasetIDs := stringSlice(params["source_dataset_ids"])
	if len(sourceDatasetIDs) == 0 {
		sourceDatasetIDs = []string{sourceDatasetID}
	}
	bindings := make([]storageio.Binding, 0, len(sourceDatasetIDs)+1)
	for candidateID, datasetID := range sourceDatasets {
		bindings = append(bindings, storageio.Binding{SpaceID: spaceID, DatasetID: datasetID, Role: storageio.RoleProviderData, Feed: "kline", ProviderID: marketdata.ProviderID(candidateID)})
	}
	if len(sourceDatasets) == 0 {
		bindings = append(bindings, storageio.Binding{SpaceID: spaceID, DatasetID: sourceDatasetID, Role: storageio.RoleProviderData, Feed: "kline", ProviderID: providerID})
	}
	bindings = append(bindings, storageio.Binding{SpaceID: spaceID, DatasetID: unifiedDatasetID, Role: storageio.RoleUnifiedData, Feed: "kline", RequiredVolume: requiresTradingValues, RequiredAmount: requiresTradingValues}, storageio.Binding{SpaceID: spaceID, DatasetID: qualityDatasetID, Role: storageio.RoleQualityEvent, Feed: "quality_event"})
	store := storageio.NewClientWithAccess(access, nil, bindings)
	leaseEpoch, _ := strconv.ParseInt(stringValue(params, "lease_epoch"), 10, 64)
	gate := controlRequestGate{gateway: runtimeapp.GetServiceGatewayTarget(), leaseID: stringValue(params, "quota_lease_id"), leaseEpoch: leaseEpoch, executionNonce: stringValue(params, "execution_nonce"), scopeKey: stringValue(params, "quota_scope_key"), windows: quotaWindows(params)}
	if gate.leaseID == "" || gate.leaseEpoch <= 0 || gate.executionNonce == "" || len(gate.windows) == 0 {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("quota lease, execution nonce and quota windows are required"), "INVALID_LEASE")
	}
	resolutionEpoch, _ := strconv.ParseInt(stringValue(params, "resolution_lease_epoch"), 10, 64)
	resolutionGuard := controlLeaseGuard{gateway: runtimeapp.GetServiceGatewayTarget(), leaseID: stringValue(params, "resolution_lease_id"), leaseType: "resolution", leaseEpoch: resolutionEpoch}
	if resolutionGuard.leaseID == "" || resolutionGuard.leaseEpoch <= 0 {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("resolution lease is required"), "INVALID_RESOLUTION_LEASE")
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
	providerGuard := controlLeaseGuard{gateway: runtimeapp.GetServiceGatewayTarget(), leaseID: gate.leaseID, leaseType: "provider", leaseEpoch: gate.leaseEpoch}
	providerPriority := providerIDSlice(params["provider_priority"])
	if len(providerPriority) == 0 {
		providerPriority = []marketdata.ProviderID{providerID}
	}
	datasetMap := map[marketdata.ProviderID]string{}
	for id, datasetID := range sourceDatasets {
		datasetMap[marketdata.ProviderID(id)] = datasetID
	}
	if len(datasetMap) == 0 {
		datasetMap[providerID] = sourceDatasetID
	}
	pipe := pipeline.KlinePipeline{Provider: provider, Gate: gate, Store: store, SpaceID: spaceID, ProviderGuard: providerGuard, ResolutionGuard: resolutionGuard, SourceDatasetID: sourceDatasetID, SourceDatasetIDs: sourceDatasetIDs, SourceDatasets: datasetMap, UnifiedDatasetID: unifiedDatasetID, QualityDatasetID: qualityDatasetID, Resolver: pipeline.QualityResolver{Policy: pipeline.QualityPolicy{ProviderPriority: providerPriority, AuthoritativeSingleSource: true}}}
	summary, err := pipe.Run(ctx, providers.FetchKlinesRequest{MarketID: marketdata.MarketID(stringValue(params, "market_id")), ExchangeID: marketdata.ExchangeID(stringValue(params, "exchange_id")), ProductType: product, InstrumentType: instrument, Frequency: frequency, Subjects: []providers.ProviderSubject{{SubjectID: subjectID, ProviderSymbol: providerSymbol}}, StartTime: start, EndTime: end, Limit: intValue(params, "limit")})
	result := nodeRuntime.Result{Summary: map[string]any{"market_id": stringValue(params, "market_id"), "provider_id": string(providerID), "fetched_rows": summary.FetchedRows, "source_rows": summary.SourceRows, "unified_rows": summary.UnifiedRows, "request_count": summary.RequestCount, "complete": summary.Complete}}
	if err != nil {
		return result, nodeRuntime.Retryable(err, "MARKET_PIPELINE_FAILED")
	}
	return result, nil
}

func marketProvider(id marketdata.ProviderID) (providers.KlineProvider, error) {
	return builtin.Default("config/markets/stock_cn/calendar.yaml").Provider(id)
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
func quotaWindows(params map[string]any) []*pb.ProviderQuotaWindow {
	raw, ok := params["quota_windows"].([]any)
	if !ok {
		return nil
	}
	result := make([]*pb.ProviderQuotaWindow, 0, len(raw))
	for _, item := range raw {
		values, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, &pb.ProviderQuotaWindow{WindowSeconds: int64(numberValue(values["window_seconds"])), Limit: int64(numberValue(values["limit"]))})
	}
	return result
}
func numberValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case string:
		parsed, _ := strconv.Atoi(typed)
		return parsed
	}
	return 0
}
