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
	"google.golang.org/protobuf/proto"
)

const (
	StockCNInstrumentDatasetID = "stock_cn_instruments"
	StockCNDataSourceID        = "stock_cn"
)

type InstrumentStorage interface {
	Storage
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	BindDatasetSubject(context.Context, *storagepb.DatasetSubject) error
	StageDatasetSubjectSet(context.Context, string, string, []*storagepb.DatasetSubject) error
	ActivateDatasetSubjectSet(context.Context, string, string) error
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
		// Instrument providers own pagination and apply their feed guard to each
		// physical page request. Wrapping the whole snapshot here would turn the
		// per-request timeout into a deadline for thousands of instruments.
		snapshot, err = fetcher.FetchInstrumentSnapshot(ctx, marketdata.InstrumentRequest{MarketID: marketdata.MarketID(marketID), SnapshotAt: req.SnapshotAt.UTC(), RequestID: req.RequestID})
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
	spaceID := firstNonEmptyString(p.MarketID, StockCNSpaceID)
	datasetID := firstNonEmptyString(p.DatasetID, StockCNInstrumentDatasetID)
	targetDatasetID := firstNonEmptyString(p.TargetDatasetID, StockCNDatasetID)
	dataSourceID := firstNonEmptyString(p.DataSourceID, StockCNDataSourceID)
	existing, err := p.Storage.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("list active instrument set: %w", err)
	}
	targetExisting, err := p.Storage.ListDatasetSubjects(ctx, spaceID, targetDatasetID)
	if err != nil {
		return fmt.Errorf("list target instrument set: %w", err)
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(snapshot.Instruments))
	present := make(map[string]marketdata.Instrument, len(snapshot.Instruments))
	sort.Slice(snapshot.Instruments, func(i, j int) bool { return snapshot.Instruments[i].SubjectID < snapshot.Instruments[j].SubjectID })
	for _, instrument := range snapshot.Instruments {
		present[instrument.SubjectID] = instrument
		rows = append(rows, instrumentRecordRow(spaceID, datasetID, snapshot, instrument))
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
				if err := p.Storage.RegisterDataSubject(registerCtx, instrumentRegistration(spaceID, dataSourceID, snapshot, instrument)); err != nil {
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
	bindings := stagedInstrumentBindings(spaceID, existing, targetExisting, present, datasetID, targetDatasetID, snapshot)
	if err := p.Storage.StageDatasetSubjectSet(ctx, spaceID, snapshot.SnapshotID, bindings); err != nil {
		return fmt.Errorf("stage active instrument set %s: %w", snapshot.SnapshotID, err)
	}
	if err := p.Storage.ActivateDatasetSubjectSet(ctx, spaceID, snapshot.SnapshotID); err != nil {
		return fmt.Errorf("activate active instrument set %s: %w", snapshot.SnapshotID, err)
	}
	return nil
}

// stagedInstrumentBindings builds the full desired state for both datasets.
// Every row is sent as one inactive staging set; the storage activation RPC
// swaps both datasets in one transaction, so an interrupted snapshot cannot
// expose a prefix of the new universe.
func stagedInstrumentBindings(spaceID string, existing, targetExisting []*storagepb.DatasetSubject, present map[string]marketdata.Instrument, datasetID, targetDatasetID string, snapshot marketdata.InstrumentSnapshot) []*storagepb.DatasetSubject {
	desired := make(map[string]*storagepb.DatasetSubject, len(existing)+len(targetExisting)+len(present)*2)
	for _, membership := range append(append([]*storagepb.DatasetSubject(nil), existing...), targetExisting...) {
		if membership == nil {
			continue
		}
		key := membership.GetDatasetId() + "\x00" + membership.GetSubjectId()
		desired[key] = proto.Clone(membership).(*storagepb.DatasetSubject)
	}
	setPresent := func(dataset, role, subjectID string) *storagepb.DatasetSubject {
		key := dataset + "\x00" + subjectID
		item := desired[key]
		if item == nil {
			item = &storagepb.DatasetSubject{SpaceId: spaceID, DatasetId: dataset, SubjectId: subjectID, SubjectRole: role}
			desired[key] = item
		}
		item.Status = "active"
		item.Attributes = cloneAttributes(item.GetAttributes())
		item.Attributes["active_instrument_set_version"] = snapshot.SnapshotID
		if dataset == datasetID {
			item.Attributes["missing_complete_snapshot_count"] = "0"
			delete(item.Attributes, "last_missing_snapshot_id")
			delete(item.Attributes, "last_missing_snapshot_date")
		}
		return item
	}
	subjectIDs := make([]string, 0, len(present))
	for subjectID := range present {
		subjectIDs = append(subjectIDs, subjectID)
	}
	sort.Strings(subjectIDs)
	for _, subjectID := range subjectIDs {
		setPresent(datasetID, "record", subjectID)
		setPresent(targetDatasetID, "normal", subjectID)
	}
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
		key := membership.GetDatasetId() + "\x00" + membership.GetSubjectId()
		updated := desired[key]
		updated.Attributes = cloneAttributes(updated.GetAttributes())
		missingCount, _ := strconv.Atoi(updated.Attributes["missing_complete_snapshot_count"])
		if updated.Attributes["last_missing_snapshot_date"] != missingDate {
			missingCount++
		}
		updated.Attributes["missing_complete_snapshot_count"] = strconv.Itoa(missingCount)
		updated.Attributes["last_missing_snapshot_id"] = snapshot.SnapshotID
		updated.Attributes["last_missing_snapshot_date"] = missingDate
		if missingCount >= 2 {
			updated.Status = "disabled"
			targetKey := targetDatasetID + "\x00" + membership.GetSubjectId()
			target := desired[targetKey]
			if target == nil {
				target = proto.Clone(updated).(*storagepb.DatasetSubject)
			}
			target.DatasetId = targetDatasetID
			target.SubjectRole = "normal"
			desired[targetKey] = target
		}
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bindings := make([]*storagepb.DatasetSubject, 0, len(keys))
	for _, key := range keys {
		bindings = append(bindings, desired[key])
	}
	return bindings
}

type instrumentBindingChange struct {
	next     *storagepb.DatasetSubject
	previous *storagepb.DatasetSubject
}

func activeInstrumentBindings(existing, targetExisting []*storagepb.DatasetSubject, present map[string]marketdata.Instrument, datasetID, targetDatasetID string, snapshot marketdata.InstrumentSnapshot) []instrumentBindingChange {
	previous := make(map[string]*storagepb.DatasetSubject, len(existing)+len(targetExisting))
	for _, membership := range append(append([]*storagepb.DatasetSubject(nil), existing...), targetExisting...) {
		if membership != nil {
			previous[membership.GetDatasetId()+"\x00"+membership.GetSubjectId()] = membership
		}
	}
	changes := make([]instrumentBindingChange, 0, len(present)*2)
	subjectIDs := make([]string, 0, len(present))
	for subjectID := range present {
		subjectIDs = append(subjectIDs, subjectID)
	}
	sort.Strings(subjectIDs)
	for _, subjectID := range subjectIDs {
		for _, item := range []struct{ datasetID, role string }{{datasetID, "record"}, {targetDatasetID, "normal"}} {
			key := item.datasetID + "\x00" + subjectID
			var next *storagepb.DatasetSubject
			if old := previous[key]; old != nil {
				next = proto.Clone(old).(*storagepb.DatasetSubject)
			} else {
				next = &storagepb.DatasetSubject{SpaceId: StockCNSpaceID, DatasetId: item.datasetID, SubjectId: subjectID, SubjectRole: item.role}
			}
			next.Status = "active"
			next.Attributes = cloneAttributes(next.GetAttributes())
			next.Attributes["active_instrument_set_version"] = snapshot.SnapshotID
			if item.datasetID == datasetID {
				next.Attributes["missing_complete_snapshot_count"] = "0"
				delete(next.Attributes, "last_missing_snapshot_id")
				delete(next.Attributes, "last_missing_snapshot_date")
			}
			changes = append(changes, instrumentBindingChange{next: next, previous: previous[key]})
		}
	}
	return changes
}

func (p *InstrumentPipeline) missingInstrumentBindings(existing, targetExisting []*storagepb.DatasetSubject, present map[string]marketdata.Instrument, datasetID, targetDatasetID string, snapshot marketdata.InstrumentSnapshot) []instrumentBindingChange {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	missingDate := snapshot.FetchedAt.In(location).Format("2006-01-02")
	targetPrevious := make(map[string]*storagepb.DatasetSubject, len(targetExisting))
	for _, membership := range targetExisting {
		if membership != nil {
			targetPrevious[membership.GetSubjectId()] = membership
		}
	}
	changes := make([]instrumentBindingChange, 0)
	for _, membership := range existing {
		if membership == nil || !strings.EqualFold(membership.GetStatus(), "active") {
			continue
		}
		if _, ok := present[membership.GetSubjectId()]; ok {
			continue
		}
		updated := proto.Clone(membership).(*storagepb.DatasetSubject)
		updated.Attributes = cloneAttributes(membership.GetAttributes())
		missingCount, _ := strconv.Atoi(updated.Attributes["missing_complete_snapshot_count"])
		if updated.Attributes["last_missing_snapshot_date"] != missingDate {
			missingCount++
		}
		updated.Attributes["missing_complete_snapshot_count"] = strconv.Itoa(missingCount)
		updated.Attributes["last_missing_snapshot_id"] = snapshot.SnapshotID
		updated.Attributes["last_missing_snapshot_date"] = missingDate
		if missingCount >= 2 {
			updated.Status = "disabled"
		}
		changes = append(changes, instrumentBindingChange{next: updated, previous: membership})
		if missingCount >= 2 {
			target := proto.Clone(updated).(*storagepb.DatasetSubject)
			target.DatasetId = targetDatasetID
			target.SubjectRole = "normal"
			changes = append(changes, instrumentBindingChange{next: target, previous: targetPrevious[target.GetSubjectId()]})
		}
	}
	return changes
}

func (p *InstrumentPipeline) commitInstrumentBindings(ctx context.Context, changes []instrumentBindingChange) error {
	applied := make([]instrumentBindingChange, 0, len(changes))
	for _, change := range changes {
		if err := p.Storage.BindDatasetSubject(ctx, change.next); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			// A transport/cache-refresh error can be returned after Storage has
			// committed the current binding. Compensate it as well as the calls
			// that returned success.
			rollbackErr := p.rollbackInstrumentBindings(rollbackCtx, append(applied, change))
			cancel()
			if rollbackErr != nil {
				return fmt.Errorf("bind %s/%s: %v; rollback: %w", change.next.GetDatasetId(), change.next.GetSubjectId(), err, rollbackErr)
			}
			return fmt.Errorf("bind %s/%s: %w", change.next.GetDatasetId(), change.next.GetSubjectId(), err)
		}
		applied = append(applied, change)
	}
	return nil
}

