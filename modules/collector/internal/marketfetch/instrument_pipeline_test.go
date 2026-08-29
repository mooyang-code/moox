package marketfetch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type instrumentProviderStub struct {
	id       string
	snapshot marketdata.InstrumentSnapshot
	err      error
	calls    int
}

func (p *instrumentProviderStub) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: p.id, DisplayName: p.id, Hosts: []string{p.id + ".test"}}
}

func (p *instrumentProviderStub) InstrumentSpec() marketdata.InstrumentSpec {
	return marketdata.InstrumentSpec{Markets: []string{StockCNSpaceID}, Exchanges: []string{"XSHG", "XSHE", "XBSE"}, FullSnapshot: true, PageSize: 100, RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: time.Second}}
}

func (p *instrumentProviderStub) FetchInstrumentSnapshot(context.Context, marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	p.calls++
	return p.snapshot, p.err
}

type instrumentStorageStub struct {
	mu            sync.Mutex
	rows          []*storagepb.RowFieldUpsert
	registrations []*storagepb.RegisterDataSubjectReq
	existing      []*storagepb.DatasetSubject
	bindings      []*storagepb.DatasetSubject
	writeErr      error
}

func (s *instrumentStorageStub) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.rows = append(s.rows, rows...)
	return s.writeErr
}
func (s *instrumentStorageStub) RegisterDataSubject(_ context.Context, req *storagepb.RegisterDataSubjectReq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registrations = append(s.registrations, req)
	return nil
}
func (s *instrumentStorageStub) ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error) {
	return s.existing, nil
}
func (s *instrumentStorageStub) BindDatasetSubject(_ context.Context, item *storagepb.DatasetSubject) error {
	copy := *item
	s.bindings = append(s.bindings, &copy)
	return nil
}

func TestInstrumentPipelineFallsBackAsWholeSnapshotAndPersistsActiveSet(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	first := &instrumentProviderStub{id: "first", err: marketdata.ErrProtocol}
	second := &instrumentProviderStub{id: "second", snapshot: testInstrumentSnapshot("second", now)}
	require.NoError(t, registry.Register(first))
	require.NoError(t, registry.Register(second))
	storage := &instrumentStorageStub{}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"first", "second"}, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, DataSourceID: StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}
	result, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})
	require.NoError(t, err)
	require.Equal(t, 1, first.calls)
	require.Equal(t, 1, second.calls)
	require.Equal(t, "second", result.SourceProvider)
	require.Equal(t, 3, result.InstrumentCount)
	require.Len(t, storage.rows, 3)
	require.Len(t, storage.registrations, 3)
	for _, registration := range storage.registrations {
		require.Equal(t, StockCNDataSourceID, registration.GetDataSourceId())
		require.Len(t, registration.GetDatasetBindings(), 2)
		require.Equal(t, result.ActiveSetVersion, registration.GetDatasetBindings()[0].GetAttributes()["active_instrument_set_version"])
	}
}

func TestInstrumentPipelineDoesNotPublishPartialSnapshotWhenRecordWriteFails(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	storage := &instrumentStorageStub{writeErr: errors.New("storage unavailable")}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}
	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})
	require.ErrorContains(t, err, "write instrument snapshot rows")
	require.Empty(t, storage.registrations)
}

func TestInstrumentPipelineDisablesOnlyAfterTwoCompleteMisses(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	missing := &storagepb.DatasetSubject{SpaceId: StockCNSpaceID, DatasetId: StockCNInstrumentDatasetID, SubjectId: "601999.XSHG", Status: "active", Attributes: map[string]string{"missing_complete_snapshot_count": "1"}}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	storage := &instrumentStorageStub{existing: []*storagepb.DatasetSubject{missing}}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}
	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})
	require.NoError(t, err)
	require.Len(t, storage.bindings, 2)
	require.Equal(t, "disabled", storage.bindings[0].GetStatus())
	require.Equal(t, StockCNInstrumentDatasetID, storage.bindings[0].GetDatasetId())
	require.Equal(t, "disabled", storage.bindings[1].GetStatus())
	require.Equal(t, StockCNDatasetID, storage.bindings[1].GetDatasetId())
}

func TestInstrumentPipelineDoesNotCountTwoMissingSnapshotsOnTheSameDay(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	missing := &storagepb.DatasetSubject{SpaceId: StockCNSpaceID, DatasetId: StockCNInstrumentDatasetID, SubjectId: "601999.XSHG", Status: "active", Attributes: map[string]string{"missing_complete_snapshot_count": "1", "last_missing_snapshot_date": "2026-08-29"}}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	storage := &instrumentStorageStub{existing: []*storagepb.DatasetSubject{missing}}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}
	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})
	require.NoError(t, err)
	require.Len(t, storage.bindings, 1)
	require.Equal(t, "active", storage.bindings[0].GetStatus())
	require.Equal(t, "1", storage.bindings[0].GetAttributes()["missing_complete_snapshot_count"])
}

func testInstrumentSnapshot(provider string, now time.Time) marketdata.InstrumentSnapshot {
	return marketdata.InstrumentSnapshot{SnapshotID: marketdata.SnapshotID(provider, StockCNSpaceID, now), SourceProvider: provider, MarketID: StockCNSpaceID, FetchedAt: now, Complete: true, PageCount: 3, ExchangeCounts: map[string]int{"XSHG": 1, "XSHE": 1, "XBSE": 1}, Instruments: []marketdata.Instrument{{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Exchange: "XSHG", Name: "浦发银行", Status: "active"}, {SubjectID: "000001.XSHE", ProviderSymbol: "sz000001", Exchange: "XSHE", Name: "平安银行", Status: "active"}, {SubjectID: "920000.XBSE", ProviderSymbol: "bj920000", Exchange: "XBSE", Name: "北交所样本", Status: "active"}}}
}
