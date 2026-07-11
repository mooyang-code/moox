package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/builtin"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/planner"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"github.com/mooyang-code/moox/modules/collector/internal/routing"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	"github.com/mooyang-code/moox/packages/marketmanifest"
)

func (s *Service) recalculateMarketRule(ctx context.Context, rule *domain.TaskRule) (int, error) {
	manifest, ok := s.manifests[rule.MarketID]
	if !ok {
		return 0, fmt.Errorf("market manifest %q is not loaded", rule.MarketID)
	}
	if !manifest.RuntimeEnabled || !manifest.Readiness.CapabilityEnabled {
		return 0, fmt.Errorf("market %q is not runtime ready", rule.MarketID)
	}
	now := s.now().UTC()
	scheduleInterval, err := marketScheduleInterval(rule.ScheduleSpec)
	if err != nil {
		return 0, err
	}
	scheduleWindow := now.Truncate(scheduleInterval)
	generation, err := repository.GetOrCreateGeneration(ctx, s.db, rule.MarketID+"|"+rule.Feed+"|"+scheduleWindow.Format(time.RFC3339), scheduleWindow)
	if err != nil {
		return 0, err
	}
	var instances []domain.TaskInstance
	switch rule.Feed {
	case "calendar":
		instances, err = s.planMarketCalendar(rule, manifest, generation, scheduleInterval)
	case "instrument":
		instances, err = s.planMarketInstrument(ctx, rule, manifest, generation, scheduleInterval)
	case "kline":
		instances, err = s.planMarketKlines(ctx, rule, manifest, generation, scheduleInterval, now)
	default:
		return 0, fmt.Errorf("unsupported market feed %q", rule.Feed)
	}
	if err != nil {
		return 0, err
	}
	if err := s.instanceRepo.UpsertMany(ctx, instances); err != nil {
		return 0, err
	}
	ids, err := s.cloudJobs.SubmitCollectorJobItems(ctx, instances)
	if err != nil {
		return 0, err
	}
	if err := s.instanceRepo.UpdateCloudJobItemIDs(ctx, rule.SpaceID, ids); err != nil {
		return 0, err
	}
	_, _ = s.cloudJobs.WakeCollectorNodes(ctx, taskpublisher.WakeOptions{SpaceID: rule.SpaceID, JobTypes: jobTypesFromInstances(instances)})
	return len(instances), nil
}

func (s *Service) planMarketCalendar(rule *domain.TaskRule, manifest marketmanifest.Manifest, generation domain.MarketGeneration, interval time.Duration) ([]domain.TaskInstance, error) {
	datasetID := manifestDataset(manifest, "unified_data", "calendar", "")
	if datasetID == "" {
		return nil, fmt.Errorf("calendar dataset is not bound")
	}
	params := map[string]any{"job_type": "collect.calendar", "phase": "materialize_policy", "market_id": rule.MarketID, "space_id": rule.SpaceID, "exchange_id": manifest.Exchange.ID, "unified_dataset_id": datasetID, "generation": generation.Generation.Format(time.RFC3339Nano), "start_time": generation.Generation.Format(time.RFC3339), "end_time": generation.Generation.AddDate(1, 0, 0).Format(time.RFC3339), "limit": 100, "schedule_window": generation.Generation.Format(time.RFC3339), "schedule_interval": interval.String()}
	return []domain.TaskInstance{marketInstance(rule, domain.StableMarketCalendarTaskID(rule.MarketID, manifest.Exchange.ID, datasetID), datasetID, "", "", params, generation.Generation)}, nil
}