func (p *InstrumentPipeline) rollbackInstrumentBindings(ctx context.Context, applied []instrumentBindingChange) error {
	var rollbackErr error
	for index := len(applied) - 1; index >= 0; index-- {
		change := applied[index]
		previous := change.previous
		if previous == nil {
			previous = proto.Clone(change.next).(*storagepb.DatasetSubject)
			previous.Status = "disabled"
			previous.Attributes = cloneAttributes(previous.GetAttributes())
			delete(previous.Attributes, "active_instrument_set_version")
		}
		if err := p.Storage.BindDatasetSubject(ctx, previous); err != nil && rollbackErr == nil {
			rollbackErr = err
		}
	}
	return rollbackErr
}

func instrumentRegistration(spaceID, dataSourceID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RegisterDataSubjectReq {
	attributes := map[string]string{"exchange": instrument.Exchange, "instrument_type": "equity", "provider_symbol": instrument.ProviderSymbol, "snapshot_id": snapshot.SnapshotID, "source_provider": snapshot.SourceProvider}
	return &storagepb.RegisterDataSubjectReq{SpaceId: spaceID, DataSourceId: dataSourceID, ExternalSymbol: instrument.ProviderSymbol, Subject: &storagepb.Subject{SpaceId: spaceID, SubjectId: instrument.SubjectID, SubjectType: "stock", Name: firstNonEmptyString(instrument.Name, instrument.SubjectID), Market: "CN", Currency: "CNY", Timezone: "Asia/Shanghai", Status: "active", Attributes: attributes}}
}

func instrumentRecordRow(spaceID, datasetID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RowFieldUpsert {
	return &storagepb.RowFieldUpsert{Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: instrument.SubjectID, Version: snapshot.SnapshotID}}}, Fields: []*storagepb.FieldValue{stringValue("security_code", instrument.SubjectID), stringValue("provider_symbol", instrument.ProviderSymbol), stringValue("exchange", instrument.Exchange), stringValue("instrument_name", instrument.Name), stringValue("instrument_status", instrument.Status), stringValue("snapshot_id", snapshot.SnapshotID), stringValue("source_provider", snapshot.SourceProvider), timeValue("fetched_at", snapshot.FetchedAt)}}
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
