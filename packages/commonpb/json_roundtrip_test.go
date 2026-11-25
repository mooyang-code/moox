package commonpb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 生命周期消息里的时刻都是 Unix 毫秒（追赶目标是 data_time 文本，不在其中）；经 JSON 往返不变，JSON 里是数字字符串。
func TestLifecycleTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &LifecycleSnapshot{
		Module: "collector", Stage: "resuming", Since: at + 3, PausedSince: at, QuiescedAt: at + 1, ReleasedAt: at + 2,
		Pause:   &LifecyclePause{Kind: "manual", Since: at, ExpiresAt: at + 3600000},
		Catchup: &CatchupProgress{Since: at, Target: "2026-08-07T00:00:00Z", Done: 1, Total: 2},
	}, new(LifecycleSnapshot), map[string]int64{
		"since": at + 3, "paused_since": at, "quiesced_at": at + 1, "released_at": at + 2,
		"pause.since": at, "pause.expires_at": at + 3600000, "catchup.since": at,
	})
	assertTRPCJSONRoundTrip(t, &ControlLifecycleReq{Command: "pause", ExpiresAt: at}, new(ControlLifecycleReq), map[string]int64{"expires_at": at})
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
