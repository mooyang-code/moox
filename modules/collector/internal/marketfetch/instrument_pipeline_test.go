package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type instrumentProviderStub struct {
	id       string
	snapshot marketdata.InstrumentSnapshot
	err      error
	calls    int
	delay    time.Duration
	timeout  time.Duration
}

func (p *instrumentProviderStub) Descriptor() marketdata.ProviderDescriptor {
	return marketdata.ProviderDescriptor{ID: p.id, DisplayName: p.id, Hosts: []string{p.id + ".test"}}
}

func (p *instrumentProviderStub) InstrumentSpec() marketdata.InstrumentSpec {
	timeout := p.timeout
	if timeout == 0 {
		timeout = time.Second
	}
	return marketdata.InstrumentSpec{Markets: []string{StockCNSpaceID}, Exchanges: []string{"XSHG", "XSHE", "XBSE"}, FullSnapshot: true, PageSize: 100, RateLimit: marketdata.RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Second, RequestTimeout: timeout}}
}

func (p *instrumentProviderStub) FetchInstrumentSnapshot(ctx context.Context, _ marketdata.InstrumentRequest) (marketdata.InstrumentSnapshot, error) {
	p.calls++
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return marketdata.InstrumentSnapshot{}, ctx.Err()
		}
	}
	return p.snapshot, p.err
}

type instrumentStorageStub struct {
	mu              sync.Mutex
	rows            []*storagepb.RowFieldUpsert
	registrations   []*storagepb.RegisterDataSubjectReq
	existing        []*storagepb.DatasetSubject
	bindings        []*storagepb.DatasetSubject
	stagedBindings  []*storagepb.DatasetSubject
	writeErr        error
	registerErrAt   int
	bindErrAt       int
	bindCommitErrAt int
	stageErr        error
	activateErr     error
	registerCalls   int
	bindCalls       int
	stageCalls      int
	activateCalls   int
}

type shardedInstrumentStorageStub struct {
	*instrumentStorageStub
	stagedSetIDs      []string
	allStagedBindings []*storagepb.DatasetSubject
}

func (s *shardedInstrumentStorageStub) StageDatasetSubjectSet(ctx context.Context, spaceID, setID string, items []*storagepb.DatasetSubject) error {
	if err := s.instrumentStorageStub.StageDatasetSubjectSet(ctx, spaceID, setID, items); err != nil {
		return err
	}
	s.stagedSetIDs = append(s.stagedSetIDs, setID)
	s.allStagedBindings = append(s.allStagedBindings, items...)
	return nil
}