func (s *Service) planMarketInstrument(ctx context.Context, rule *domain.TaskRule, manifest marketmanifest.Manifest, generation domain.MarketGeneration, interval time.Duration) ([]domain.TaskInstance, error) {
	if len(manifest.Providers) == 0 {
		return nil, fmt.Errorf("instrument provider is not configured")
	}
	provider := manifest.Providers[0]
	sourceID := manifestDataset(manifest, "provider_data", "instrument", provider.ID)
	unifiedID := manifestDataset(manifest, "unified_data", "instrument", "")
	if sourceID == "" || unifiedID == "" {
		return nil, fmt.Errorf("instrument dataset bindings are incomplete")
	}
	leaseKey := rule.MarketID + "|instrument|" + provider.ID + "|" + generation.Generation.Format(time.RFC3339)
	leaseID := stableScheduleID("provider", leaseKey)
	expiry := s.now().UTC().Add(2 * time.Minute)
	if err := s.marketControl.PutLease(ctx, repository.MarketLease{LeaseID: leaseID, LeaseType: "provider", LeaseKey: leaseKey, Epoch: generation.Epoch, OwnerID: rule.RuleID, ExpiresAt: expiry}); err != nil {
		return nil, err
	}
	params := map[string]any{"job_type": "collect.instrument", "phase": "fetch", "market_id": rule.MarketID, "space_id": rule.SpaceID, "exchange_id": manifest.Exchange.ID, "provider_id": provider.ID, "source_dataset_id": sourceID, "unified_dataset_id": unifiedID, "generation": generation.Generation.Format(time.RFC3339Nano), "instrument_types": strings.Join(jsonStrings(rule.InstrumentTypes), ","), "limit": 500, "quota_lease_id": leaseID, "lease_epoch": generation.Epoch, "execution_nonce": stableScheduleID("instrument", rule.RuleID, generation.Generation.Format(time.RFC3339Nano)), "quota_scope_key": providerQuotaScope(provider), "quota_windows": quotaWindowParams(provider.Quotas), "subject_dataset_ids": unifiedMarketDatasets(manifest), "timezone": manifest.Timezone, "schedule_window": generation.Generation.Format(time.RFC3339), "schedule_interval": interval.String()}
	return []domain.TaskInstance{marketInstance(rule, domain.StableMarketInstrumentTaskID(rule.MarketID, manifest.Exchange.ID, firstString(manifest.ProductTypes...), unifiedID), unifiedID, "", "", params, generation.Generation)}, nil
}

func (s *Service) planMarketKlines(ctx context.Context, rule *domain.TaskRule, manifest marketmanifest.Manifest, generation domain.MarketGeneration, interval time.Duration, now time.Time) ([]domain.TaskInstance, error) {
	providerIDs := make([]string, 0, len(manifest.Providers))
	for _, value := range manifest.Providers {
		providerIDs = append(providerIDs, value.ID)
	}
	frequencies := jsonStrings(rule.Frequencies)
	if len(frequencies) == 0 {
		return nil, fmt.Errorf("kline frequencies are required")
	}
	instrumentTypes := jsonStrings(rule.InstrumentTypes)
	if len(instrumentTypes) == 0 {
		instrumentTypes = manifest.InstrumentTypes
	}
	var instances []domain.TaskInstance
	catalog := builtin.Default("config/markets/stock_cn/calendar.yaml")
	marketPlanner := planner.MarketPlanner{Leases: s.marketControl, Now: s.now}
	start, end, err := marketHistoryWindow(rule, now)
	if err != nil {
		return nil, err
	}
	for _, instrumentType := range instrumentTypes {
		unifiedID := unifiedKlineDataset(manifest, instrumentType)
		if unifiedID == "" {
			continue
		}
		subjects, err := s.datasetSrc.ListSubjectsForProviders(ctx, rule.SpaceID, unifiedID, providerIDs)
		if err != nil {
			return nil, err
		}
		subjects = filterSubjects(subjects, jsonStrings(rule.SubjectFilters))
		for _, subject := range subjects {
			for _, frequencyText := range frequencies {
				frequency, err := marketdata.ParseFrequency(frequencyText)
				if err != nil {
					return nil, err
				}
				query := providers.CapabilityQuery{Feed: providers.FeedKline, ProductType: marketdata.ProductType(firstString(manifest.ProductTypes...)), InstrumentType: marketdata.InstrumentType(instrumentType), Frequency: frequency, StartTime: start, EndTime: end}
				candidates := make([]routing.Candidate, 0)
				bindings := map[string]planner.KlinePlanProvider{}
				for _, configured := range manifest.Providers {
					symbol := subject.ProviderSymbols[configured.ID]
					if symbol == "" {
						continue
					}
					concrete, err := catalog.Provider(marketdata.ProviderID(configured.ID))
					if err != nil {
						return nil, err
					}
					sourceID := manifestDataset(manifest, "provider_data", "kline", configured.ID)
					if sourceID == "" {
						continue
					}
					candidates = append(candidates, routing.Candidate{ProviderID: marketdata.ProviderID(configured.ID), Weight: 1, Enabled: true, Health: routing.HealthClosed, Capabilities: concrete.Capabilities()})
					windows := make([]repository.QuotaWindow, 0, len(configured.Quotas))
					for _, q := range configured.Quotas {
						windows = append(windows, repository.QuotaWindow{WindowSeconds: q.WindowSeconds, Limit: q.Limit})
					}
					bindings[configured.ID] = planner.KlinePlanProvider{ProviderID: marketdata.ProviderID(configured.ID), SourceDatasetID: sourceID, ProviderSymbol: symbol, QuotaScopeKey: providerQuotaScope(configured), QuotaWindows: windows}
				}
				chain, err := routing.Route(routing.RouteRequest{ShardKey: rule.MarketID + "|" + subject.SubjectID + "|" + frequencyText + "|" + generation.Generation.Format(time.RFC3339), Query: query, Candidates: candidates})
				if err != nil {
					return nil, err
				}
				planProviders := make([]planner.KlinePlanProvider, 0, len(chain))
				for _, id := range chain {
					planProviders = append(planProviders, bindings[string(id)])
				}
				instance, err := marketPlanner.PlanKline(ctx, planner.MarketKlinePlanRequest{RuleID: rule.RuleID, MarketID: marketdata.MarketID(rule.MarketID), SpaceID: rule.SpaceID, ExchangeID: marketdata.ExchangeID(manifest.Exchange.ID), ProductType: marketdata.ProductType(firstString(manifest.ProductTypes...)), InstrumentType: marketdata.InstrumentType(instrumentType), UnifiedDatasetID: unifiedID, SubjectID: subject.SubjectID, Frequency: frequency, StartTime: start, EndTime: end, ScheduleWindow: generation.Generation, ScheduleInterval: interval, LeaseTTL: 2 * time.Minute, LeaseEpoch: generation.Epoch, Limit: 1000, CandidateChain: planProviders})
				if err != nil {
					return nil, err
				}
				instances = append(instances, instance)
			}
		}
	}
	return instances, nil
}

