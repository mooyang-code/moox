package marketfetch

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	StockCNInstrumentDatasetID = "stock_cn_instruments"
	StockCNDataSourceID        = "stock_cn"
)

type InstrumentStorage interface {
	Storage
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	BindDatasetSubject(context.Context, *storagepb.DatasetSubject) error
}

type InstrumentPipeline struct {
	Registry          *marketdata.Registry
	Storage           InstrumentStorage
	CandidateChain    []string
	MarketID          string
	DatasetID         string
	TargetDatasetID   string
	DataSourceID      string
	RequiredExchanges []string
	MinimumCount      int
	Now               func() time.Time
}

type InstrumentPipelineRequest struct {
	RequestID  string    `json:"request_id"`
	SnapshotAt time.Time `json:"snapshot_at"`
}

type InstrumentPipelineResult struct {
	SnapshotID       string         `json:"snapshot_id"`
	SourceProvider   string         `json:"source_provider"`
	FetchedAt        time.Time      `json:"fetched_at"`
	Complete         bool           `json:"complete"`
	PageCount        int            `json:"page_count"`
	InstrumentCount  int            `json:"instrument_count"`
	ExchangeCounts   map[string]int `json:"exchange_counts"`
	ActiveSetVersion string         `json:"active_instrument_set_version"`
}

func (p *InstrumentPipeline) Execute(ctx context.Context, req InstrumentPipelineRequest) (InstrumentPipelineResult, error) {
	if p == nil || p.Registry == nil || p.Storage == nil {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument pipeline is not initialized")
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument request_id is required")
	}
	if req.SnapshotAt.IsZero() {
		req.SnapshotAt = time.Now().UTC()
		if p.Now != nil {
			req.SnapshotAt = p.Now().UTC()
		}
	}
	marketID := firstNonEmptyString(p.MarketID, StockCNSpaceID)
	chain := uniqueProviders(p.CandidateChain)
	if len(chain) == 0 {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument candidate chain is empty")
	}

	var snapshot marketdata.InstrumentSnapshot
	var lastErr error
	for _, providerID := range chain {
		fetcher, err := p.Registry.InstrumentFetcher(providerID)
		if err != nil {
			lastErr = err
			continue
		}
		guard, err := marketdata.NewFeedGuard(fetcher.InstrumentSpec().RateLimit, nil, nil)
		if err != nil {
			return InstrumentPipelineResult{}, err
		}
		err = guard.Do(ctx, func(callCtx context.Context) error {
			var fetchErr error
			snapshot, fetchErr = fetcher.FetchInstrumentSnapshot(callCtx, marketdata.InstrumentRequest{MarketID: marketdata.MarketID(marketID), SnapshotAt: req.SnapshotAt.UTC(), RequestID: req.RequestID})
			return fetchErr
		})
		if err == nil {
			err = p.validateSnapshot(snapshot, marketID)
		}
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = fmt.Errorf("instrument provider %s: %w", providerID, err)
		if !marketdata.CanFallback(ctx, err) && !strings.Contains(err.Error(), "snapshot") {
			return InstrumentPipelineResult{}, lastErr
		}
	}
	if lastErr != nil {
		return InstrumentPipelineResult{}, lastErr
	}
	if err := p.persistSnapshot(ctx, snapshot); err != nil {
		return InstrumentPipelineResult{}, err
	}
	return InstrumentPipelineResult{SnapshotID: snapshot.SnapshotID, SourceProvider: snapshot.SourceProvider, FetchedAt: snapshot.FetchedAt, Complete: snapshot.Complete, PageCount: snapshot.PageCount, InstrumentCount: len(snapshot.Instruments), ExchangeCounts: cloneCounts(snapshot.ExchangeCounts), ActiveSetVersion: snapshot.SnapshotID}, nil
}

func (p *InstrumentPipeline) validateSnapshot(snapshot marketdata.InstrumentSnapshot, marketID string) error {
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return fmt.Errorf("invalid instrument snapshot: %w", err)
	}
	if snapshot.MarketID != marketID {
		return fmt.Errorf("instrument snapshot market %q does not match %q", snapshot.MarketID, marketID)
	}
	if p.MinimumCount > 0 && len(snapshot.Instruments) < p.MinimumCount {
		return fmt.Errorf("instrument snapshot count %d is below minimum %d", len(snapshot.Instruments), p.MinimumCount)
	}
	for _, exchange := range p.RequiredExchanges {
		if snapshot.ExchangeCounts[exchange] <= 0 {
			return fmt.Errorf("instrument snapshot is missing exchange %s", exchange)
		}
	}
	return nil
}