func (s *shardedInstrumentStorageStub) ActivateDatasetSubjectSetWithCount(ctx context.Context, spaceID, setID string) (int, error) {
	if len(s.stagedSetIDs) < 2 {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.bindings = append([]*storagepb.DatasetSubject(nil), s.allStagedBindings...)
	return len(s.bindings), nil
}

func (s *instrumentStorageStub) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, rows...)
	return s.writeErr
}
func (s *instrumentStorageStub) RegisterDataSubject(_ context.Context, req *storagepb.RegisterDataSubjectReq) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registerCalls++
	if s.registerErrAt > 0 && s.registerCalls == s.registerErrAt {
		return errors.New("register failed")
	}
	s.registrations = append(s.registrations, req)
	return nil
}
func (s *instrumentStorageStub) ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error) {
	return s.existing, nil
}
func (s *instrumentStorageStub) BindDatasetSubject(_ context.Context, item *storagepb.DatasetSubject) error {
	s.bindCalls++
	if s.bindErrAt > 0 && s.bindCalls == s.bindErrAt {
		return errors.New("bind failed")
	}
	s.bindings = append(s.bindings, item)
	if s.bindCommitErrAt > 0 && s.bindCalls == s.bindCommitErrAt {
		return errors.New("bind response failed after commit")
	}
	return nil
}
func (s *instrumentStorageStub) StageDatasetSubjectSet(_ context.Context, _ string, _ string, items []*storagepb.DatasetSubject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stageCalls++
	if s.stageErr != nil {
		return s.stageErr
	}
	s.stagedBindings = append([]*storagepb.DatasetSubject(nil), items...)
	return nil
}
func (s *instrumentStorageStub) ActivateDatasetSubjectSet(ctx context.Context, _ string, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activateCalls++
	if s.activateErr != nil {
		return s.activateErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.bindings = append([]*storagepb.DatasetSubject(nil), s.stagedBindings...)
	return nil
}

func TestInstrumentPipelineDoesNotApplyOneTimeoutToWholeSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	provider := &instrumentProviderStub{id: "sina", delay: 30 * time.Millisecond, timeout: 10 * time.Millisecond, snapshot: testInstrumentSnapshot("sina", now)}
	require.NoError(t, registry.Register(provider))
	storage := &instrumentStorageStub{}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}

	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})

	require.NoError(t, err)
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
		require.Empty(t, registration.GetDatasetBindings(), "registration must stage subjects without switching active bindings")
	}
	require.Len(t, storage.stagedBindings, 6)
	require.Len(t, storage.bindings, 6)
	for _, binding := range storage.bindings {
		require.Equal(t, result.ActiveSetVersion, binding.GetAttributes()["active_instrument_set_version"])
	}
}

func TestInstrumentPipelineDistributesSnapshotPersistenceAcrossShards(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	storage := &shardedInstrumentStorageStub{instrumentStorageStub: &instrumentStorageStub{}}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, DataSourceID: StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}

	first, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "shard-0", SnapshotAt: now, SnapshotShardIndex: 0, SnapshotShardCount: 2})
	require.NoError(t, err)
	require.Empty(t, first.ActiveSetVersion, "the active set must stay on the previous generation until every shard arrives")
	second, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "shard-1", SnapshotAt: now, SnapshotShardIndex: 1, SnapshotShardCount: 2})
	require.NoError(t, err)
	require.Equal(t, first.SnapshotID, second.ActiveSetVersion)
	require.Equal(t, []string{first.SnapshotID + "::shard:0/2", first.SnapshotID + "::shard:1/2"}, storage.stagedSetIDs)
	require.Len(t, storage.rows, 3, "each instrument row is persisted by exactly one shard")
	require.Len(t, storage.registrations, 3, "each subject is registered by exactly one shard")
	require.Len(t, storage.bindings, 6, "the Storage activation receives both dataset bindings for all instruments")
}

func TestInstrumentPipelineDoesNotActivateInactiveSnapshotInActiveSet(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	snapshot := testInstrumentSnapshot("sina", now)
	snapshot.Instruments[1].Status = "inactive"
	snapshot.Instruments[2].Status = "delisted"
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: snapshot}))
	storage := &instrumentStorageStub{}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, DataSourceID: StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}

	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "status-snapshot-request", SnapshotAt: now})
	require.NoError(t, err)
	require.Len(t, storage.registrations, 3, "the snapshot record remains auditable")
	for _, registration := range storage.registrations {
		if registration.GetSubject().GetSubjectId() == "000001.XSHE" {
			require.Equal(t, "inactive", registration.GetSubject().GetStatus())
		}
		if registration.GetSubject().GetSubjectId() == "920000.XBSE" {
			require.Equal(t, "delisted", registration.GetSubject().GetStatus())
		}
	}
	for _, binding := range storage.bindings {
		require.NotEqual(t, "000001.XSHE", binding.GetSubjectId())
		require.NotEqual(t, "920000.XBSE", binding.GetSubjectId())
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

func TestInstrumentPipelineDoesNotSwitchBindingsWhenStagingFails(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	storage := &instrumentStorageStub{registerErrAt: 2}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}

	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})

	require.ErrorContains(t, err, "register instrument")
	require.Empty(t, storage.bindings)
}

