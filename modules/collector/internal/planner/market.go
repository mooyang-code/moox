package planner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
)

type LeaseWriter interface {
	PutLease(context.Context, repository.MarketLease) error
}

type KlinePlanProvider struct {
	ProviderID      marketdata.ProviderID
	SourceDatasetID string
	ProviderSymbol  string
	QuotaScopeKey   string
	QuotaWindows    []repository.QuotaWindow
}

type MarketKlinePlanRequest struct {
	RuleID           string
	MarketID         marketdata.MarketID
	SpaceID          string
	ExchangeID       marketdata.ExchangeID
	ProductType      marketdata.ProductType
	InstrumentType   marketdata.InstrumentType
	UnifiedDatasetID string
	SubjectID        string
	Frequency        marketdata.Frequency
	StartTime        time.Time
	EndTime          time.Time
	ScheduleWindow   time.Time
	ScheduleInterval time.Duration
	LeaseTTL         time.Duration
	LeaseEpoch       int64
	Limit            int
	CandidateChain   []KlinePlanProvider
}

type MarketPlanner struct {
	Leases LeaseWriter
	Now    func() time.Time
}

func (p MarketPlanner) PlanKline(ctx context.Context, request MarketKlinePlanRequest) (domain.TaskInstance, error) {
	if p.Leases == nil {
		return domain.TaskInstance{}, fmt.Errorf("lease repository is required")
	}
	if request.MarketID == "" || request.SpaceID == "" || request.UnifiedDatasetID == "" || request.SubjectID == "" || request.Frequency == "" {
		return domain.TaskInstance{}, fmt.Errorf("market, space, unified dataset, subject and frequency are required")
	}
	if request.EndTime.IsZero() || !request.EndTime.After(request.StartTime) {
		return domain.TaskInstance{}, fmt.Errorf("end_time must be after start_time")
	}
	if request.ScheduleWindow.IsZero() || request.LeaseEpoch <= 0 || request.LeaseTTL <= 0 {
		return domain.TaskInstance{}, fmt.Errorf("schedule window and positive lease fencing are required")
	}
	if len(request.CandidateChain) == 0 {
		return domain.TaskInstance{}, fmt.Errorf("candidate chain is required")
	}
	selected := request.CandidateChain[0]
	if selected.ProviderID == "" || selected.SourceDatasetID == "" || selected.ProviderSymbol == "" || len(selected.QuotaWindows) == 0 {
		return domain.TaskInstance{}, fmt.Errorf("selected provider binding and quota windows are required")
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	expiresAt := now.Add(request.LeaseTTL)
	fixedWindow := request.StartTime.UTC().Format(time.RFC3339) + "/" + request.EndTime.UTC().Format(time.RFC3339)
	taskID := domain.StableMarketKlineTaskID(string(request.MarketID), request.UnifiedDatasetID, request.SubjectID, string(request.Frequency))
	providerLeaseKey := string(request.MarketID) + "|" + string(selected.ProviderID) + "|" + fixedWindow
	resolutionLeaseKey := string(request.MarketID) + "|" + request.UnifiedDatasetID + "|" + request.SubjectID + "|" + string(request.Frequency) + "|" + fixedWindow
	providerLeaseID := stablePlanID("provider", providerLeaseKey)
	resolutionLeaseID := stablePlanID("resolution", resolutionLeaseKey)
	owner := stablePlanID("attempt", taskID, request.ScheduleWindow.UTC().Unix())
	for _, lease := range []repository.MarketLease{
		{LeaseID: providerLeaseID, LeaseType: "provider", LeaseKey: providerLeaseKey, Epoch: request.LeaseEpoch, OwnerID: owner, ExpiresAt: expiresAt},
		{LeaseID: resolutionLeaseID, LeaseType: "resolution", LeaseKey: resolutionLeaseKey, Epoch: request.LeaseEpoch, OwnerID: owner, ExpiresAt: expiresAt},
	} {
		if err := p.Leases.PutLease(ctx, lease); err != nil {
			return domain.TaskInstance{}, fmt.Errorf("persist %s lease: %w", lease.LeaseType, err)
		}
	}
	windows := append([]repository.QuotaWindow(nil), selected.QuotaWindows...)
	sort.Slice(windows, func(i, j int) bool { return windows[i].WindowSeconds < windows[j].WindowSeconds })
	params := map[string]any{
		"job_type": "collect.kline", "phase": "fetch", "market_id": string(request.MarketID), "space_id": request.SpaceID,
		"exchange_id": string(request.ExchangeID), "product_type": string(request.ProductType), "instrument_type": string(request.InstrumentType),
		"unified_dataset_id": request.UnifiedDatasetID, "quality_dataset_id": "kline_quality_event", "source_dataset_id": selected.SourceDatasetID, "subject_id": request.SubjectID,
		"provider_id": string(selected.ProviderID), "provider_symbol": selected.ProviderSymbol, "frequency": string(request.Frequency),
		"start_time": request.StartTime.UTC().Format(time.RFC3339), "end_time": request.EndTime.UTC().Format(time.RFC3339), "limit": request.Limit,
		"quota_lease_id": providerLeaseID, "lease_epoch": request.LeaseEpoch, "resolution_lease_id": resolutionLeaseID,
		"resolution_lease_epoch": request.LeaseEpoch, "execution_nonce": owner, "quota_scope_key": selected.QuotaScopeKey,
		"schedule_window": request.ScheduleWindow.UTC().Format(time.RFC3339), "schedule_interval": request.ScheduleInterval.String(),
		"candidate_chain": providerIDs(request.CandidateChain), "candidate_bindings": providerBindingMap(request.CandidateChain), "provider_priority": providerIDs(request.CandidateChain), "source_dataset_ids": providerDatasetIDs(request.CandidateChain), "source_datasets": providerDatasetMap(request.CandidateChain), "quota_windows": windows,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return domain.TaskInstance{}, err
	}
	return domain.TaskInstance{SpaceID: request.SpaceID, TaskID: taskID, RuleID: request.RuleID, Exchange: string(request.ExchangeID), Market: string(request.ProductType), DataType: "kline", DatasetID: request.UnifiedDatasetID, SubjectID: request.SubjectID, Symbol: selected.ProviderSymbol, Interval: string(request.Frequency), LastExecStatus: domain.InstanceStatusPending, TaskParams: string(raw), Result: "{}", CreateTime: now, ModifyTime: now}, nil
}
func providerBindingMap(values []KlinePlanProvider) map[string]any {
	out := make(map[string]any, len(values))
	for _, value := range values {
		windows := append([]repository.QuotaWindow(nil), value.QuotaWindows...)
		sort.Slice(windows, func(i, j int) bool { return windows[i].WindowSeconds < windows[j].WindowSeconds })
		out[string(value.ProviderID)] = map[string]any{
			"source_dataset_id": value.SourceDatasetID,
			"provider_symbol":   value.ProviderSymbol,
			"quota_scope_key":   value.QuotaScopeKey,
			"quota_windows":     windows,
		}
	}
	return out
}

func stablePlanID(kind, key string, suffix ...any) string {
	value := fmt.Sprintf("%s|%s", kind, key)
	for _, item := range suffix {
		value += fmt.Sprintf("|%v", item)
	}
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func providerIDs(values []KlinePlanProvider) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, string(value.ProviderID))
	}
	return out
}
func providerDatasetIDs(values []KlinePlanProvider) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		if value.SourceDatasetID != "" && !seen[value.SourceDatasetID] {
			seen[value.SourceDatasetID] = true
			out = append(out, value.SourceDatasetID)
		}
	}
	return out
}
func providerDatasetMap(values []KlinePlanProvider) map[string]string {
	out := make(map[string]string, len(values))
	for _, value := range values {
		out[string(value.ProviderID)] = value.SourceDatasetID
	}
	return out
}
