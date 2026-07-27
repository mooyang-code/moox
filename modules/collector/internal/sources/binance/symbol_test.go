package binance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	exchange "github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestSymbolReportConcurrencyIsSerialForMetadataRefresh(t *testing.T) {
	if maxConcurrency != 1 {
		t.Fatalf("maxConcurrency = %d, want 1 to avoid concurrent metadata snapshot refresh conflicts", maxConcurrency)
	}
}

func TestNormalizedSubjectID(t *testing.T) {
	assert.Equal(t, "BTC-USDT", normalizedSubjectID(&exchange.SymbolInfo{
		Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
	}))
	assert.Equal(t, "ETH-USDT", normalizedSubjectID(&exchange.SymbolInfo{
		Symbol: "ETHUSDT", BaseAsset: "ETH", QuoteAsset: "USDT",
	}))
}

func TestContainsHyphen(t *testing.T) {
	assert.True(t, containsHyphen("BTC-USDT"))
	assert.False(t, containsHyphen("BTCUSDT"))
}

func TestAppendOptionalDoubleField(t *testing.T) {
	cols, err := appendOptionalDoubleField(nil, "x", "")
	require.NoError(t, err)
	assert.Nil(t, cols)

	cols, err = appendOptionalDoubleField(nil, "x", "1.25")
	require.NoError(t, err)
	require.Len(t, cols, 1)
	assert.Equal(t, 1.25, cols[0].GetValue().GetDoubleValue())

	_, err = appendOptionalDoubleField(nil, "x", "bad")
	assert.Error(t, err)
}

func TestBuildSymbolRegisterRequest(t *testing.T) {
	req := buildSymbolRegisterRequest(&exchange.SymbolInfo{
		Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active",
	}, "space-custom", "target-dataset", StorageBinding{
		DataSourceID: "binance", SubjectDatasetIDs: []string{"shared-dataset", "target-dataset"},
	})
	assert.Equal(t, "space-custom", req.GetSpaceId())
	assert.Equal(t, "BTC-USDT", req.GetSubject().GetSubjectId())
	assert.Equal(t, "binance", req.GetDataSourceId())
	require.Len(t, req.GetDatasetBindings(), 2)
	assert.Equal(t, "target-dataset", req.GetDatasetBindings()[0].GetDatasetId())
	assert.Equal(t, "shared-dataset", req.GetDatasetBindings()[1].GetDatasetId())
	for _, binding := range req.GetDatasetBindings() {
		assert.Equal(t, "space-custom", binding.GetSpaceId())
	}
}

func TestBuildSymbolRecordRows(t *testing.T) {
	rows, err := buildSymbolRecordRows([]*exchange.SymbolInfo{{
		Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active",
	}}, "space-custom", "target-dataset")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, "space-custom", rows[0].GetKey().GetSpaceId())
	assert.Equal(t, "target-dataset", rows[0].GetKey().GetDatasetId())
	assert.Equal(t, "BTC-USDT", rows[0].GetKey().GetRecord().GetRecordId())
	_, err = time.Parse(time.RFC3339Nano, rows[0].GetKey().GetRecord().GetVersion())
	assert.NoError(t, err)
}

func TestSymbolCollectorWritesRowsAndRegistrationsToRequestedTarget(t *testing.T) {
	writer := &fakeSymbolStorage{}
	collector := &SymbolCollector{
		storage: writer,
		fetchSymbolPage: func(context.Context, *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
			return []*exchange.SymbolInfo{{
				Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Status: "active",
			}}, nil
		},
	}

	err := collector.Collect(context.Background(), &sources.CollectParams{
		SpaceID: " space-custom ", DatasetID: " symbols-custom ", InstType: InstTypeSPOT,
	})
	require.NoError(t, err)
	require.Len(t, writer.rows, 1)
	assert.Equal(t, "space-custom", writer.rows[0].GetKey().GetSpaceId())
	assert.Equal(t, "symbols-custom", writer.rows[0].GetKey().GetDatasetId())
	require.Len(t, writer.registrations, 1)
	assert.Equal(t, "space-custom", writer.registrations[0].GetSpaceId())
	assert.Equal(t, "symbols-custom", writer.registrations[0].GetDatasetBindings()[0].GetDatasetId())
}