func TestInstrumentPipelineRollsBackActiveBindingsWhenCommitFails(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	existing := []*storagepb.DatasetSubject{{SpaceId: StockCNSpaceID, DatasetId: StockCNInstrumentDatasetID, SubjectId: "000001.XSHE", SubjectRole: "record", Status: "active", Attributes: map[string]string{"active_instrument_set_version": "old"}}}
	storage := &instrumentStorageStub{existing: existing, activateErr: errors.New("activation failed")}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}

	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})

	require.ErrorContains(t, err, "activate active instrument set")
	require.Empty(t, storage.bindings, "failed activation must not publish staged bindings")
}

func TestInstrumentPipelineRollsBackAmbiguousFailedBinding(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now)}))
	existing := []*storagepb.DatasetSubject{{SpaceId: StockCNSpaceID, DatasetId: StockCNInstrumentDatasetID, SubjectId: "000001.XSHE", SubjectRole: "record", Status: "active", Attributes: map[string]string{"active_instrument_set_version": "old"}}}
	storage := &instrumentStorageStub{existing: existing, activateErr: errors.New("ambiguous activation response")}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}

	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "snapshot-request", SnapshotAt: now})

	require.ErrorContains(t, err, "activate active instrument set")
	require.Empty(t, storage.bindings, "ambiguous activation must not expose a partial set")
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
	require.GreaterOrEqual(t, len(storage.bindings), 2)
	missingBindings := make(map[string]*storagepb.DatasetSubject)
	for _, binding := range storage.bindings {
		if binding.GetSubjectId() == "601999.XSHG" {
			missingBindings[binding.GetDatasetId()] = binding
		}
	}
	require.Equal(t, "disabled", missingBindings[StockCNInstrumentDatasetID].GetStatus())
	require.Equal(t, "disabled", missingBindings[StockCNDatasetID].GetStatus())
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
	require.NotEmpty(t, storage.bindings)
	var missingBinding *storagepb.DatasetSubject
	for _, binding := range storage.bindings {
		if binding.GetSubjectId() == "601999.XSHG" && binding.GetDatasetId() == StockCNInstrumentDatasetID {
			missingBinding = binding
		}
	}
	require.NotNil(t, missingBinding)
	require.Equal(t, "active", missingBinding.GetStatus())
	require.Equal(t, "1", missingBinding.GetAttributes()["missing_complete_snapshot_count"])
}

func TestInstrumentPipelineStagesLargeSnapshotAsOneInactiveSet(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	instruments := make([]marketdata.Instrument, 0, 5000)
	for index := 0; index < 5000; index++ {
		instruments = append(instruments, marketdata.Instrument{SubjectID: fmt.Sprintf("%06d.XSHG", index), ProviderSymbol: fmt.Sprintf("sh%06d", index), Exchange: "XSHG", Status: "active"})
	}
	snapshot := marketdata.InstrumentSnapshot{SnapshotID: "large-snapshot", SourceProvider: "sina", MarketID: StockCNSpaceID, FetchedAt: now, Complete: true, PageCount: 50, ExchangeCounts: map[string]int{"XSHG": 5000}, Instruments: instruments}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: snapshot}))
	storage := &instrumentStorageStub{}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, RequiredExchanges: []string{"XSHG"}}
	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "large-snapshot-request", SnapshotAt: now})
	require.NoError(t, err)
	require.Equal(t, 1, storage.stageCalls)
	require.Equal(t, 10000, len(storage.stagedBindings))
	require.Equal(t, 10000, len(storage.bindings))
}

