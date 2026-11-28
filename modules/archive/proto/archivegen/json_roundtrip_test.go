package archivepb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 归档接口里的墙钟时刻都是 Unix 毫秒；经 trpc 的 JSON 序列化往返不变，JSON 里是数字字符串。
// 分区范围与数据时间（range_start、min_data_time 等）是 data_time 口径的文本，不在此列。
func TestArchiveTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	run := &ArchiveRun{RunId: "r", RangeStart: "2026-10-16T00:00:00Z", ScheduledAt: at, StartedAt: at + 1, FinishedAt: at + 2, LeaseUntil: at + 3}
	assertTRPCJSONRoundTrip(t, &ListArchiveRunsRsp{Runs: []*ArchiveRun{run}}, new(ListArchiveRunsRsp), map[string]int64{
		"runs.0.scheduled_at": at, "runs.0.started_at": at + 1, "runs.0.finished_at": at + 2, "runs.0.lease_until": at + 3,
	})
	assertTRPCJSONRoundTrip(t, &ListArchiveTasksRsp{Tasks: []*ArchiveTask{{DatasetId: "d", LastFollowAt: at - 1, LastSuccessAt: at, CreatedAt: at + 1, UpdatedAt: at + 2, LastRun: run}}},
		new(ListArchiveTasksRsp), map[string]int64{"tasks.0.last_follow_at": at - 1, "tasks.0.last_success_at": at, "tasks.0.created_at": at + 1, "tasks.0.updated_at": at + 2, "tasks.0.last_run.started_at": at + 1})
	assertTRPCJSONRoundTrip(t, &ListArchivePartitionsRsp{Partitions: []*ArchivePartition{{PartitionKey: "2026-10-16", LastChangedAt: at, ExportedAt: at + 1,
		FinalizedAt: at + 2, RemoteUploadedAt: at + 3, RemoteVerifiedAt: at + 4}}}, new(ListArchivePartitionsRsp), map[string]int64{
		"partitions.0.last_changed_at": at, "partitions.0.exported_at": at + 1, "partitions.0.finalized_at": at + 2,
		"partitions.0.remote_uploaded_at": at + 3, "partitions.0.remote_verified_at": at + 4,
	})
	assertTRPCJSONRoundTrip(t, &GetArchiveOverviewRsp{Worker: &ArchiveWorkerInfo{WorkerId: "w", LastHeartbeatAt: at, ObjectStore: &ObjectStoreInfo{UnavailableSince: at + 1}}},
		new(GetArchiveOverviewRsp), map[string]int64{"worker.last_heartbeat_at": at, "worker.object_store.unavailable_since": at + 1})
	assertTRPCJSONRoundTrip(t, &ListFollowTasksRsp{Tasks: []*FollowTask{{SpaceId: "crypto", DatasetId: "d", TaskId: "1", LastSuccessAt: at,
		RegisteredPartitions: []*ArchivePartition{{PartitionKey: "2026-10-16", FinalizedAt: at + 1}}}}}, new(ListFollowTasksRsp), map[string]int64{
		"tasks.0.last_success_at": at, "tasks.0.registered_partitions.0.finalized_at": at + 1,
	})
	assertTRPCJSONRoundTrip(t, &ReportFollowResultReq{SpaceId: "crypto", DatasetId: "d", TaskId: "1", StartedAt: at, FinishedAt: at + 1,
		Partitions: []*ArchivePartition{{PartitionKey: "2026-10-16", LastChangedAt: at + 2}}}, new(ReportFollowResultReq), map[string]int64{
		"started_at": at, "finished_at": at + 1, "partitions.0.last_changed_at": at + 2,
	})
	assertTRPCJSONRoundTrip(t, &PullArchiveRunRsp{Runs: []*RunAssignment{{RunId: "r", ScheduledAt: at, StartedAt: at + 1, LastSuccessAt: at + 2,
		RegisteredPartitions: []*ArchivePartition{{PartitionKey: "2026-10-16", FinalizedAt: at + 3}}}}}, new(PullArchiveRunRsp), map[string]int64{
		"runs.0.scheduled_at": at, "runs.0.started_at": at + 1, "runs.0.last_success_at": at + 2, "runs.0.registered_partitions.0.finalized_at": at + 3,
	})
	assertTRPCJSONRoundTrip(t, &CompleteArchiveRunReq{RunId: "r", Partitions: []*ArchivePartition{{PartitionKey: "2026-10-16", LastChangedAt: at, ExportedAt: at + 1,
		FinalizedAt: at + 2, RemoteUploadedAt: at + 3, RemoteVerifiedAt: at + 4}}}, new(CompleteArchiveRunReq), map[string]int64{
		"partitions.0.last_changed_at": at, "partitions.0.exported_at": at + 1, "partitions.0.finalized_at": at + 2,
		"partitions.0.remote_uploaded_at": at + 3, "partitions.0.remote_verified_at": at + 4,
	})
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
