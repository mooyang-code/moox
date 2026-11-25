package storagepb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 事件里的时刻是 Unix 毫秒，补采区间是 Unix 秒（拍板 D2）；经 JSON 往返不变，JSON 里是数字字符串。
func TestStorageEventsTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &DatasetPeriodCompleted{DatasetId: "d", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", Source: "ledger", CompletedAt: at},
		new(DatasetPeriodCompleted), map[string]int64{"completed_at": at, "period_time": 1786032000})
	assertTRPCJSONRoundTrip(t, &ViewDataReady{DatasetId: "d", Frequency: "1m", PeriodTime: 1786032000, Status: "complete", ReadyAt: at},
		new(ViewDataReady), map[string]int64{"ready_at": at})
	assertTRPCJSONRoundTrip(t, &CollectorKlineBackfilled{
		DatasetId: "d", Frequency: "1m", SubjectIds: []string{"BTC"}, Start: 1786032000, End: 1786035600, Origin: "auto", ConfirmedAt: at,
		Absent: []*CollectorKlineAbsent{{SubjectId: "BTC", Start: 1786032000, End: 1786032060}},
	}, new(CollectorKlineBackfilled), map[string]int64{
		"start": 1786032000, "end": 1786035600, "confirmed_at": at, "absent.0.start": 1786032000, "absent.0.end": 1786032060,
	})
	assertTRPCJSONRoundTrip(t, &InstrumentDelistScheduled{Exchange: "binance", Market: "spot", ExchangeSymbol: "FOOUSDT", DelistAt: at + 86400000, FirstSeenAt: at},
		new(InstrumentDelistScheduled), map[string]int64{"delist_at": at + 86400000, "first_seen_at": at})
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
