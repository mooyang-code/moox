package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/marketfetchpb"
	"github.com/stretchr/testify/require"
)

type deadlineRecordingStorage struct {
	remaining time.Duration
}

func (s *deadlineRecordingStorage) UpsertFields(ctx context.Context, _ []*storagepb.RowFieldUpsert) error {
	deadline, ok := ctx.Deadline()
	if ok {
		s.remaining = time.Until(deadline)
	}
	return ctx.Err()
}

func (*deadlineRecordingStorage) RegisterDataSubject(context.Context, *storagepb.RegisterDataSubjectReq) error {
	return nil
}

func TestContextWithReserveEndsFetchBeforeStorageAndCLSWindow(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer parentCancel()
	work, workCancel := contextWithReserve(parent, 180*time.Millisecond)
	defer workCancel()
	deadline, ok := work.Deadline()
	require.True(t, ok)
	remaining := time.Until(deadline)
	require.Positive(t, remaining)
	require.LessOrEqual(t, remaining, 140*time.Millisecond)
}

func TestStorageAndPublishReservesColdEventBusConnection(t *testing.T) {
	commit, publish := storageAndPublishReserves(5*time.Second, 0, true)
	require.Equal(t, 15*time.Second, commit)
	require.Equal(t, 10*time.Second, publish)
}

func TestReservedDeadlineStorageUsesReservedParentBudget(t *testing.T) {
	parent, parentCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer parentCancel()
	expiredWork, workCancel := context.WithCancel(parent)
	workCancel()
	underlying := &deadlineRecordingStorage{}
	storage := &reservedDeadlineStorage{Storage: underlying, parent: parent, timeout: 120 * time.Millisecond}

	require.NoError(t, storage.UpsertFields(expiredWork, []*storagepb.RowFieldUpsert{{}}))
	require.Positive(t, underlying.remaining)
	require.LessOrEqual(t, underlying.remaining, 130*time.Millisecond)
}

func TestReservedDeadlineStorageForwardsInstrumentNames(t *testing.T) {
	underlying := &pipelineStorageWithInstrumentNames{names: map[string]string{"600000.XSHG": "浦发银行"}}
	storage := &reservedDeadlineStorage{Storage: underlying, parent: context.Background(), timeout: time.Second}

	names, err := storage.ListInstrumentNames(context.Background(), StockCNSpaceID, []string{"600000.XSHG"})
	require.NoError(t, err)
	require.Equal(t, "浦发银行", names["600000.XSHG"])
}

func TestHandlerUsesCommonInstrumentPipelineForCryptoSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 29, 3, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "binance", snapshot: marketdata.InstrumentSnapshot{
		SnapshotID: "crypto-snapshot", SourceProvider: "binance", MarketID: "crypto", FetchedAt: now,
		Complete: true, PageCount: 1, ExchangeCounts: map[string]int{"binance": 1},
		Instruments: []marketdata.Instrument{{SubjectID: "BTC-USDT-SPOT", ProviderSymbol: "BTCUSDT", Exchange: "binance", Status: "active"}},
	}}))
	storage := &instrumentStorageStub{}
	h := &Handler{
		NewStorage: func(string, string, string) (Storage, error) { return storage, nil },
		NewCryptoInstrumentPipeline: func(InstrumentStorage, marketdata.ProductType) (*InstrumentPipeline, error) {
			return &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto", DatasetID: "dataset_binance_spot_symbols", DataSourceID: "binance", TargetDatasetID: "", RequiredExchanges: []string{"binance"}, MinimumCount: 1}, nil
		},
		Now: func() time.Time { return now },
	}
	req := Request{BatchID: "crypto-instrument-batch", BatchKind: domain.BatchKindInstrumentSnapshot, SpaceID: "crypto", DatasetID: "dataset_binance_spot_symbols", MarketType: "spot", RequestID: "crypto-instrument-request", Items: []domain.CollectionItem{{DataType: "instrument", DatasetID: "dataset_binance_spot_symbols"}}}

	response, err := h.handleRequest(context.Background(), req, "storage", false)
	require.NoError(t, err)
	completed, ok := response.Data.(*marketfetchpb.MarketFetchBatchCompleted)
	require.True(t, ok)
	require.Equal(t, "succeeded", completed.GetStatus())
	require.Len(t, storage.stagedBindings, 1)
}

func TestHandlerUsesCommonInstrumentPipelineForStockSnapshot(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(&instrumentProviderStub{id: "sina", snapshot: marketdata.InstrumentSnapshot{
		SnapshotID: "stock-snapshot", SourceProvider: "sina", MarketID: StockCNSpaceID, FetchedAt: now,
		Complete: true, PageCount: 1, ExchangeCounts: map[string]int{"XSHG": 1, "XSHE": 1, "XBSE": 1},
		Instruments: []marketdata.Instrument{
			{SubjectID: "600000.XSHG", ProviderSymbol: "sh600000", Exchange: "XSHG", Status: "active"},
			{SubjectID: "000001.XSHE", ProviderSymbol: "sz000001", Exchange: "XSHE", Status: "active"},
			{SubjectID: "920000.XBSE", ProviderSymbol: "bj920000", Exchange: "XBSE", Status: "active"},
		},
	}}))
	storage := &instrumentStorageStub{}
	h := &Handler{
		NewStorage: func(string, string, string) (Storage, error) { return storage, nil },
		NewInstrumentPipeline: func(InstrumentStorage, string, marketdata.ProductType) (*InstrumentPipeline, error) {
			return &InstrumentPipeline{Registry: registry, Storage: storage, CandidateChain: []string{"sina"}, SpaceID: StockCNSpaceID, MarketID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, TargetDatasetID: StockCNDatasetID, DataSourceID: StockCNDataSourceID, RequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}, MinimumCount: 3}, nil
		},
		Now: func() time.Time { return now },
	}
	req := Request{BatchID: "stock-instrument-batch", BatchKind: domain.BatchKindInstrumentSnapshot, SpaceID: StockCNSpaceID, DatasetID: StockCNInstrumentDatasetID, MarketType: "equity", RequestID: "stock-instrument-request", Items: []domain.CollectionItem{{DataType: "instrument", DatasetID: StockCNInstrumentDatasetID}}}

	response, err := h.handleRequest(context.Background(), req, "storage", false)
	require.NoError(t, err)
	completed, ok := response.Data.(*marketfetchpb.MarketFetchBatchCompleted)
	require.True(t, ok)
	require.Equal(t, "succeeded", completed.GetStatus())
	// The stock snapshot atomically stages both the instrument set and the
	// kline subject set. Each set contains the complete three-symbol snapshot.
	require.Len(t, storage.stagedBindings, 6)
	instrumentBindings := 0
	klineBindings := 0
	for _, binding := range storage.stagedBindings {
		switch binding.GetDatasetId() {
		case StockCNInstrumentDatasetID:
			instrumentBindings++
		case StockCNDatasetID:
			klineBindings++
		}
	}
	require.Equal(t, 3, instrumentBindings)
	require.Equal(t, 3, klineBindings)
}
