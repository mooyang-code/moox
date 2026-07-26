package binance

import (
	"context"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	exchange "github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
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

type fakeSymbolStorage struct {
	rows          []*storagepb.RowFieldUpsert
	registrations []*storagepb.RegisterDataSubjectReq
}

func (s *fakeSymbolStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.rows = append(s.rows, rows...)
	return nil
}

func (s *fakeSymbolStorage) RegisterDataSubject(_ context.Context, req *storagepb.RegisterDataSubjectReq) error {
	s.registrations = append(s.registrations, req)
	return nil
}
