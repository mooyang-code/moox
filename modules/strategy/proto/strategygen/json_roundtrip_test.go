package strategypb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 策略接口的时刻（含 bar_end_time、回放的起止与进度）都是 Unix 毫秒：经 JSON 往返不变，JSON 里是数字字符串。
func TestStrategyTimesTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &Strategy{StrategyId: "s", CreatedAt: at, UpdatedAt: at + 1}, new(Strategy),
		map[string]int64{"created_at": at, "updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &StrategyInstance{InstanceId: "i", CreatedAt: at, UpdatedAt: at + 1, ConfirmedAt: at + 2}, new(StrategyInstance),
		map[string]int64{"created_at": at, "updated_at": at + 1, "confirmed_at": at + 2})
	assertTRPCJSONRoundTrip(t, &StrategyResult{ResultId: "r", BarEndTime: at, ValidUntil: at + 1, CreatedAt: at + 2, ParamsDeliveredAt: at + 3,
		Targets: []*InstrumentWeight{{InstrumentId: "BTC-USDT", TargetWeight: "0.5", Market: "spot"}}}, new(StrategyResult),
		map[string]int64{"bar_end_time": at, "valid_until": at + 1, "created_at": at + 2, "params_delivered_at": at + 3})
	assertTRPCJSONRoundTrip(t, &Replay{ReplayId: "p", StartTime: at, EndTime: at + 1, ProgressTime: at + 2, CreatedAt: at + 3, UpdatedAt: at + 4}, new(Replay),
		map[string]int64{"start_time": at, "end_time": at + 1, "progress_time": at + 2, "created_at": at + 3, "updated_at": at + 4})
	assertTRPCJSONRoundTrip(t, &ReplayBar{BarEndTime: at}, new(ReplayBar), map[string]int64{"bar_end_time": at})
	assertTRPCJSONRoundTrip(t, &ListStrategyTargetsRsp{BarEndTime: at, ValidUntil: at + 1}, new(ListStrategyTargetsRsp),
		map[string]int64{"bar_end_time": at, "valid_until": at + 1})
	assertTRPCJSONRoundTrip(t, &StartReplayReq{StrategyId: "s", StartTime: at, EndTime: at + 1}, new(StartReplayReq),
		map[string]int64{"start_time": at, "end_time": at + 1})
	assertTRPCJSONRoundTrip(t, &StartReplayRsp{BarCount: 2, FirstBarEnd: at, LastBarEnd: at + 1}, new(StartReplayRsp),
		map[string]int64{"first_bar_end": at, "last_bar_end": at + 1})
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
