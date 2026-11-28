package storagepb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 写入方完成标记的完成时刻是 Unix 毫秒；经 JSON 往返不变，JSON 里是数字字符串。
func TestDatasetPeriodCompletedMarkerTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &DatasetPeriodCompletedMarker{DatasetId: "d", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", CompletedAt: at},
		new(DatasetPeriodCompletedMarker), map[string]int64{"completed_at": at, "period_time": 1786032000})
}

// 元数据与变更日志的创建、更新、失效时刻都是 Unix 毫秒；经 JSON 往返不变，JSON 里是数字字符串。
func TestMetadataTimestampsTRPCJSONRoundTrip(t *testing.T) {
	const created, updated = int64(1700000000123), int64(1700000060456)
	both := map[string]int64{"created_at": created, "updated_at": updated}
	assertTRPCJSONRoundTrip(t, &Space{SpaceId: "s", CreatedAt: created, UpdatedAt: updated}, new(Space), both)
	assertTRPCJSONRoundTrip(t, &DataSource{SpaceId: "s", DataSourceId: "src", CreatedAt: created, UpdatedAt: updated}, new(DataSource), both)
	assertTRPCJSONRoundTrip(t, &Tag{SpaceId: "s", TagId: "t", CreatedAt: created, UpdatedAt: updated}, new(Tag), both)
	assertTRPCJSONRoundTrip(t, &Subject{SpaceId: "s", SubjectId: "BTC", CreatedAt: created, UpdatedAt: updated}, new(Subject), both)
	assertTRPCJSONRoundTrip(t, &Dataset{SpaceId: "s", DatasetId: "d", CreatedAt: created, UpdatedAt: updated}, new(Dataset), both)
	assertTRPCJSONRoundTrip(t, &DatasetColumn{SpaceId: "s", DatasetId: "d", ColumnName: "close", CreatedAt: created, UpdatedAt: updated}, new(DatasetColumn), both)
	assertTRPCJSONRoundTrip(t, &TagMember{SpaceId: "s", TagId: "t", InactiveAt: updated}, new(TagMember), map[string]int64{"inactive_at": updated})
	assertTRPCJSONRoundTrip(t, &DatasetChange{Sequence: 1, SpaceId: "s", DatasetId: "d", CreatedAt: created}, new(DatasetChange), map[string]int64{"created_at": created})
}

// 统一行消息（Row）、TimeSeriesSelector 与 SearchRecordRowsReq 经 tRPC JSON 往返保持字段名与可选语意一致。
func TestUnifiedRowAndKeyTRPCJSONRoundTrip(t *testing.T) {
	serializer := codec.GetSerializer(codec.SerializationTypeJSON)
	emptyTag := ""
	queryReq := &QueryTimeSeriesRowsReq{
		SpaceId: "crypto", DatasetId: "ds_kline",
		Selectors: []*TimeSeriesSelector{{SubjectId: "BTC-USDT", Freq: "1m", SeriesTag: &emptyTag}},
	}
	raw, err := serializer.Marshal(queryReq)
	if err != nil {
		t.Fatalf("marshal QueryTimeSeriesRowsReq: %v", err)
	}
	var decodedQuery QueryTimeSeriesRowsReq
	if err := serializer.Unmarshal(raw, &decodedQuery); err != nil {
		t.Fatalf("unmarshal QueryTimeSeriesRowsReq: %v", err)
	}
	if !proto.Equal(queryReq, &decodedQuery) || decodedQuery.GetSelectors()[0].SeriesTag == nil {
		t.Fatalf("QueryTimeSeriesRowsReq round trip mismatch: got %v want %v", &decodedQuery, queryReq)
	}

	searchReq := &SearchRecordRowsReq{
		SpaceId: "crypto", DatasetId: "ds_rec",
		Keys: []*RecordRowKey{{RecordId: "r1", Version: "v1"}, {RecordId: "r2"}},
	}
	raw, err = serializer.Marshal(searchReq)
	if err != nil {
		t.Fatalf("marshal SearchRecordRowsReq: %v", err)
	}
	var decodedSearch SearchRecordRowsReq
	if err := serializer.Unmarshal(raw, &decodedSearch); err != nil {
		t.Fatalf("unmarshal SearchRecordRowsReq: %v", err)
	}
	if !proto.Equal(searchReq, &decodedSearch) {
		t.Fatalf("SearchRecordRowsReq round trip mismatch: got %v want %v", &decodedSearch, searchReq)
	}

	row := &Row{
		Key: &RowKey{
			SpaceId: "crypto", DatasetId: "ds_kline",
			Kind: &RowKey_TimeSeries{TimeSeries: &TimeSeriesRowKey{
				SubjectId: "BTC-USDT", Freq: "1m", DataTime: "2026-10-01T00:00:00Z", SeriesTag: "venue:binance",
			}},
		},
		Fields: []*FieldValue{{FieldId: "close", Value: &TypedValue{Value: &TypedValue_DoubleValue{DoubleValue: 100.5}}}},
		Attributes: map[string]*TypedValue{
			"source": {Value: &TypedValue_StringValue{StringValue: "spot"}},
		},
	}
	raw, err = serializer.Marshal(row)
	if err != nil {
		t.Fatalf("marshal Row: %v", err)
	}
	var decodedRow Row
	if err := serializer.Unmarshal(raw, &decodedRow); err != nil {
		t.Fatalf("unmarshal Row: %v", err)
	}
	if !proto.Equal(row, &decodedRow) {
		t.Fatalf("Row round trip mismatch: got %v want %v", &decodedRow, row)
	}
	if !strings.Contains(string(raw), `"time_series"`) || !strings.Contains(string(raw), `"subject_id"`) {
		t.Fatalf("Row JSON 应使用 proto 字段名，实际=%s", raw)
	}
}

// assertTRPCJSONRoundTrip 用服务端实际使用的 JSON 序列化（trpc-go 注册的 SerializationTypeJSON，即 protojson）编码再解码：
// 解码后与原消息相同，want 里每个字段路径（点分，数组用下标）在 JSON 里都是十进制数字字符串。
func assertTRPCJSONRoundTrip(t *testing.T, message, decoded proto.Message, want map[string]int64) {
	t.Helper()
	serializer := codec.GetSerializer(codec.SerializationTypeJSON)
	raw, err := serializer.Marshal(message)
	if err != nil {
		t.Fatalf("marshal %T: %v", message, err)
	}
	if err := serializer.Unmarshal(raw, decoded); err != nil {
		t.Fatalf("unmarshal %T: %v", message, err)
	}
	if !proto.Equal(message, decoded) {
		t.Fatalf("round trip of %T changed the message:\n got %v\nwant %v", message, decoded, message)
	}
	var tree any
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	for path, value := range want {
		node := tree
		for _, part := range strings.Split(path, ".") {
			if index, err := strconv.Atoi(part); err == nil {
				items, _ := node.([]any)
				if index >= len(items) {
					t.Fatalf("%s: index %d out of range in %s", path, index, raw)
				}
				node = items[index]
				continue
			}
			object, _ := node.(map[string]any)
			node = object[part]
		}
		if got, ok := node.(string); !ok || got != strconv.FormatInt(value, 10) {
			t.Fatalf("%s = %#v, want decimal string %q in %s", path, node, strconv.FormatInt(value, 10), raw)
		}
	}
}
