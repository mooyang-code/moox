package marketfetch

import (
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRequestValidateRejectsMalformedOrReversedCoverage(t *testing.T) {
	base := Request{
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		BatchKind: domain.BatchKindBackfill,
		Items: []domain.CollectionItem{{
			SubjectID: "600000.XSHG", Symbol: "sh600000", DatasetID: StockCNDatasetID,
			StartTime: "2026-08-30T01:00:00Z", BarLimit: 3,
		}},
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
		BatchID: "batch-1", SpaceID: StockCNSpaceID, DatasetID: StockCNDatasetID,
		BatchKind: domain.BatchKindGapRepair,
		Items: []domain.CollectionItem{{
			SubjectID: "600000.XSHG", Symbol: "sh600000", DatasetID: StockCNDatasetID,
			StartTime: "2026-08-30T01:00:00.123456789Z", EndTime: "2026-08-30T01:01:00Z", BarLimit: 3,
		}},
	}
	require.NoError(t, req.validate())
}
