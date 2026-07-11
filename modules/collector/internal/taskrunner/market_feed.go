package taskrunner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/builtin"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/pipeline"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"trpc.group/trpc-go/trpc-go/client"
)

func executeMarketInstrumentJobItem(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	params := item.Params
	spaceID := firstString(item.SpaceID, stringValue(params, "space_id"))
	if spaceID == "" || spaceID != runtimeSpaceID() {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("job space %q does not match runtime space %q", spaceID, runtimeSpaceID()), "SPACE_MISMATCH")
	}
	providerID := marketdata.ProviderID(stringValue(params, "provider_id"))
	baseProvider, err := marketProvider(providerID)
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "UNSUPPORTED_PROVIDER")
	}
	provider, ok := baseProvider.(providers.InstrumentProvider)
	if !ok {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("provider %q has no instrument capability", providerID), "UNSUPPORTED_PROVIDER")
	}
	sourceID, unifiedID := stringValue(params, "source_dataset_id"), stringValue(params, "unified_dataset_id")
	if sourceID == "" || unifiedID == "" {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("instrument source and unified datasets are required"), "INVALID_JOB_ITEM")
	}
	generation, err := parseJobTime(params, "generation")
	if err != nil || generation.IsZero() {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("valid generation is required: %w", err), "INVALID_GENERATION")
	}
	accessTarget := runtimeapp.GetStorageAccessTarget()
	if accessTarget == "" {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(fmt.Errorf("storage access target is required"), "STORAGE_UNAVAILABLE")
	}
	store := storageio.NewClientWithAccess(storagepb.NewAccessClientProxy(client.WithTarget(accessTarget)), nil, []storageio.Binding{{SpaceID: spaceID, DatasetID: sourceID, Role: storageio.RoleProviderData, Feed: "instrument", ProviderID: providerID}, {SpaceID: spaceID, DatasetID: unifiedID, Role: storageio.RoleUnifiedData, Feed: "instrument"}})
	metadataTarget := runtimeapp.GetStorageMetadataTarget()
	if metadataTarget == "" {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(fmt.Errorf("storage metadata target is required"), "STORAGE_UNAVAILABLE")
	}
	datasetIDs := stringSlice(params["subject_dataset_ids"])
	if len(datasetIDs) == 0 {
		datasetIDs = []string{unifiedID}
	}
	registrar := storageio.NewInstrumentMetadataRegistrar(storagepb.NewMetadataClientProxy(client.WithTarget(metadataTarget)), nil, spaceID, string(providerID), firstString(stringValue(params, "timezone"), "UTC"), datasetIDs)
	leaseEpoch, _ := strconv.ParseInt(stringValue(params, "lease_epoch"), 10, 64)
	gate := controlRequestGate{gateway: runtimeapp.GetServiceGatewayTarget(), leaseID: stringValue(params, "quota_lease_id"), leaseEpoch: leaseEpoch, executionNonce: stringValue(params, "execution_nonce"), scopeKey: stringValue(params, "quota_scope_key"), windows: quotaWindows(params)}
	if gate.leaseID == "" || gate.leaseEpoch <= 0 || gate.executionNonce == "" || len(gate.windows) == 0 {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("instrument quota lease is required"), "INVALID_LEASE")
	}
	resolutionEpoch, _ := strconv.ParseInt(stringValue(params, "resolution_lease_epoch"), 10, 64)
	resolutionGuard := controlLeaseGuard{gateway: runtimeapp.GetServiceGatewayTarget(), leaseID: stringValue(params, "resolution_lease_id"), leaseType: "resolution", leaseEpoch: resolutionEpoch}
	if resolutionGuard.leaseID == "" || resolutionGuard.leaseEpoch <= 0 {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("instrument resolution lease is required"), "INVALID_RESOLUTION_LEASE")
	}
	pipe := pipeline.InstrumentPipeline{Provider: provider, Gate: gate, Store: store, Registrar: registrar, ResolutionGuard: resolutionGuard, SpaceID: spaceID, SourceDatasetID: sourceID, SourceDatasetIDs: []string{sourceID}, SourceDatasets: map[marketdata.ProviderID]string{providerID: sourceID}, UnifiedDatasetID: unifiedID, ProviderPriority: []marketdata.ProviderID{providerID}, Generation: generation}
	result, err := pipe.Run(ctx, providers.FetchInstrumentsRequest{MarketID: marketdata.MarketID(stringValue(params, "market_id")), ExchangeID: marketdata.ExchangeID(stringValue(params, "exchange_id")), InstrumentTypes: parseInstrumentTypes(stringValue(params, "instrument_types")), SnapshotAt: generation, Limit: intValue(params, "limit"), Cursor: stringValue(params, "cursor")})
	summary := map[string]any{"market_id": stringValue(params, "market_id"), "provider_id": string(providerID), "fetched_rows": result.Fetched, "source_rows": result.SourceRows, "unified_rows": result.UnifiedRows, "request_count": result.RequestCount, "complete": result.Complete, "next_cursor": result.NextCursor}
	if err != nil {
		return nodeRuntime.Result{Summary: summary}, nodeRuntime.Retryable(err, "INSTRUMENT_PIPELINE_FAILED")
	}
	return nodeRuntime.Result{Summary: summary}, nil
}

