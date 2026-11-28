package monitorpb

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// Monitor 接口里的墙钟时刻都是 Unix 毫秒：经服务端的 JSON 序列化往返不变，JSON 里是十进制数字字符串；
// HealthDataset 的两个水位与 expected_bar_at 是 data_time 文本，仍是普通字符串。
func TestMonitorTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	dataset := &HealthDataset{
		DatasetId: "bars", Freq: "1m",
		InputWatermarkAt: "2023-11-14T22:13:00Z", OutputWatermarkAt: "2023-11-14T22:12:00Z", ExpectedBarAt: "2023-11-14T22:12:00Z",
		LastSuccessAt: at, LastRunAt: at + 1, LastReportedAt: at + 2, LatestBarCloseAt: at + 3, ArrivedAt: at + 4, WaitingSince: at + 5, ExpectedBarCloseAt: at + 6,
	}
	component := &HealthComponent{
		ComponentId: "monitor", StatusSince: at, Reporter: &HealthSignal{CheckedAt: at + 1},
		Instances: []*HealthReporter{{InstanceId: "i", LastReportedAt: at + 2}},
		Lifecycle: &commonpb.LifecycleSnapshot{Stage: "resuming", Pause: &commonpb.LifecyclePause{Kind: "system", Since: at + 3}, Catchup: &commonpb.CatchupProgress{Since: at + 4}},
	}
	assertTRPCJSONRoundTrip(t, &HealthOverview{
		GeneratedAt:    at,
		Alerts:         []*HealthAlert{{Id: "a", TriggeredAt: at + 1, LastCheckedAt: at + 2, Dataset: dataset}},
		Components:     []*HealthComponent{component},
		Pipeline:       []*HealthPipelineStage{{Id: "storage", Datasets: []*HealthDataset{dataset}}},
		BusinessChecks: []*HealthBusinessCheck{{CheckId: "c", CheckedAt: at + 3}},
		Hosts:          []*HealthHost{{HostId: "control", LastReportedAt: at + 4}},
		Incidents:      []*AlertIncident{{Id: "x", TriggeredAt: at + 5, ResolvedAt: at + 6}},
		System:         &HealthSystem{Phase: "paused", PausedSince: at + 7},
	}, new(HealthOverview), map[string]int64{
		"generated_at": at, "alerts.0.triggered_at": at + 1, "alerts.0.last_checked_at": at + 2,
		"components.0.status_since": at, "components.0.reporter.checked_at": at + 1, "components.0.instances.0.last_reported_at": at + 2,
		"components.0.lifecycle.pause.since": at + 3, "components.0.lifecycle.catchup.since": at + 4,
		"pipeline.0.datasets.0.last_success_at": at, "pipeline.0.datasets.0.last_run_at": at + 1, "pipeline.0.datasets.0.last_reported_at": at + 2,
		"pipeline.0.datasets.0.latest_bar_close_at": at + 3, "pipeline.0.datasets.0.arrived_at": at + 4,
		"pipeline.0.datasets.0.waiting_since": at + 5, "pipeline.0.datasets.0.expected_bar_close_at": at + 6,
		"business_checks.0.checked_at": at + 3, "hosts.0.last_reported_at": at + 4,
		"incidents.0.triggered_at": at + 5, "incidents.0.resolved_at": at + 6, "system.paused_since": at + 7,
	}, map[string]string{
		"pipeline.0.datasets.0.input_watermark_at": "2023-11-14T22:13:00Z", "pipeline.0.datasets.0.output_watermark_at": "2023-11-14T22:12:00Z",
		"pipeline.0.datasets.0.expected_bar_at": "2023-11-14T22:12:00Z",
	})
	assertTRPCJSONRoundTrip(t, &NotificationChannel{ChannelId: "c", CreatedAt: at}, new(NotificationChannel), map[string]int64{"created_at": at}, nil)
	assertTRPCJSONRoundTrip(t, &SyncHostAgentReq{Host: &HostReport{ReportId: "r", CollectedAt: at}}, new(SyncHostAgentReq), map[string]int64{"host.collected_at": at}, nil)
	assertTRPCJSONRoundTrip(t, &SyncHostAgentRsp{MonitorStartedAt: at}, new(SyncHostAgentRsp), map[string]int64{"monitor_started_at": at}, nil)
	assertTRPCJSONRoundTrip(t, &ListHostAgentsRsp{Agents: []*HostAgentInfo{{HostId: "h", LastSeenAt: at}}}, new(ListHostAgentsRsp), map[string]int64{"agents.0.last_seen_at": at}, nil)
	assertTRPCJSONRoundTrip(t, &QueryHostMetricHistoryReq{HostId: "h", StartAt: at, EndAt: at + 1}, new(QueryHostMetricHistoryReq), map[string]int64{"start_at": at, "end_at": at + 1}, nil)
	assertTRPCJSONRoundTrip(t, &QueryHostMetricHistoryRsp{Points: []*HostMetricHistoryPoint{{HostId: "h", ObservedAt: at}}}, new(QueryHostMetricHistoryRsp), map[string]int64{"points.0.observed_at": at}, nil)
	assertTRPCJSONRoundTrip(t, &QueryPipelineArrivalHistoryRsp{
		Day: "2023-11-14", StartAt: at, EndAt: at + 1,
		Series: []*ArrivalHistorySeries{{Stage: "storage", Points: []*ArrivalHistoryPoint{{CloseAt: at + 2}}}},
	}, new(QueryPipelineArrivalHistoryRsp), map[string]int64{"start_at": at, "end_at": at + 1, "series.0.points.0.close_at": at + 2}, map[string]string{"day": "2023-11-14"})
	assertTRPCJSONRoundTrip(t, &GetAlertMuteRsp{State: &AlertMuteState{Muted: true, Since: at, ExpiresAt: at + 1}}, new(GetAlertMuteRsp), map[string]int64{"state.since": at, "state.expires_at": at + 1}, nil)
	assertTRPCJSONRoundTrip(t, &QueryLogRecordsReq{StartAt: at, EndAt: at + 1}, new(QueryLogRecordsReq), map[string]int64{"start_at": at, "end_at": at + 1}, nil)
	assertTRPCJSONRoundTrip(t, &QueryLogRecordsRsp{Records: []*LogRecord{{Id: 1, LoggedAt: at}}, Buckets: []*LogBucket{{StartAt: at + 1}}},
		new(QueryLogRecordsRsp), map[string]int64{"records.0.logged_at": at, "buckets.0.start_at": at + 1}, nil)
}

// assertTRPCJSONRoundTrip 用服务端实际使用的 JSON 序列化（trpc-go 注册的 SerializationTypeJSON，即 protojson）编码再解码：
// 解码后与原消息相同；millis 里每个字段路径（点分，数组用下标）在 JSON 里是十进制数字字符串，texts 里的是原样文本。
func assertTRPCJSONRoundTrip(t *testing.T, message, decoded proto.Message, millis map[string]int64, texts map[string]string) {
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
	want := make(map[string]string, len(millis)+len(texts))
	for path, value := range millis {
		want[path] = strconv.FormatInt(value, 10)
	}
	for path, value := range texts {
		want[path] = value
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
		if got, ok := node.(string); !ok || got != value {
			t.Fatalf("%s = %#v, want string %q in %s", path, node, value, raw)
		}
	}
}
