package collectorpb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/codec"
)

// Collector 接口上的墙钟时刻都是 Unix 毫秒；经 trpc 的 JSON 序列化往返不变，JSON 里是数字字符串。
func TestCollectorTimestampsTRPCJSONRoundTrip(t *testing.T) {
	const a, b = int64(1700000000123), int64(1700000060456)
	both := map[string]int64{"created_at": a, "updated_at": b}
	assertTRPCJSONRoundTrip(t, &CollectionTask{TaskId: "task", CreatedAt: a, UpdatedAt: b}, new(CollectionTask), both)
	assertTRPCJSONRoundTrip(t, &DataTypeConfig{Id: 1, CreatedAt: a, UpdatedAt: b}, new(DataTypeConfig), both)
	assertTRPCJSONRoundTrip(t, &DataTypeFieldConfig{Id: 1, DefaultValue: structpb.NewStringValue("x"), CreatedAt: a, UpdatedAt: b}, new(DataTypeFieldConfig), both)
	assertTRPCJSONRoundTrip(t, &ResampleJob{JobId: "j", NotBefore: a, CreatedAt: a, UpdatedAt: b}, new(ResampleJob), map[string]int64{"not_before": a, "created_at": a, "updated_at": b})
	assertTRPCJSONRoundTrip(t, &KlineBackfillJob{JobId: "j", CreatedAt: a, UpdatedAt: b}, new(KlineBackfillJob), both)
	assertTRPCJSONRoundTrip(t, &CloudAccount{AccountId: "acc", CreatedAt: a, UpdatedAt: b}, new(CloudAccount), both)
	assertTRPCJSONRoundTrip(t, &FunctionPackage{PackageId: "pkg", CreatedAt: a, UpdatedAt: b}, new(FunctionPackage), both)
	assertTRPCJSONRoundTrip(t, &InitPackageUploadRsp{UploadUrl: "u", ExpiresAt: a}, new(InitPackageUploadRsp), map[string]int64{"expires_at": a})
	assertTRPCJSONRoundTrip(t, &CloudFunction{Name: "f", ReadAt: a}, new(CloudFunction), map[string]int64{"read_at": a})
	assertTRPCJSONRoundTrip(t, &TagSyncStatus{TagId: "t", LastRunAt: b, LastSuccessAt: a}, new(TagSyncStatus), map[string]int64{"last_run_at": b, "last_success_at": a})
	assertTRPCJSONRoundTrip(t, &GetTaskResultInventoryRsp{SnapshotId: "s", ObservedAt: a, Entries: []*TaskResultInventoryEntry{{TaskId: "task", ObservedAt: a}}},
		new(GetTaskResultInventoryRsp), map[string]int64{"observed_at": a, "entries.0.observed_at": a})
	assertTRPCJSONRoundTrip(t, &GetCollectorStatusRsp{
		GeneratedAt: b,
		Open:        &FetchPlanSummary{Id: 1, CreatedAt: a, UpdatedAt: b},
		Functions:   []*CollectorFunctionStatus{{Name: "f", CloudReadAt: a}},
		Stream:      &StreamWorkerStatus{InstanceId: "i", HeartbeatAt: b, Conns: []*StreamConnStatus{{Market: "swap", LastMessageAt: a}}},
	}, new(GetCollectorStatusRsp), map[string]int64{
		"generated_at": b, "open.created_at": a, "open.updated_at": b, "functions.0.cloud_read_at": a,
		"stream.heartbeat_at": b, "stream.conns.0.last_message_at": a,
	})
	assertTRPCJSONRoundTrip(t, &ListSubjectExclusionsRsp{Exclusions: []*SubjectExclusion{{SubjectId: "BTC", CreatedAt: a, ExpiresAt: b}}},
		new(ListSubjectExclusionsRsp), map[string]int64{"exclusions.0.created_at": a, "exclusions.0.expires_at": b})
	assertTRPCJSONRoundTrip(t, &SetSubjectExclusionsReq{SpaceId: "s", ExpiresAt: b}, new(SetSubjectExclusionsReq), map[string]int64{"expires_at": b})
	assertTRPCJSONRoundTrip(t, &StreamHeartbeatReq{InstanceId: "i", SentAt: a}, new(StreamHeartbeatReq), map[string]int64{"sent_at": a})
	assertTRPCJSONRoundTrip(t, &StreamHeartbeatRsp{PlanId: 1, ServerTime: b}, new(StreamHeartbeatRsp), map[string]int64{"server_time": b})
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