func TestInstrumentPipelineUsesMarketMetadataAndCanPublishOnlyInstrumentSet(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "binance", snapshot: marketdata.InstrumentSnapshot{
		SnapshotID: "crypto-snapshot", SourceProvider: "binance", MarketID: "crypto", FetchedAt: now,
		Complete: true, PageCount: 1, ExchangeCounts: map[string]int{"binance": 1},
		Instruments: []marketdata.Instrument{{SubjectID: "BTC-USDT-SPOT", CanonicalSymbol: "BTC-USDT", ProviderSymbol: "BTCUSDT", Exchange: "binance", Name: "BTC/USDT", Status: "active", BaseAsset: "BTC", QuoteAsset: "USDT"}},
	}}))
	storage := &instrumentStorageStub{}
	pipeline := &InstrumentPipeline{
		Registry: registry, Storage: storage, CandidateChain: []string{"binance"},
		SpaceID: "crypto_market", MarketID: "crypto", DatasetID: "binance_spot_symbols", DataSourceID: "binance",
		SubjectType: "crypto_pair", SubjectMarket: "CRYPTO", Currency: "USDT", Timezone: "UTC",
		RequiredExchanges: []string{"binance"}, MinimumCount: 1,
	}

	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "crypto-snapshot-request", SnapshotAt: now})
	require.NoError(t, err)
	require.Len(t, storage.stagedBindings, 1, "an empty target dataset must not create a second binding set")
	require.Len(t, storage.registrations, 1)
	registration := storage.registrations[0]
	require.Equal(t, "crypto_market", registration.GetSpaceId())
	require.Equal(t, "crypto_pair", registration.GetSubject().GetSubjectType())
	require.Equal(t, "CRYPTO", registration.GetSubject().GetMarket())
	require.Equal(t, "USDT", registration.GetSubject().GetCurrency())
	require.Equal(t, "UTC", registration.GetSubject().GetTimezone())
}

func TestInstrumentPipelineRejectsOlderSnapshotBeforeWriting(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: testInstrumentSnapshot("sina", now.Add(-time.Hour))}))
	storage := &instrumentStorageStub{existing: []*storagepb.DatasetSubject{{SpaceId: StockCNSpaceID, DatasetId: StockCNInstrumentDatasetID, SubjectId: "600000.XSHG", Status: "active", Attributes: map[string]string{"active_instrument_set_fetched_at": now.Format(time.RFC3339Nano)}}}}
	pipeline := &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, MarketID: StockCNSpaceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}}
	_, err := pipeline.Execute(context.Background(), InstrumentPipelineRequest{RequestID: "older-snapshot-request", SnapshotAt: now.Add(-time.Hour)})
	require.ErrorContains(t, err, "older than active snapshot")
	require.Empty(t, storage.rows)
	require.Zero(t, storage.stageCalls)
}

func TestSnapshotGenerationIDChangesWhenCompleteSnapshotContentChanges(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	left := testInstrumentSnapshot("sina", now)
	right := testInstrumentSnapshot("sina", now)
	right.Instruments[0].ProviderSymbol = "sh600001"

	assert.NotEqual(t, snapshotGenerationID(left), snapshotGenerationID(right))
	assert.Equal(t, snapshotGenerationID(left), snapshotGenerationID(left))
}

func testInstrumentSnapshot(provider string, now time.Time) marketdata.InstrumentSnapshot {
	return marketdata.InstrumentSnapshot{SnapshotID: marketdata.SnapshotID(provider, StockCNSpaceID, now), SourceProvider: provider, MarketID: StockCNSpaceID, FetchedAt: now, Complete: true, PageCount: 3, ExchangeCounts: map[string]int{"XSHG": 1, "XSHE": 1, "XBSE": 1}, Instruments: []marketdata.Instrument{{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Exchange: "XSHG", Name: "浦发银行", Status: "active"}, {SubjectID: "000001.XSHE", ProviderSymbol: "sz000001", Exchange: "XSHE", Name: "平安银行", Status: "active"}, {SubjectID: "920000.XBSE", ProviderSymbol: "bj920000", Exchange: "XBSE", Name: "北交所样本", Status: "active"}}}
}
