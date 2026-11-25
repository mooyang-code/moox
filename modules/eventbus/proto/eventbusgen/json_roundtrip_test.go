package eventbuspb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 流与消费者的时刻都是 Unix 毫秒；经 trpc 的 JSON 序列化往返不变，JSON 里是数字字符串。
func TestEventBusTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &ListStreamsRsp{Streams: []*StreamInfo{{Name: "MOOX", FirstTime: at, LastTime: at + 1}}}, new(ListStreamsRsp),
		map[string]int64{"streams.0.first_time": at, "streams.0.last_time": at + 1})
	assertTRPCJSONRoundTrip(t, &GetConsumerRsp{Consumer: &ConsumerInfo{Stream: "MOOX", Name: "c", LastDeliveredAt: at}}, new(GetConsumerRsp),
		map[string]int64{"consumer.last_delivered_at": at})
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
