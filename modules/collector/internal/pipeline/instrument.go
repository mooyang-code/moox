package pipeline

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/providers"
)

type InstrumentStore interface {
	WriteProviderInstruments(context.Context, string, time.Time, []providers.ProviderInstrument) error
	InstrumentCandidates(context.Context, string, []string, string, time.Time) ([]providers.ProviderInstrument, error)
	WriteUnifiedInstrument(context.Context, string, providers.ResolvedInstrument) error
}

type InstrumentPipeline struct {
	Provider                                   providers.InstrumentProvider
	Gate                                       providers.RequestGate
	Store                                      InstrumentStore
	SpaceID, SourceDatasetID, UnifiedDatasetID string
	SourceDatasetIDs                           []string
	SourceDatasets                             map[marketdata.ProviderID]string
	ProviderPriority                           []marketdata.ProviderID
	Generation                                 time.Time
	Now                                        func() time.Time
}

type InstrumentPipelineResult struct {
	Fetched, SourceRows, UnifiedRows, RequestCount int
	Complete                                       bool
	NextCursor                                     string
}

func (p InstrumentPipeline) Run(ctx context.Context, request providers.FetchInstrumentsRequest) (InstrumentPipelineResult, error) {
	if p.Provider == nil || p.Gate == nil || p.Store == nil || p.Generation.IsZero() {
		return InstrumentPipelineResult{}, fmt.Errorf("provider, gate, store and generation are required")
	}
	fetched, err := p.Provider.FetchInstruments(ctx, p.Gate, request)
	if err != nil {
		return InstrumentPipelineResult{}, err
	}
	result := InstrumentPipelineResult{Fetched: len(fetched.Instruments), RequestCount: fetched.RequestCount, Complete: fetched.Complete, NextCursor: fetched.NextCursor}
	if len(fetched.Instruments) == 0 {
		return result, nil
	}
	if err := p.Store.WriteProviderInstruments(ctx, p.SourceDatasetID, p.Generation, fetched.Instruments); err != nil {
		return result, fmt.Errorf("persist provider instruments: %w", err)
	}
	result.SourceRows = len(fetched.Instruments)
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	for _, source := range fetched.Instruments {
		datasets := p.SourceDatasetIDs
		if len(datasets) == 0 {
			datasets = []string{p.SourceDatasetID}
		}
		candidates, err := p.Store.InstrumentCandidates(ctx, p.SpaceID, datasets, source.SubjectID, p.Generation)
		if err != nil {
			return result, err
		}
		winner := chooseInstrument(candidates, p.ProviderPriority)
		if winner == nil {
			continue
		}
		sourceDataset := p.SourceDatasets[winner.ProviderID]
		if sourceDataset == "" {
			sourceDataset = p.SourceDatasetID
		}
		if err := p.Store.WriteUnifiedInstrument(ctx, p.UnifiedDatasetID, providers.ResolvedInstrument{ProviderInstrument: *winner, SourceDatasetID: sourceDataset, QualityStatus: "accepted", Generation: p.Generation, ResolvedAt: now}); err != nil {
			return result, err
		}
		result.UnifiedRows++
	}
	return result, nil
}

func chooseInstrument(values []providers.ProviderInstrument, priority []marketdata.ProviderID) *providers.ProviderInstrument {
	for _, id := range priority {
		for index := range values {
			if values[index].ProviderID == id {
				value := values[index]
				return &value
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	value := values[0]
	return &value
}