func (p *InstrumentPipeline) persistSnapshot(ctx context.Context, snapshot marketdata.InstrumentSnapshot) error {
	datasetID := firstNonEmptyString(p.DatasetID, StockCNInstrumentDatasetID)
	targetDatasetID := firstNonEmptyString(p.TargetDatasetID, StockCNDatasetID)
	dataSourceID := firstNonEmptyString(p.DataSourceID, StockCNDataSourceID)
	existing, err := p.Storage.ListDatasetSubjects(ctx, StockCNSpaceID, datasetID)
	if err != nil {
		return fmt.Errorf("list active instrument set: %w", err)
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(snapshot.Instruments))
	present := make(map[string]marketdata.Instrument, len(snapshot.Instruments))
	sort.Slice(snapshot.Instruments, func(i, j int) bool { return snapshot.Instruments[i].SubjectID < snapshot.Instruments[j].SubjectID })
	for _, instrument := range snapshot.Instruments {
		present[instrument.SubjectID] = instrument
		rows = append(rows, instrumentRecordRow(datasetID, snapshot, instrument))
	}
	for start := 0; start < len(rows); start += 25 {
		end := start + 25
		if end > len(rows) {
			end = len(rows)
		}
		if err := p.Storage.UpsertFields(ctx, rows[start:end]); err != nil {
			return fmt.Errorf("write instrument snapshot rows %d..%d: %w", start, end, err)
		}
	}
	registerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan marketdata.Instrument)
	errCh := make(chan error, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for instrument := range jobs {
				if err := p.Storage.RegisterDataSubject(registerCtx, instrumentRegistration(dataSourceID, datasetID, targetDatasetID, snapshot, instrument)); err != nil {
					select {
					case errCh <- fmt.Errorf("register instrument %s: %w", instrument.SubjectID, err):
						cancel()
					default:
					}
					return
				}
			}
		}()
	}
	for _, instrument := range snapshot.Instruments {
		select {
		case jobs <- instrument:
		case <-registerCtx.Done():
			break
		}
		if registerCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	return p.reconcileMissing(ctx, existing, present, datasetID, targetDatasetID, snapshot)
}

func (p *InstrumentPipeline) reconcileMissing(ctx context.Context, existing []*storagepb.DatasetSubject, present map[string]marketdata.Instrument, datasetID, targetDatasetID string, snapshot marketdata.InstrumentSnapshot) error {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	missingDate := snapshot.FetchedAt.In(location).Format("2006-01-02")
	for _, membership := range existing {
		if membership == nil || !strings.EqualFold(membership.GetStatus(), "active") {
			continue
		}
		if _, ok := present[membership.GetSubjectId()]; ok {
			continue
		}
		copy := *membership
		copy.Attributes = cloneAttributes(membership.GetAttributes())
		missingCount, _ := strconv.Atoi(copy.Attributes["missing_complete_snapshot_count"])
		if copy.Attributes["last_missing_snapshot_date"] != missingDate {
			missingCount++
		}
		copy.Attributes["missing_complete_snapshot_count"] = strconv.Itoa(missingCount)
		copy.Attributes["last_missing_snapshot_id"] = snapshot.SnapshotID
		copy.Attributes["last_missing_snapshot_date"] = missingDate
		if missingCount >= 2 {
			copy.Status = "disabled"
		}
		if err := p.Storage.BindDatasetSubject(ctx, &copy); err != nil {
			return fmt.Errorf("update missing instrument %s: %w", copy.SubjectId, err)
		}
		if missingCount >= 2 {
			target := copy
			target.DatasetId = targetDatasetID
			if err := p.Storage.BindDatasetSubject(ctx, &target); err != nil {
				return fmt.Errorf("disable target instrument %s: %w", target.SubjectId, err)
			}
		}
	}
	return nil
}

func instrumentRegistration(dataSourceID, datasetID, targetDatasetID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RegisterDataSubjectReq {
	attributes := map[string]string{"exchange": instrument.Exchange, "instrument_type": "equity", "provider_symbol": instrument.ProviderSymbol, "snapshot_id": snapshot.SnapshotID, "source_provider": snapshot.SourceProvider}
	return &storagepb.RegisterDataSubjectReq{SpaceId: StockCNSpaceID, DataSourceId: dataSourceID, ExternalSymbol: instrument.ProviderSymbol, Subject: &storagepb.Subject{SpaceId: StockCNSpaceID, SubjectId: instrument.SubjectID, SubjectType: "stock", Name: firstNonEmptyString(instrument.Name, instrument.SubjectID), Market: "CN", Currency: "CNY", Timezone: "Asia/Shanghai", Status: "active", Attributes: attributes}, DatasetBindings: []*storagepb.DatasetSubject{{SpaceId: StockCNSpaceID, DatasetId: datasetID, SubjectId: instrument.SubjectID, SubjectRole: "record", Status: "active", Attributes: map[string]string{"active_instrument_set_version": snapshot.SnapshotID, "missing_complete_snapshot_count": "0"}}, {SpaceId: StockCNSpaceID, DatasetId: targetDatasetID, SubjectId: instrument.SubjectID, SubjectRole: "normal", Status: "active", Attributes: map[string]string{"active_instrument_set_version": snapshot.SnapshotID}}}}
}

func instrumentRecordRow(datasetID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{Key: &storagepb.RowKey{SpaceId: StockCNSpaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: instrument.SubjectID, Version: snapshot.SnapshotID}}}, Fields: []*storagepb.FieldValue{stringValue("security_code", instrument.SubjectID), stringValue("provider_symbol", instrument.ProviderSymbol), stringValue("exchange", instrument.Exchange), stringValue("instrument_name", instrument.Name), stringValue("instrument_status", instrument.Status), stringValue("snapshot_id", snapshot.SnapshotID), stringValue("source_provider", snapshot.SourceProvider), timeValue("fetched_at", snapshot.FetchedAt)}}
}

func uniqueProviders(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneAttributes(input map[string]string) map[string]string {
	output := make(map[string]string, len(input)+2)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneCounts(input map[string]int) map[string]int {
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
