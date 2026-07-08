package builder

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMapDatasetColumnsToViewMapsOnlySourceDatasetColumns(t *testing.T) {
	view := builderTestView()
	columns := builderTestViewColumns()

	mapped := MapDatasetColumnsToView(view, columns, "kline", []*pb.TimeSeriesRow{
		builderTestTSRow("crypto", "kline", "BTC-USDT", "2026-07-08T10:00:00Z",
			builderTestValue("close", 12),
			builderTestValue("volume", 99),
		),
	})

	if len(mapped) != 1 {
		t.Fatalf("mapped rows = %d, want 1", len(mapped))
	}
	if got := mapped[0].GetKey().GetDatasetId(); got != "kline" {
		t.Fatalf("mapped dataset_id = %q, want primary dataset kline", got)
	}
	if len(mapped[0].GetColumns()) != 1 || mapped[0].GetColumns()[0].GetColumnName() != "close" {
		t.Fatalf("mapped columns = %+v, want only close", mapped[0].GetColumns())
	}
}

func TestMapDatasetColumnsToViewAllowsNonPrimaryHalfRowPatch(t *testing.T) {
	view := builderTestView()
	columns := builderTestViewColumns()

	mapped := MapDatasetColumnsToView(view, columns, "funding", []*pb.TimeSeriesRow{
		builderTestTSRow("crypto", "funding", "BTC-USDT", "2026-07-08T10:00:00Z", builderTestValue("rate", 0.01)),
	})

	if len(mapped) != 1 {
		t.Fatalf("mapped rows = %d, want 1", len(mapped))
	}
	if got := mapped[0].GetKey().GetDatasetId(); got != "kline" {
		t.Fatalf("mapped dataset_id = %q, want primary dataset kline", got)
	}
	if len(mapped[0].GetColumns()) != 1 || mapped[0].GetColumns()[0].GetColumnName() != "funding_rate" {
		t.Fatalf("mapped columns = %+v, want only funding_rate", mapped[0].GetColumns())
	}
}

func TestMapRecordColumnsToViewMapsOnlySourceDatasetColumns(t *testing.T) {
	view := builderTestRecordView()
	columns := builderTestViewColumns()

	mapped := MapRecordColumnsToView(view, columns, "funding", []*pb.RecordRow{
		builderTestRecordRow("crypto", "funding", "BTC-USDT", "v1", builderTestValue("rate", 0.02)),
	})

	if len(mapped) != 1 {
		t.Fatalf("mapped rows = %d, want 1", len(mapped))
	}
	if got := mapped[0].GetKey().GetDatasetId(); got != "kline" {
		t.Fatalf("mapped dataset_id = %q, want primary dataset kline", got)
	}
	if len(mapped[0].GetColumns()) != 1 || mapped[0].GetColumns()[0].GetColumnName() != "funding_rate" {
		t.Fatalf("mapped columns = %+v, want only funding_rate", mapped[0].GetColumns())
	}
}

func builderTestView() *pb.View {
	return &pb.View{
		SpaceId:          "crypto",
		ViewId:           "spot_view",
		PrimaryDatasetId: "kline",
		DatasetIds:       []string{"kline", "funding"},
		Engine:           "duckdb",
	}
}

func builderTestRecordView() *pb.View {
	view := builderTestView()
	view.Engine = "bleve"
	return view
}

func builderTestViewColumns() []*pb.ViewColumn {
	return []*pb.ViewColumn{
		{
			ColumnName: "close",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "kline.close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		},
		{
			ColumnName: "funding_rate",
			OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId:   "funding.rate",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		},
	}
}
