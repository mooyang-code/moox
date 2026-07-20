//go:build legacy_storage

package main

import (
	"fmt"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func fieldDefs() []*pb.Field {
	defs := []struct {
		name string
		typ  pb.FieldValueType
	}{
		{"open", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"high", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"low", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"close", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"quote_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"trade_num", pb.FieldValueType_FIELD_VALUE_TYPE_INT},
		{"taker_buy_base_asset_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"taker_buy_quote_asset_volume", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"symbol", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"avg_price_1m", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"avg_price_5m", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"fundingRate", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"title", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"status", pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{"score", pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{"updated_at", pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		{"payload_json", pb.FieldValueType_FIELD_VALUE_TYPE_JSON},
	}
	out := make([]*pb.Field, 0, len(defs))
	for _, def := range defs {
		out = append(out, &pb.Field{SpaceId: spaceID, FieldId: def.name, Name: def.name, ValueType: def.typ, Status: "active"})
	}
	return out
}

func columnsForMarket(market string) []*pb.DatasetColumn {
	names := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num", "taker_buy_base_asset_volume", "taker_buy_quote_asset_volume", "symbol", "avg_price_1m", "avg_price_5m"}
	if market == "swap" {
		names = append(names, "fundingRate")
	}
	out := make([]*pb.DatasetColumn, 0, len(names))
	for _, name := range names {
		out = append(out, &pb.DatasetColumn{ColumnName: name, OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: name, ValueType: valueTypeForColumn(name), Required: isRequiredColumn(name), Status: "active", Attributes: displayNameAttrs(name)})
	}
	return out
}

func recordColumns() []*pb.DatasetColumn {
	defs := []struct {
		name        string
		valueType   pb.FieldValueType
		textIndexed bool
	}{
		{name: "title", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, textIndexed: true},
		{name: "status", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING},
		{name: "score", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE},
		{name: "updated_at", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_TIME},
		{name: "payload_json", valueType: pb.FieldValueType_FIELD_VALUE_TYPE_JSON},
	}
	out := make([]*pb.DatasetColumn, 0, len(defs))
	for _, def := range defs {
		out = append(out, &pb.DatasetColumn{
			SpaceId:    spaceID,
			DatasetId:  recordDatasetID,
			ColumnName: def.name,
			OriginType: pb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD,
			OriginId:   def.name,
			ValueType:  def.valueType,
			Status:     "active",
			Attributes: displayNameAttrs(def.name),
		})
	}
	return out
}

func benchDatasetName(market string) string {
	if market == "swap" {
		return "合约K线"
	}
	return "现货K线"
}

func benchViewName(market string) string {
	if market == "swap" {
		return "合约收盘"
	}
	return "现货收盘"
}

func displayNameAttrs(name string) map[string]string {
	return map[string]string{"display_name": displayName(name)}
}

func displayName(name string) string {
	switch name {
	case "open":
		return "开盘价"
	case "high":
		return "最高价"
	case "low":
		return "最低价"
	case "close":
		return "收盘价"
	case "volume":
		return "成交量"
	case "quote_volume":
		return "成交额"
	case "trade_num":
		return "成交笔数"
	case "taker_buy_base_asset_volume":
		return "主动买量"
	case "taker_buy_quote_asset_volume":
		return "主动买额"
	case "symbol":
		return "交易标的"
	case "avg_price_1m":
		return "均价1分"
	case "avg_price_5m":
		return "均价5分"
	case "fundingRate":
		return "资金费率"
	case "title":
		return "标题"
	case "status":
		return "状态"
	case "score":
		return "分数"
	case "updated_at":
		return "更新时间"
	case "payload_json":
		return "载荷JSON"
	default:
		return "字段"
	}
}

func valueTypeForColumn(name string) pb.FieldValueType {
	if name == "trade_num" {
		return pb.FieldValueType_FIELD_VALUE_TYPE_INT
	}
	if name == "symbol" {
		return pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	}
	return pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
}

func isRequiredColumn(name string) bool {
	switch name {
	case "open", "high", "low", "close":
		return true
	default:
		return false
	}
}

func syntheticRecordID(index int) string {
	return fmt.Sprintf("bench-record-%06d", index)
}

func syntheticRecordRows(count int) []*pb.RecordRow {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]*pb.RecordRow, 0, count)
	for i := 0; i < count; i++ {
		recordID := syntheticRecordID(i)
		updatedAt := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		out = append(out, &pb.RecordRow{
			Key: &pb.RecordKey{
				SpaceId:   spaceID,
				DatasetId: recordDatasetID,
				RecordId:  recordID,
				Version:   updatedAt,
			},
			Columns: []*pb.ColumnValue{
				stringValue("title", fmt.Sprintf("synthetic record %06d", i)),
				stringValue("status", []string{"active", "paused", "archived"}[i%3]),
				doubleValue("score", 100+float64(i%1000)/10),
				timeValue("updated_at", updatedAt),
				jsonValue("payload_json", fmt.Sprintf(`{"bucket":%d,"rank":%d}`, i%10, i)),
			},
		})
	}
	return out
}

func stringValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: value}},
	}
}

func doubleValue(name string, value float64) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_DoubleValue{DoubleValue: value}},
	}
}

func timeValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_TimeValue{TimeValue: value}},
	}
}

func jsonValue(name string, value string) *pb.ColumnValue {
	return &pb.ColumnValue{
		ColumnName: name,
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_JSON,
		Value:      &pb.TypedValue{Value: &pb.TypedValue_JsonValue{JsonValue: value}},
	}
}

func sumDatasetRows(values map[string]int) int {
	var total int
	for _, value := range values {
		total += value
	}
	return total
}