func TestSymbolCollectorDeactivatesMissingDatasetSubjects(t *testing.T) {
	datasetIDs := []string{"symbols-custom", "binance_spot_symbols", "binance_spot_kline_1h"}
	writer := &fakeSymbolStorage{listByDataset: make(map[string][]*storagepb.DatasetSubject)}
	oldTemplate := &storagepb.DatasetSubject{
		SpaceId: "space-custom", DatasetId: datasetIDs[0], SubjectId: "OLD-USDT",
		SubjectRole: "benchmark", EffectiveStartTime: "2026-01-01T00:00:00Z",
		EffectiveEndTime: "2026-12-31T00:00:00Z", Status: "active",
		CreatedAt: "created", UpdatedAt: "updated",
		Attributes: map[string]string{"source": "binance", "dataset": datasetIDs[0]},
	}
	var returnedOld *storagepb.DatasetSubject
	for _, datasetID := range datasetIDs {
		old := proto.Clone(oldTemplate).(*storagepb.DatasetSubject)
		old.DatasetId = datasetID
		old.Attributes["dataset"] = datasetID
		if returnedOld == nil {
			returnedOld = old
		}
		writer.listByDataset[datasetID] = []*storagepb.DatasetSubject{
			{
				SpaceId: "space-custom", DatasetId: datasetID, SubjectId: "BTC-USDT",
				SubjectRole: "normal", Status: "active",
			},
			old,
		}
	}
	collector := symbolCollectorWithSymbols(writer, activeSymbol("BTC"))

	require.NoError(t, collector.Collect(context.Background(), symbolCollectParams()))
	assert.Equal(t, datasetIDs, writer.listCalls)
	require.Len(t, writer.bound, len(datasetIDs))
	for _, got := range writer.bound {
		assert.Equal(t, "inactive", got.GetStatus())
		assert.Equal(t, "benchmark", got.GetSubjectRole())
		assert.Equal(t, "2026-01-01T00:00:00Z", got.GetEffectiveStartTime())
		assert.Equal(t, "2026-12-31T00:00:00Z", got.GetEffectiveEndTime())
		assert.Equal(t, "created", got.GetCreatedAt())
		assert.Equal(t, "updated", got.GetUpdatedAt())
		assert.Equal(t, map[string]string{"source": "binance", "dataset": got.GetDatasetId()}, got.GetAttributes())
	}
	assert.Equal(t, "active", returnedOld.GetStatus(), "reconcile must clone storage bindings before mutation")

	require.NoError(t, collector.Collect(context.Background(), symbolCollectParams()))
	assert.Len(t, writer.bound, len(datasetIDs), "repeated reconciliation must not rewrite inactive memberships")
}

func TestSymbolCollectorKeepsReturnedSubjectsActive(t *testing.T) {
	writer := &fakeSymbolStorage{
		listByDataset: map[string][]*storagepb.DatasetSubject{
			"symbols-custom": {
				{SpaceId: "space-custom", DatasetId: "symbols-custom", SubjectId: "BTC-USDT", Status: "active"},
				{SpaceId: "space-custom", DatasetId: "symbols-custom", SubjectId: "OLD-USDT", Status: "inactive"},
			},
		},
	}
	collector := symbolCollectorWithSymbols(writer, activeSymbol("BTC"))

	require.NoError(t, collector.Collect(context.Background(), symbolCollectParams()))
	assert.Empty(t, writer.bound)
}

func TestSymbolCollectorDoesNotDeactivateAfterPartialWriteFailure(t *testing.T) {
	symbols := make([]*exchange.SymbolInfo, 0, batchSize+1)
	for i := 0; i < batchSize+1; i++ {
		symbols = append(symbols, activeSymbol(fmt.Sprintf("TOKEN%d", i)))
	}
	writer := &fakeSymbolStorage{
		listByDataset: map[string][]*storagepb.DatasetSubject{
			"symbols-custom": {{
				SpaceId: "space-custom", DatasetId: "symbols-custom", SubjectId: "OLD-USDT", Status: "active",
			}},
		},
		upsertErrAfter: 1,
	}
	collector := symbolCollectorWithSymbols(writer, symbols...)

	err := collector.Collect(context.Background(), symbolCollectParams())
	require.ErrorContains(t, err, "write failed")
	assert.GreaterOrEqual(t, writer.upsertCalls, 2)
	assert.Empty(t, writer.listCalls)
	assert.Empty(t, writer.bound)
}

