package marketfetch

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

// InstrumentWriteSpec contains the metadata contract needed to materialize a
// provider snapshot. Provider-specific fetchers return only normalized
// instruments; the composition root supplies the destination DataSource and
// Subject/Dataset binding policy here.
type InstrumentWriteSpec struct {
	DataSourceID  string
	SubjectType   string
	SubjectMarket string
	Currency      string
	SeriesTag     string
}

func (s InstrumentWriteSpec) validate() error {
	for name, value := range map[string]string{
		"data_source_id": s.DataSourceID,
		"subject_type":   s.SubjectType,
		"subject_market": s.SubjectMarket,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

// InstrumentPipelineRequest is the canonical input for one source snapshot.
// Shards are selected after fetching the complete snapshot so shard zero can
// reconcile removals without trusting a partial provider response.
type InstrumentPipelineRequest struct {
	SpaceID       string
	DatasetID     string
	SourceEventID string
	SourceKey     marketdata.SourceKey
	Request       marketdata.InstrumentRequest
	WriteSpec     InstrumentWriteSpec
	ShardIndex    int
	ShardCount    int
}

type InstrumentPipelineResult struct {
	RowsWritten     int
	Instruments     int
	SnapshotVersion string
	SourceKey       marketdata.SourceKey
}

type instrumentMembershipStorage interface {
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	BindDatasetSubject(context.Context, *storagepb.DatasetSubject) error
}

// InstrumentPipeline owns the full snapshot boundary: fetch, validate,
// shard, write records, register subjects and optionally deactivate stale
// memberships. No provider-specific type is allowed past the fetcher.
type InstrumentPipeline struct {
	Fetcher marketdata.InstrumentFetcher
	Storage Storage
}

func (p *InstrumentPipeline) FetchAndWrite(ctx context.Context, request InstrumentPipelineRequest) (InstrumentPipelineResult, error) {
	if p == nil || p.Fetcher == nil || p.Storage == nil {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument pipeline is not initialized")
	}
	if strings.TrimSpace(request.SpaceID) == "" || strings.TrimSpace(request.DatasetID) == "" {
		return InstrumentPipelineResult{}, fmt.Errorf("space_id and dataset_id are required")
	}
	if strings.TrimSpace(request.SourceEventID) == "" {
		return InstrumentPipelineResult{}, fmt.Errorf("source_event_id is required")
	}
	if err := request.WriteSpec.validate(); err != nil {
		return InstrumentPipelineResult{}, err
	}
	if err := request.Request.Validate(); err != nil {
		return InstrumentPipelineResult{}, err
	}
	descriptor := p.Fetcher.Descriptor()
	if request.SourceKey != descriptor.Key() {
		return InstrumentPipelineResult{}, fmt.Errorf("source_key %s does not match fetcher %s", request.SourceKey, descriptor.Key())
	}
	spec := p.Fetcher.InstrumentSpec()
	if err := spec.Validate(); err != nil {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument fetcher spec: %w", err)
	}
	if !spec.SupportsMarketInstrument(request.Request.MarketID, request.Request.InstrumentType) {
		return InstrumentPipelineResult{}, fmt.Errorf("market/instrument %s/%s is not supported by %s", request.Request.MarketID, request.Request.InstrumentType, descriptor.Key())
	}
	if request.Request.Page != 0 || request.Request.PageSize != 0 {
		if !spec.SupportsPaging {
			return InstrumentPipelineResult{}, fmt.Errorf("%w: source %s does not support instrument paging", marketdata.ErrNotSupported, descriptor.Key())
		}
	}
	if !spec.SupportsFull && request.Request.Page == 0 {
		return InstrumentPipelineResult{}, fmt.Errorf("%w: source %s does not support full instrument snapshots", marketdata.ErrNotSupported, descriptor.Key())
	}
	if request.ShardCount < 0 || request.ShardIndex < 0 || (request.ShardCount == 0 && request.ShardIndex != 0) || (request.ShardCount > 0 && request.ShardIndex >= request.ShardCount) {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument snapshot shard %d is outside [0,%d)", request.ShardIndex, request.ShardCount)
	}
	if request.ShardCount > 1 {
		return InstrumentPipelineResult{}, fmt.Errorf("%w: instrument snapshots require one complete invocation; sharded activation has no durable barrier", marketdata.ErrNotSupported)
	}
	membershipStorage, ok := p.Storage.(instrumentMembershipStorage)
	if !ok {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument snapshot storage must support dataset membership activation")
	}

	snapshot, err := p.Fetcher.FetchInstruments(ctx, request.Request)
	if err != nil {
		return InstrumentPipelineResult{}, err
	}
	if err := snapshot.Validate(); err != nil {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument snapshot: %w", err)
	}
	if snapshot.MarketID != request.Request.MarketID || snapshot.InstrumentType != request.Request.InstrumentType {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument snapshot identity %s/%s does not match request %s/%s", snapshot.MarketID, snapshot.InstrumentType, request.Request.MarketID, request.Request.InstrumentType)
	}
	if len(snapshot.Items) == 0 {
		return InstrumentPipelineResult{}, fmt.Errorf("%w: source %s returned an empty instrument snapshot", marketdata.ErrUnavailable, descriptor.Key())
	}

	selected := snapshot.Items
	rows, registrations, err := buildInstrumentWrites(request, descriptor, snapshot.Version, selected)
	if err != nil {
		return InstrumentPipelineResult{}, err
	}
	if len(rows) > 0 {
		if err := upsertInstrumentRows(ctx, p.Storage, rows, request.SourceEventID); err != nil {
			return InstrumentPipelineResult{}, fmt.Errorf("write instrument rows: %w", err)
		}
	}
	for _, registration := range registrations {
		if err := p.Storage.RegisterDataSubject(ctx, registration); err != nil {
			return InstrumentPipelineResult{}, fmt.Errorf("register instrument %s: %w", registration.GetSubject().GetSubjectId(), err)
		}
	}
	// Keep newly registered bindings invisible to symbol readers until the full
	// snapshot has been written. A failed invocation therefore leaves pending
	// rows rather than partially activating the exchange catalogue.
	for _, registration := range registrations {
		for _, binding := range registration.GetDatasetBindings() {
			active := proto.Clone(binding).(*storagepb.DatasetSubject)
			active.Status = "active"
			if err := membershipStorage.BindDatasetSubject(ctx, active); err != nil {
				return InstrumentPipelineResult{}, fmt.Errorf("activate instrument membership %s: %w", active.GetSubjectId(), err)
			}
		}
	}
	if err := reconcileInstrumentMemberships(ctx, p.Storage, request, descriptor, snapshot.Version, snapshot.Items); err != nil {
		return InstrumentPipelineResult{}, err
	}
	return InstrumentPipelineResult{RowsWritten: len(rows), Instruments: len(selected), SnapshotVersion: snapshot.Version, SourceKey: descriptor.Key()}, nil
}

func upsertInstrumentRows(ctx context.Context, storage Storage, rows []*storagepb.RowFieldUpsert, sourceEventID string) error {
	if writer, ok := storage.(sourceStorage); ok {
		return writer.UpsertFieldsWithSource(ctx, rows, sourceEventID)
	}
	return storage.UpsertFields(ctx, rows)
}

func buildInstrumentWrites(request InstrumentPipelineRequest, descriptor marketdata.ProviderDescriptor, version string, instruments []marketdata.Instrument) ([]*storagepb.RowFieldUpsert, []*storagepb.RegisterDataSubjectReq, error) {
	rows := make([]*storagepb.RowFieldUpsert, 0, len(instruments))
	registrations := make([]*storagepb.RegisterDataSubjectReq, 0, len(instruments))
	seen := make(map[string]struct{}, len(instruments))
	for index, item := range instruments {
		subjectID := strings.TrimSpace(item.SubjectID)
		providerSymbol := strings.TrimSpace(item.ProviderSymbol)
		if subjectID == "" || providerSymbol == "" {
			return nil, nil, fmt.Errorf("instrument %d subject_id and provider_symbol are required", index)
		}
		if _, exists := seen[subjectID]; exists {
			return nil, nil, fmt.Errorf("duplicate instrument subject_id %q", subjectID)
		}
		seen[subjectID] = struct{}{}
		status := strings.TrimSpace(item.Status)
		if status == "" {
			status = "active"
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = subjectID
		}
		fields := []*storagepb.FieldValue{
			stringField("symbol", subjectID),
			stringField("external_symbol", providerSymbol),
			stringField("name", name),
			stringField("exchange_id", item.ExchangeID),
			stringField("status", status),
			stringField("instrument_type", item.InstrumentType),
		}
		rows = append(rows, &storagepb.RowFieldUpsert{
			Key:    &storagepb.RowKey{SpaceId: request.SpaceID, DatasetId: request.DatasetID, Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: subjectID, Version: version}}},
			Fields: fields,
			Attributes: map[string]*storagepb.TypedValue{
				"provider_id":     stringValue(descriptor.ProviderID),
				"source_id":       stringValue(descriptor.SourceID),
				"provider_symbol": stringValue(providerSymbol),
				"exchange_id":     stringValue(item.ExchangeID),
				"instrument_type": stringValue(item.InstrumentType),
				"series_tag":      stringValue(request.WriteSpec.SeriesTag),
			},
		})
		attributes := map[string]string{
			"provider_id":     descriptor.ProviderID,
			"source_id":       descriptor.SourceID,
			"provider_symbol": providerSymbol,
			"exchange_id":     item.ExchangeID,
			"instrument_type": item.InstrumentType,
		}
		if request.WriteSpec.SeriesTag != "" {
			attributes["series_tag"] = request.WriteSpec.SeriesTag
		}
		registrations = append(registrations, &storagepb.RegisterDataSubjectReq{
			SpaceId: request.SpaceID, DataSourceId: request.WriteSpec.DataSourceID, ExternalSymbol: providerSymbol,
			Subject:         &storagepb.Subject{SpaceId: request.SpaceID, SubjectId: subjectID, SubjectType: request.WriteSpec.SubjectType, Name: name, Market: request.WriteSpec.SubjectMarket, Currency: request.WriteSpec.Currency, Status: status, Attributes: attributes},
			DatasetBindings: []*storagepb.DatasetSubject{{SpaceId: request.SpaceID, DatasetId: request.DatasetID, SubjectId: subjectID, SubjectRole: "normal", Status: "pending"}},
		})
	}
	return rows, registrations, nil
}

