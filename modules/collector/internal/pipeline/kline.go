package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type KlineStore interface {
	WriteProviderKlines(context.Context, string, []marketdata.ProviderKline) error
	Candidates(context.Context, string, []string, string, marketdata.Frequency, time.Time) ([]marketdata.ProviderKline, error)
	Unified(context.Context, string, string, string, marketdata.Frequency, time.Time) (*marketdata.ResolvedKline, error)
	WriteUnifiedKline(context.Context, string, marketdata.ResolvedKline) error
}
type QualityEventStore interface {
	WriteQualityEvents(context.Context, string, marketdata.ResolvedKline, []QualityEvent) error
}
type ResolutionLeaseGuard interface{ BeforeUnifiedWrite(context.Context) error }
type KlinePipeline struct {
	Provider         providers.KlineProvider
	Gate             providers.RequestGate
	Store            KlineStore
	Resolver         QualityResolver
	SourceDatasetID  string
	SourceDatasetIDs []string
	SourceDatasets   map[marketdata.ProviderID]string
	SpaceID          string
	ResolutionGuard  ResolutionLeaseGuard
	ProviderGuard    ResolutionLeaseGuard
	UnifiedDatasetID string
	QualityDatasetID string
}
type KlinePipelineResult struct {
	FetchedRows  int
	SourceRows   int
	UnifiedRows  int
	RequestCount int
	Complete     bool
}

func (p KlinePipeline) Run(ctx context.Context, request providers.FetchKlinesRequest) (KlinePipelineResult, error) {
	if p.Provider == nil || p.Gate == nil || p.Store == nil {
		return KlinePipelineResult{}, fmt.Errorf("provider, request gate and store are required")
	}
	fetched, err := p.Provider.FetchKlines(ctx, p.Gate, request)
	if err != nil {
		return KlinePipelineResult{}, err
	}
	closed := make([]marketdata.ProviderKline, 0, len(fetched.Rows))
	for _, row := range fetched.Rows {
		if row.Closed {
			closed = append(closed, row)
		}
	}
	result := KlinePipelineResult{FetchedRows: len(fetched.Rows), RequestCount: fetched.RequestCount, Complete: fetched.Complete}
	if len(closed) == 0 {
		return result, nil
	}
	if p.ProviderGuard != nil {
		if err := p.ProviderGuard.BeforeUnifiedWrite(ctx); err != nil {
			return result, fmt.Errorf("validate provider lease before source write: %w", err)
		}
	}
	if err := p.Store.WriteProviderKlines(ctx, p.SourceDatasetID, closed); err != nil {
		return result, fmt.Errorf("persist provider source: %w", err)
	}
	result.SourceRows = len(closed)
	for _, source := range closed {
		candidateDatasets := p.SourceDatasetIDs
		if len(candidateDatasets) == 0 {
			candidateDatasets = []string{p.SourceDatasetID}
		}
		candidates, err := p.Store.Candidates(ctx, p.SpaceID, candidateDatasets, source.SubjectID, source.Frequency, source.DataTime)
		if err != nil {
			return result, err
		}
		existing, err := p.Store.Unified(ctx, p.SpaceID, p.UnifiedDatasetID, source.SubjectID, source.Frequency, source.DataTime)
		if err != nil {
			return result, err
		}
		decision, err := p.Resolver.Resolve(candidates, existing)
		if err != nil {
			return result, err
		}
		if decision.Row == nil {
			continue
		}
		if p.ResolutionGuard != nil {
			if err := p.ResolutionGuard.BeforeUnifiedWrite(ctx); err != nil {
				return result, fmt.Errorf("validate resolution lease: %w", err)
			}
		}
		decision.Row.SourceDatasetID = p.SourceDatasetID
		if datasetID := p.SourceDatasets[decision.Row.ProviderID]; datasetID != "" {
			decision.Row.SourceDatasetID = datasetID
		}
		if err := p.Store.WriteUnifiedKline(ctx, p.UnifiedDatasetID, *decision.Row); err != nil {
			return result, fmt.Errorf("persist unified kline: %w", err)
		}
		if len(decision.Events) > 0 && p.QualityDatasetID != "" {
			writer, ok := p.Store.(QualityEventStore)
			if !ok {
				return result, fmt.Errorf("quality event store is required")
			}
			if err := writer.WriteQualityEvents(ctx, p.QualityDatasetID, *decision.Row, decision.Events); err != nil {
				return result, fmt.Errorf("persist quality event: %w", err)
			}
		}
		result.UnifiedRows++
	}
	return result, nil
}