func marketInstance(rule *domain.TaskRule, taskID, datasetID, subjectID, frequency string, params map[string]any, now time.Time) domain.TaskInstance {
	raw, _ := json.Marshal(params)
	return domain.TaskInstance{SpaceID: rule.SpaceID, TaskID: taskID, RuleID: rule.RuleID, Exchange: "", Market: rule.MarketID, DataType: rule.Feed, DatasetID: datasetID, SubjectID: subjectID, Interval: frequency, LastExecStatus: domain.InstanceStatusPending, TaskParams: string(raw), Result: "{}", CreateTime: now, ModifyTime: now}
}
func marketScheduleInterval(raw string) (time.Duration, error) {
	values := map[string]any{}
	_ = json.Unmarshal([]byte(raw), &values)
	text, _ := values["interval"].(string)
	if text == "" {
		text = "30m"
	}
	value, err := time.ParseDuration(text)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid market schedule interval %q", text)
	}
	return value, nil
}
func marketHistoryWindow(rule *domain.TaskRule, now time.Time) (time.Time, time.Time, error) {
	end := now
	if rule.HistoryEnd != "" {
		value, err := time.Parse(time.RFC3339, rule.HistoryEnd)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		end = value.UTC()
	}
	start := end.Add(-24 * time.Hour)
	if rule.HistoryStart != "" {
		value, err := time.Parse(time.RFC3339, rule.HistoryStart)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		start = value.UTC()
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("history end must be after start")
	}
	return start, end, nil
}
func jsonStrings(raw string) []string {
	var values []string
	_ = json.Unmarshal([]byte(raw), &values)
	return values
}
func manifestDataset(m marketmanifest.Manifest, role, feed, provider string) string {
	for _, d := range m.Datasets {
		if d.Role == role && d.Feed == feed && (provider == "" || d.ProviderID == provider) {
			return d.ID
		}
	}
	return ""
}
func unifiedKlineDataset(m marketmanifest.Manifest, instrument string) string {
	for _, f := range m.Feeds {
		if f.InstrumentType == instrument && f.DatasetID != "" {
			return f.DatasetID
		}
	}
	return manifestDataset(m, "unified_data", "kline", "")
}
func unifiedMarketDatasets(m marketmanifest.Manifest) []string {
	out := []string{}
	for _, d := range m.Datasets {
		if d.Role == "unified_data" && (d.Feed == "instrument" || d.Feed == "kline") {
			out = append(out, d.ID)
		}
	}
	return out
}
func providerQuotaScope(p marketmanifest.Provider) string {
	if len(p.Quotas) > 0 {
		return p.Quotas[0].Scope
	}
	return "provider"
}
func quotaWindowParams(values []marketmanifest.Quota) []map[string]any {
	out := make([]map[string]any, 0, len(values))
	for _, value := range values {
		out = append(out, map[string]any{"window_seconds": value.WindowSeconds, "limit": value.Limit})
	}
	return out
}
func filterSubjects(values []domain.DatasetSubject, allow []string) []domain.DatasetSubject {
	if len(allow) == 0 {
		return values
	}
	set := map[string]bool{}
	for _, v := range allow {
		set[v] = true
	}
	out := values[:0]
	for _, v := range values {
		if set[v.SubjectID] {
			out = append(out, v)
		}
	}
	return out
}
func stableScheduleID(parts ...string) string {
	return domain.StableMarketCalendarTaskID(strings.Join(parts, "|"), "", "")
}
func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
