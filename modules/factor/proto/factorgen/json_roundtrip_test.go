package factorpb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 因子接口的墙钟时刻都是 Unix 毫秒：经 JSON 往返不变，JSON 里是数字字符串；数据坐标（补算区间与进度、水位）仍是 RFC3339 文本。
func TestFactorTimesTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	assertTRPCJSONRoundTrip(t, &FactorTask{TaskId: "t", CreatedAt: at, UpdatedAt: at + 1}, new(FactorTask),
		map[string]int64{"created_at": at, "updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &FactorCategory{CategoryId: "c", CreatedAt: at, UpdatedAt: at + 1}, new(FactorCategory),
		map[string]int64{"created_at": at, "updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &FactorDef{FactorId: "f", CreatedAt: at, UpdatedAt: at + 1}, new(FactorDef),
		map[string]int64{"created_at": at, "updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &FactorMember{TaskId: "t", CreatedAt: at, UpdatedAt: at + 1}, new(FactorMember),
		map[string]int64{"created_at": at, "updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &TaskRunSummary{TaskId: "t", LastPeriodTime: 1786032000, CompletedAt: at}, new(TaskRunSummary),
		map[string]int64{"last_period_time": 1786032000, "completed_at": at})
	assertTRPCJSONRoundTrip(t, &RecalcJob{JobId: "j", StartTime: "2026-08-07T00:00:00Z", EndTime: "2026-08-08T00:00:00Z", ProgressTime: "2026-08-07T12:00:00Z",
		CreatedAt: at, UpdatedAt: at + 1}, new(RecalcJob), map[string]int64{"created_at": at, "updated_at": at + 1})
	assertTRPCJSONRoundTrip(t, &EngineInfo{EngineId: "e", LastHeartbeatAt: at, CatalogSyncedAt: at + 1}, new(EngineInfo),
		map[string]int64{"last_heartbeat_at": at, "catalog_synced_at": at + 1})
	assertTRPCJSONRoundTrip(t, &MemberParamState{MemberKey: "m", LastSubmittedPeriod: 1786032000, LastSubmittedAt: at}, new(MemberParamState),
		map[string]int64{"last_submitted_period": 1786032000, "last_submitted_at": at})
	assertTRPCJSONRoundTrip(t, &UdfInfo{Name: "u", CreatedAt: at, UpdatedAt: at + 1, Versions: []*UdfVersionInfo{{Hash: "h", CreatedAt: at}}}, new(UdfInfo),
		map[string]int64{"created_at": at, "updated_at": at + 1, "versions.0.created_at": at})
	assertTRPCJSONRoundTrip(t, &EngineRuntimeStatus{CatalogSyncedAt: at, Lifecycle: &EngineLifecycleStatus{Stage: "paused", PausedSince: at},
		Watermarks: []*TaskWatermarkStatus{{TaskId: "t", CommittedThrough: "2026-08-07T00:00:00Z", BlockedSince: at, BlockedPeriod: "2026-08-07T00:01:00Z"}}},
		new(EngineRuntimeStatus), map[string]int64{"catalog_synced_at": at, "lifecycle.paused_since": at, "watermarks.0.blocked_since": at})
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