func executeMarketCalendarJobItem(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	params := item.Params
	spaceID := firstString(item.SpaceID, stringValue(params, "space_id"))
	if spaceID == "" || spaceID != runtimeSpaceID() {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("job space %q does not match runtime space %q", spaceID, runtimeSpaceID()), "SPACE_MISMATCH")
	}
	marketID := marketdata.MarketID(stringValue(params, "market_id"))
	module, err := builtin.Default("config/markets/stock_cn/calendar.yaml").Market(marketID)
	if err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(err, "UNSUPPORTED_MARKET")
	}
	if module.Calendar() == nil {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("market %q has no calendar policy", marketID), "UNSUPPORTED_CALENDAR")
	}
	datasetID := stringValue(params, "unified_dataset_id")
	generation, generationErr := parseJobTime(params, "generation")
	start, startErr := parseJobTime(params, "start_time")
	end, endErr := parseJobTime(params, "end_time")
	if datasetID == "" || generationErr != nil || generation.IsZero() || startErr != nil || endErr != nil || !end.After(start) {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("calendar dataset, generation and valid window are required"), "INVALID_JOB_ITEM")
	}
	accessTarget := runtimeapp.GetStorageAccessTarget()
	if accessTarget == "" {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(fmt.Errorf("storage access target is required"), "STORAGE_UNAVAILABLE")
	}
	store := storageio.NewClientWithAccess(storagepb.NewAccessClientProxy(client.WithTarget(accessTarget)), nil, []storageio.Binding{{SpaceID: spaceID, DatasetID: datasetID, Role: storageio.RoleUnifiedData, Feed: "calendar"}})
	resolutionEpoch, _ := strconv.ParseInt(stringValue(params, "resolution_lease_epoch"), 10, 64)
	resolutionGuard := controlLeaseGuard{gateway: runtimeapp.GetServiceGatewayTarget(), leaseID: stringValue(params, "resolution_lease_id"), leaseType: "resolution", leaseEpoch: resolutionEpoch}
	if resolutionGuard.leaseID == "" || resolutionGuard.leaseEpoch <= 0 {
		return nodeRuntime.Result{}, nodeRuntime.Permanent(fmt.Errorf("calendar resolution lease is required"), "INVALID_RESOLUTION_LEASE")
	}
	result, err := (pipeline.CalendarPipeline{Policy: module.Calendar(), Store: store, DatasetID: datasetID, Generation: generation, ResolutionGuard: resolutionGuard}).Materialize(ctx, pipeline.CalendarRequest{Start: start, End: end, Limit: intValue(params, "limit"), Cursor: stringValue(params, "cursor")})
	summary := map[string]any{"market_id": string(marketID), "rows": result.Rows, "complete": result.Complete, "next_cursor": result.NextCursor}
	if err != nil {
		return nodeRuntime.Result{Summary: summary}, nodeRuntime.Retryable(err, "CALENDAR_PIPELINE_FAILED")
	}
	return nodeRuntime.Result{Summary: summary}, nil
}

func parseInstrumentTypes(raw string) []marketdata.InstrumentType {
	values := strings.Split(raw, ",")
	result := make([]marketdata.InstrumentType, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, marketdata.InstrumentType(value))
		}
	}
	return result
}