func TestSymbolCollectorDoesNotDeactivateAfterRegistrationFailure(t *testing.T) {
	writer := &fakeSymbolStorage{
		listByDataset: map[string][]*storagepb.DatasetSubject{
			"symbols-custom": {{
				SpaceId: "space-custom", DatasetId: "symbols-custom", SubjectId: "OLD-USDT", Status: "active",
			}},
		},
		registerErrAfter: 1,
	}
	collector := symbolCollectorWithSymbols(writer, activeSymbol("BTC"), activeSymbol("ETH"))

	err := collector.Collect(context.Background(), symbolCollectParams())
	require.ErrorContains(t, err, "register failed")
	assert.GreaterOrEqual(t, writer.registerCalls, 2)
	assert.Empty(t, writer.listCalls)
	assert.Empty(t, writer.bound)
}

func TestSymbolCollectorDoesNotDeactivateAfterWorkerPanic(t *testing.T) {
	writer := &fakeSymbolStorage{
		listByDataset: map[string][]*storagepb.DatasetSubject{
			"symbols-custom": {{
				SpaceId: "space-custom", DatasetId: "symbols-custom", SubjectId: "OLD-USDT", Status: "active",
			}},
		},
		upsertPanic: true,
	}
	collector := symbolCollectorWithSymbols(writer, activeSymbol("BTC"))

	err := collector.Collect(context.Background(), symbolCollectParams())
	require.ErrorContains(t, err, "panic found in call handlers")
	assert.Empty(t, writer.listCalls)
	assert.Empty(t, writer.bound)
}

func TestSymbolCollectorSkipsDeactivationForEmptySnapshot(t *testing.T) {
	writer := &fakeSymbolStorage{
		listByDataset: map[string][]*storagepb.DatasetSubject{
			"symbols-custom": {{
				SpaceId: "space-custom", DatasetId: "symbols-custom", SubjectId: "OLD-USDT", Status: "active",
			}},
		},
	}
	collector := symbolCollectorWithSymbols(writer)

	require.NoError(t, collector.Collect(context.Background(), symbolCollectParams()))
	assert.Empty(t, writer.listCalls)
	assert.Empty(t, writer.bound)
}

func symbolCollectorWithSymbols(writer symbolStorage, symbols ...*exchange.SymbolInfo) *SymbolCollector {
	return &SymbolCollector{
		storage: writer,
		fetchSymbolPage: func(context.Context, *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
			return symbols, nil
		},
	}
}

func symbolCollectParams() *sources.CollectParams {
	return &sources.CollectParams{
		SpaceID: "space-custom", DatasetID: "symbols-custom", InstType: InstTypeSPOT,
	}
}

func activeSymbol(base string) *exchange.SymbolInfo {
	return &exchange.SymbolInfo{
		Symbol: base + "USDT", BaseAsset: base, QuoteAsset: "USDT", Status: "active",
	}
}

type fakeSymbolStorage struct {
	rows             []*storagepb.RowFieldUpsert
	registrations    []*storagepb.RegisterDataSubjectReq
	listByDataset    map[string][]*storagepb.DatasetSubject
	listCalls        []string
	bound            []*storagepb.DatasetSubject
	upsertCalls      int
	upsertErrAfter   int
	upsertPanic      bool
	registerCalls    int
	registerErrAfter int
}

func (s *fakeSymbolStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.upsertCalls++
	if s.upsertPanic {
		panic("fake symbol storage panic")
	}
	if s.upsertErrAfter > 0 && s.upsertCalls > s.upsertErrAfter {
		return errors.New("write failed")
	}
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *fakeSymbolStorage) RegisterDataSubject(_ context.Context, req *storagepb.RegisterDataSubjectReq) error {
	s.registerCalls++
	if s.registerErrAfter > 0 && s.registerCalls > s.registerErrAfter {
		return errors.New("register failed")
	}
	s.registrations = append(s.registrations, req)
	return nil
}

func (s *fakeSymbolStorage) ListDatasetSubjects(_ context.Context, _ string, datasetID string) ([]*storagepb.DatasetSubject, error) {
	s.listCalls = append(s.listCalls, datasetID)
	return s.listByDataset[datasetID], nil
}

func (s *fakeSymbolStorage) BindDatasetSubject(_ context.Context, item *storagepb.DatasetSubject) error {
	copied := proto.Clone(item).(*storagepb.DatasetSubject)
	s.bound = append(s.bound, copied)
	for index, existing := range s.listByDataset[item.GetDatasetId()] {
		if existing.GetSubjectId() == item.GetSubjectId() {
			s.listByDataset[item.GetDatasetId()][index] = copied
		}
	}
	return nil
}