func reconcileInstrumentMemberships(ctx context.Context, storage Storage, request InstrumentPipelineRequest, descriptor marketdata.ProviderDescriptor, version string, active []marketdata.Instrument) error {
	membershipStorage, ok := storage.(instrumentMembershipStorage)
	if !ok {
		return nil
	}
	memberships, err := membershipStorage.ListDatasetSubjects(ctx, request.SpaceID, request.DatasetID)
	if err != nil {
		return fmt.Errorf("list instrument memberships: %w", err)
	}
	activeIDs := make(map[string]struct{}, len(active))
	for _, item := range active {
		activeIDs[strings.TrimSpace(item.SubjectID)] = struct{}{}
	}
	stale := make([]*storagepb.DatasetSubject, 0)
	staleInstruments := make([]marketdata.Instrument, 0)
	for _, membership := range memberships {
		if membership == nil || !strings.EqualFold(strings.TrimSpace(membership.GetStatus()), "active") {
			continue
		}
		subjectID := strings.TrimSpace(membership.GetSubjectId())
		if _, exists := activeIDs[subjectID]; exists || subjectID == "" {
			continue
		}
		updated := proto.Clone(membership).(*storagepb.DatasetSubject)
		updated.Status = "disabled"
		stale = append(stale, updated)
		providerSymbol := subjectID
		if value := strings.TrimSpace(membership.GetAttributes()["provider_symbol"]); value != "" {
			providerSymbol = value
		}
		staleInstruments = append(staleInstruments, marketdata.Instrument{SubjectID: subjectID, ProviderSymbol: providerSymbol, Name: subjectID, Status: "disabled", InstrumentType: request.Request.InstrumentType})
	}
	if len(stale) == 0 {
		return nil
	}
	staleRows, _, err := buildInstrumentWrites(request, descriptor, version, staleInstruments)
	if err != nil {
		return fmt.Errorf("build stale instrument rows: %w", err)
	}
	// Stale rows are a separate Storage payload from the fresh snapshot rows;
	// give them a deterministic but distinct event marker so source-event
	// deduplication cannot silently skip the disabled records.
	staleSourceEventID := strings.TrimSpace(request.SourceEventID) + ":stale"
	if err := upsertInstrumentRows(ctx, storage, staleRows, staleSourceEventID); err != nil {
		return fmt.Errorf("write stale instrument rows: %w", err)
	}
	for _, membership := range stale {
		if err := membershipStorage.BindDatasetSubject(ctx, membership); err != nil {
			return fmt.Errorf("disable instrument membership %s: %w", membership.GetSubjectId(), err)
		}
	}
	return nil
}
