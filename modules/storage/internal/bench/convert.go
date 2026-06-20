package bench

import (
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func KlineRowsToDataRows(spaceID, datasetID, subjectID, freq string, rows []KlineRow) []*pb.DataRow {
	out := make([]*pb.DataRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.DataRow{
			Key: &pb.DataKey{
				Scope: &pb.DataScope{
					SpaceId:   spaceID,
					DatasetId: datasetID,
					SubjectId: subjectID,
					Freq:      freq,
				},
				DataTime: row.Time.UTC().Format(time.RFC3339),
				RowId:    row.Time.UTC().Format(time.RFC3339),
			},
			Columns: klineColumns(row),
		})
	}
	return out
}

func KlineRowsToTimeSeriesRows(spaceID, datasetID, subjectID, freq string, rows []KlineRow) []*pb.TimeSeriesRow {
	out := make([]*pb.TimeSeriesRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, &pb.TimeSeriesRow{
			Key: &pb.TimeSeriesKey{
				SpaceId:   spaceID,
				DatasetId: datasetID,
				SubjectId: subjectID,
				Freq:      freq,
				DataTime:  row.Time.UTC().Format(time.RFC3339),
			},
			Columns: klineColumns(row),
		})
	}
	return out
}

func klineColumns(row KlineRow) []*pb.ColumnValue {
	columns := []*pb.ColumnValue{
		doubleColumn("open", row.Open),
		doubleColumn("high", row.High),
		doubleColumn("low", row.Low),
		doubleColumn("close", row.Close),
		doubleColumn("volume", row.Volume),
		doubleColumn("quote_volume", row.QuoteVolume),
		intColumn("trade_num", row.TradeNum),
		doubleColumn("taker_buy_base_asset_volume", row.TakerBuyBaseAssetVolume),
		doubleColumn("taker_buy_quote_asset_volume", row.TakerBuyQuoteAssetVolume),
		stringColumn("symbol", row.Symbol),
		doubleColumn("avg_price_1m", row.AvgPrice1M),
		doubleColumn("avg_price_5m", row.AvgPrice5M),
	}
	if row.FundingRate != nil {
		columns = append(columns, doubleColumn("fundingRate", *row.FundingRate))
	}
	return columns
}

func doubleColumn(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func intColumn(name string, value int64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_IntValue{IntValue: value}},
	}
}

func stringColumn(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}
