package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/builtin"
	"github.com/mooyang-code/moox/modules/collector/internal/coverage"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/planner"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"trpc.group/trpc-go/trpc-go/client"
)

func (s *Service) ReconcileMarketCoverage(ctx context.Context, limit int) (int, error) {
	if err := s.requireLeader(ctx); err != nil {
		return 0, err
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	instances, _, err := s.instanceRepo.List(ctx, repository.TaskInstanceFilter{DataType: "kline", IncludeDeleted: false, Page: 1, PageSize: limit})
	if err != nil {
		return 0, err
	}
	var repairs []domain.TaskInstance
	for _, instance := range instances {
		planned, err := s.reconcileCoverageInstance(ctx, instance)
		if err != nil {
			return len(repairs), fmt.Errorf("reconcile %s: %w", instance.TaskID, err)
		}
		repairs = append(repairs, planned...)
	}
	if len(repairs) == 0 {
		return 0, nil
	}
	ids, err := s.cloudJobs.SubmitCollectorJobItems(ctx, repairs)
	if err != nil {
		return 0, err
	}
	_ = ids
	_, _ = s.cloudJobs.WakeCollectorNodes(ctx, taskpublisher.WakeOptions{SpaceID: repairs[0].SpaceID, JobTypes: []string{"collect.kline"}})
	return len(repairs), nil
}

func (s *Service) reconcileCoverageInstance(ctx context.Context, instance domain.TaskInstance) ([]domain.TaskInstance, error) {
	params := map[string]any{}
	if err := json.Unmarshal([]byte(instance.TaskParams), &params); err != nil {
		return nil, err
	}
	marketID := stringValueMap(params, "market_id")
	manifest, ok := s.manifests[marketID]
	if !ok || !manifest.RuntimeEnabled {
		return nil, nil
	}
	frequency, err := marketdata.ParseFrequency(stringValueMap(params, "frequency"))
	if err != nil {
		return nil, err
	}
	start, err := time.Parse(time.RFC3339, stringValueMap(params, "start_time"))
	if err != nil {
		return nil, err
	}
	end, err := time.Parse(time.RFC3339, stringValueMap(params, "end_time"))
	if err != nil {
		return nil, err
	}
	module, err := builtin.Default("config/markets/stock_cn/calendar.yaml").Market(marketdata.MarketID(marketID))
	if err != nil {
		return nil, err
	}
	days, err := module.Calendar().TradingDays(start, end)
	if err != nil {
		return nil, err
	}
	sessions, err := coverageSessions(days)
	if err != nil {
		return nil, err
	}
	unifiedID := stringValueMap(params, "unified_dataset_id")
	coverageID := manifestDataset(manifest, "coverage_state", "coverage", "")
	if coverageID == "" {
		coverageID = "market_coverage"
	}
	access := s.storageAccess
	if access == nil {
		access = storagepb.NewAccessClientProxy(client.WithTarget(s.storageAccessTarget))
	}
	store := storageio.NewClientWithAccess(access, nil, []storageio.Binding{{SpaceID: instance.SpaceID, DatasetID: unifiedID, Role: storageio.RoleUnifiedData, Feed: "kline"}, {SpaceID: instance.SpaceID, DatasetID: coverageID, Role: storageio.RoleCoverageState, Feed: "coverage"}})
	sink := &repairCollector{}
	partition := start.UTC().Format("2006-01-02")
	_, err = (coverage.Reconciler{Reader: store, States: store, Repairs: sink, Now: s.now}).Reconcile(ctx, coverage.Request{SpaceID: instance.SpaceID, DatasetID: unifiedID, SubjectID: instance.SubjectID, PartitionID: partition, Frequency: frequency, Start: start, End: end, Sessions: sessions})
	if err != nil {
		return nil, err
	}
	providerID := stringValueMap(params, "provider_id")
	configured, ok := manifestProvider(manifest, providerID)
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", providerID)
	}
	windows := make([]repository.QuotaWindow, 0, len(configured.Quotas))
	for _, value := range configured.Quotas {
		windows = append(windows, repository.QuotaWindow{WindowSeconds: value.WindowSeconds, Limit: value.Limit})
	}
	plannerValue := planner.MarketPlanner{Leases: s.marketControl, Now: s.now}
	result := make([]domain.TaskInstance, 0, len(sink.values))
	for _, repair := range sink.values {
		generation, err := repository.GetOrCreateGeneration(ctx, s.db, "coverage|"+repair.ID, repair.Start.UTC())
		if err != nil {
			return nil, err
		}
		planned, err := plannerValue.PlanKline(ctx, planner.MarketKlinePlanRequest{RuleID: instance.RuleID, MarketID: marketdata.MarketID(marketID), SpaceID: instance.SpaceID, ExchangeID: marketdata.ExchangeID(stringValueMap(params, "exchange_id")), ProductType: marketdata.ProductType(stringValueMap(params, "product_type")), InstrumentType: marketdata.InstrumentType(stringValueMap(params, "instrument_type")), UnifiedDatasetID: unifiedID, SubjectID: instance.SubjectID, Frequency: frequency, StartTime: repair.Start, EndTime: repair.End.Add(time.Duration(frequency.DurationMinutes()) * time.Minute), ScheduleWindow: generation.Generation, ScheduleInterval: time.Hour, LeaseTTL: 2 * time.Minute, LeaseEpoch: generation.Epoch, Limit: intValueMap(params, "limit"), CandidateChain: []planner.KlinePlanProvider{{ProviderID: marketdata.ProviderID(providerID), SourceDatasetID: stringValueMap(params, "source_dataset_id"), ProviderSymbol: stringValueMap(params, "provider_symbol"), QuotaScopeKey: stringValueMap(params, "quota_scope_key"), QuotaWindows: windows}}})
		if err != nil {
			return nil, err
		}
		result = append(result, planned)
	}
	return result, nil
}

type repairCollector struct{ values []coverage.RepairRequest }

func (r *repairCollector) EnqueueRepair(_ context.Context, value coverage.RepairRequest) error {
	r.values = append(r.values, value)
	return nil
}
func coverageSessions(days []markets.CalendarDay) ([]coverage.Session, error) {
	result := []coverage.Session{}
	for _, day := range days {
		location, err := time.LoadLocation(day.Timezone)
		if err != nil {
			return nil, err
		}
		anchor, err := time.ParseInLocation("2006-01-02", day.TradeDate, location)
		if err != nil {
			return nil, err
		}
		for _, session := range day.Sessions {
			result = append(result, coverage.Session{TradeDate: day.TradeDate, Open: session.Open, Close: session.Close, DailyAnchor: anchor.UTC()})
		}
	}
	return result, nil
}
func manifestProvider(manifest marketmanifest.Manifest, id string) (marketmanifest.Provider, bool) {
	for _, value := range manifest.Providers {
		if value.ID == id {
			return value, true
		}
	}
	return marketmanifest.Provider{}, false
}
func stringValueMap(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}
func intValueMap(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	}
	return 0
}
