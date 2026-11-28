package marketfetch

import (
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRequestPayloadCarriesOutputFieldsOnCollectionItem(t *testing.T) {
	req := Request{Items: []domain.CollectionItem{{OutputFields: []string{"close", "volume"}}}}
	raw, err := json.Marshal(req)
	require.NoError(t, err)
	var decoded Request
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, []string{"close", "volume"}, decoded.Items[0].OutputFields)
}

func TestRequestValidateRejectsMalformedOrReversedCoverage(t *testing.T) {
	base := Request{
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID,
		BatchKind: domain.BatchKindBackfill,
		Items: []domain.CollectionItem{{
			InstanceID: "i1", SubjectID: "600000.XSHG", DatasetID: testStockDatasetID,
			StartTime: "2026-08-30T01:00:00Z", BarLimit: 3,
		}},
		Targets: []domain.WriteTarget{{ID: "t1", SpaceID: StockCNSpaceID, InstanceID: "i1", TaskID: "backfill", DatasetID: testStockDatasetID}},
	}

	tests := []struct {
		name   string
		mutate func(*Request)
	}{
		{name: "malformed start", mutate: func(req *Request) { req.Items[0].StartTime = "2026-08-30 01:00:00" }},
		{name: "malformed end", mutate: func(req *Request) { req.Items[0].EndTime = "2026-08-30 01:01:00" }},
		{name: "reversed range", mutate: func(req *Request) { req.Items[0].EndTime = "2026-08-30T00:59:00Z" }},
		{name: "empty range", mutate: func(req *Request) { req.Items[0].EndTime = req.Items[0].StartTime }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := base
			req.Items = append([]domain.CollectionItem(nil), base.Items...)
			test.mutate(&req)
			require.Error(t, req.validate())
		})
	}
}

func TestRequestValidateAcceptsRFC3339NanoCoverage(t *testing.T) {
	req := Request{
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID,
		BatchKind: domain.BatchKindBackfill,
		Items: []domain.CollectionItem{{
			InstanceID: "i1", SubjectID: "600000.XSHG", DatasetID: testStockDatasetID,
			StartTime: "2026-08-30T01:00:00.123456789Z", EndTime: "2026-08-30T01:01:00Z", BarLimit: 3,
		}},
		Targets: []domain.WriteTarget{{ID: "t1", SpaceID: StockCNSpaceID, InstanceID: "i1", TaskID: "backfill", DatasetID: testStockDatasetID}},
	}
	require.NoError(t, req.validate())
}

func TestRequestValidateAllowsTenRealtimeBarsAndRejectsEleven(t *testing.T) {
	base := Request{
		BatchID: "batch-realtime", SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID, Frequency: "1m",
		BatchKind: domain.BatchKindRealtime, RequirePeriodCommit: true,
		Items: []domain.CollectionItem{{
			InstanceID: "i1", SubjectID: "600000.XSHG", DatasetID: testStockDatasetID,
			TargetDataTime: "2026-08-30T01:00:00Z", BarLimit: MaxRealtimeRows,
		}},
		Targets: []domain.WriteTarget{{
			ID: "t1", SpaceID: StockCNSpaceID, InstanceID: "i1", TaskID: "task-1", DatasetID: testStockDatasetID,
			Frequency: "1m", TargetDataTime: "2026-08-30T01:00:00Z", SeriesHash: "h", ExpectedCount: 1,
		}},
	}
	require.NoError(t, base.validate())

	base.Items[0].BarLimit = MaxRealtimeRows + 1
	require.ErrorContains(t, base.validate(), "realtime bar_limit")
}

func TestRequestValidateEnforcesTwoRequestShapes(t *testing.T) {
	realtime := Request{
		BatchID: "b1", SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID, Frequency: "1m",
		BatchKind: domain.BatchKindRealtime, RequirePeriodCommit: false,
		Items:   []domain.CollectionItem{{InstanceID: "i1", SubjectID: "600000.XSHG", DatasetID: testStockDatasetID, TargetDataTime: "2026-08-30T01:00:00Z"}},
		Targets: []domain.WriteTarget{{ID: "t1", SpaceID: StockCNSpaceID, InstanceID: "i1", TaskID: "task-1", DatasetID: testStockDatasetID, Frequency: "1m", TargetDataTime: "2026-08-30T01:00:00Z", SeriesHash: "h", ExpectedCount: 1}},
	}
	require.ErrorContains(t, realtime.validate(), "requires period commit")

	backfill := Request{
		BatchID: "b2", SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID, Frequency: "1m",
		BatchKind: domain.BatchKindBackfill, RequirePeriodCommit: true,
		Items:   []domain.CollectionItem{{InstanceID: "i1", SubjectID: "600000.XSHG", DatasetID: testStockDatasetID, StartTime: "2026-08-30T01:00:00Z", BarLimit: 1}},
		Targets: []domain.WriteTarget{{ID: "t1", SpaceID: StockCNSpaceID, InstanceID: "i1", TaskID: "backfill", DatasetID: testStockDatasetID}},
	}
	require.ErrorContains(t, backfill.validate(), "must not require period commit")

	backfill.RequirePeriodCommit = false
	backfill.Targets = nil
	require.ErrorContains(t, backfill.validate(), "targets must not be empty")
}

func TestRequestValidateRejectsMixedSourceBinding(t *testing.T) {
	req := Request{
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: testStockDatasetID, Frequency: "1m",
		Provider: "sina", SourceID: "stockcn_minute_http",
		BatchKind: domain.BatchKindRealtime, RequirePeriodCommit: true,
		Items: []domain.CollectionItem{
			{InstanceID: "i1", SubjectID: "600000.XSHG", DatasetID: testStockDatasetID, Provider: "sina", SourceID: "stockcn_minute_http", TargetDataTime: "2026-08-30T01:00:00Z"},
			{InstanceID: "i2", SubjectID: "000001.XSHE", DatasetID: testStockDatasetID, Provider: "tdx", SourceID: "normal_7709", TargetDataTime: "2026-08-30T01:00:00Z"},
		},
		Targets: []domain.WriteTarget{
			{ID: "t1", SpaceID: StockCNSpaceID, InstanceID: "i1", TaskID: "task-1", DatasetID: testStockDatasetID, Frequency: "1m", TargetDataTime: "2026-08-30T01:00:00Z", SeriesIndex: 0, SeriesHash: "h", ExpectedCount: 2},
			{ID: "t2", SpaceID: StockCNSpaceID, InstanceID: "i2", TaskID: "task-1", DatasetID: testStockDatasetID, Frequency: "1m", TargetDataTime: "2026-08-30T01:00:00Z", SeriesIndex: 1, SeriesHash: "h", ExpectedCount: 2},
		},
	}
	require.ErrorContains(t, req.validate(), "source binding")
}
