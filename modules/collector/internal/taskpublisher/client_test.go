package taskpublisher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildJobItemUsesCollectorRoutingFields(t *testing.T) {
	item := buildJobItem(domain.TaskInstance{
		SpaceID: "space-a",
		RuleID:  "rule-1",
		TaskID:  "task-1",
		TaskParams: `{
			"exchange":"binance",
			"data_type":"symbol",
			"job_type":"collect.symbol",
			"code_package_id":"moox-collector_dev"
		}`,
	})

	if item.GetSpaceId() != "space-a" || item.GetJobId() != "rule-1" {
		t.Fatalf("identity fields = space:%q job:%q item:%q", item.GetSpaceId(), item.GetJobId(), item.GetJobItemId())
	}
	if item.GetJobType() != "collect.symbol" {
		t.Fatalf("job_type = %q, want collect.symbol", item.GetJobType())
	}
	if item.GetCodePackageId() != "moox-collector_dev" {
		t.Fatalf("code_package_id = %q, want moox-collector_dev", item.GetCodePackageId())
	}
	if item.GetParams().GetFields()["task_id"].GetStringValue() != "task-1" {
		t.Fatalf("params.task_id = %q, want task-1", item.GetParams().GetFields()["task_id"].GetStringValue())
	}
}

func TestBuildJobItemUsesWindowedJobItemIDAndStableTaskID(t *testing.T) {
	item := buildJobItem(domain.TaskInstance{
		SpaceID: "space-a",
		RuleID:  "rule-1",
		TaskID:  "task-1",
		TaskParams: `{
			"exchange":"binance",
			"data_type":"kline",
			"schedule_interval":"1m"
		}`,
	})

	fields := item.GetParams().GetFields()
	window := fields["schedule_window"].GetStringValue()
	if window == "" {
		t.Fatalf("params.schedule_window is empty")
	}
	if fields["task_id"].GetStringValue() != "task-1" {
		t.Fatalf("params.task_id = %q, want task-1", fields["task_id"].GetStringValue())
	}
	if item.GetJobItemId() == "task-1" {
		t.Fatalf("job_item_id = %q, want a windowed id", item.GetJobItemId())
	}
	if item.GetJobItemId() != "task-1:"+window {
		t.Fatalf("job_item_id = %q, want task-1:%s", item.GetJobItemId(), window)
	}
}

func TestBuildJobItemDefaultsRoutingFromDataType(t *testing.T) {
	item := buildJobItem(domain.TaskInstance{
		SpaceID:    "space-a",
		RuleID:     "rule-1",
		TaskID:     "task-1",
		TaskParams: `{"exchange":"binance","data_type":"kline"}`,
	})

	if item.GetJobType() != "collect.kline" {
		t.Fatalf("job_type = %q, want collect.kline", item.GetJobType())
	}
	if item.GetCodePackageId() != "moox-collector_dev" {
		t.Fatalf("code_package_id = %q, want moox-collector_dev", item.GetCodePackageId())
	}
}

func TestJobItemIDsByTaskIDUsesAckedJobItemID(t *testing.T) {
	got := jobItemIDsByTaskID(
		[]*cloudnodepb.JobItem{
			{
				JobItemId: "task-1:2026-07-04T09:07:00Z",
				Params:    mapStruct(t, map[string]any{"task_id": "task-1"}),
			},
			{
				JobItemId: "task-2:2026-07-04T09:07:00Z",
				Params:    mapStruct(t, map[string]any{"task_id": "task-2"}),
			},
		},
		[]*cloudnodepb.JobItemAck{
			{JobItemId: "task-1:2026-07-04T09:07:00Z", Status: cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED},
			{JobItemId: "task-2:2026-07-04T09:07:00Z", Status: cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED},
			{JobItemId: "missing", Status: cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED},
		},
	)

	if len(got) != 1 || got["task-2"] != "task-2:2026-07-04T09:07:00Z" {
		t.Fatalf("ids = %#v, want task-2 mapped to windowed job item id", got)
	}
}

func TestWakeCollectorNodesSetsSpaceHeaderAndInvokesMatchingNodes(t *testing.T) {
	var invoked bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Space-Id") != "crypto" {
			t.Fatalf("X-Space-Id = %q, want crypto", r.Header.Get("X-Space-Id"))
		}
		switch r.URL.Path {
		case "/api/service/cloudnode/GetNodeList":
			writeProtoJSON(t, w, &cloudnodepb.GetNodeListRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Items: []*cloudnodepb.CloudNode{
					{NodeId: "node-a", SupportedWorkloads: []string{"kline"}},
					{NodeId: "node-b", SupportedWorkloads: []string{"collect.symbol"}},
				},
			})
		case "/api/service/cloudnode/InvokeFunction":
			invoked = true
			var req cloudnodepb.InvokeFunctionReq
			if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
				t.Fatal(err)
			}
			if req.GetNodeId() != "node-a" {
				t.Fatalf("node_id = %q, want node-a", req.GetNodeId())
			}
			if req.GetScfInvokeType() != cloudnodepb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT {
				t.Fatalf("invoke type = %v, want event", req.GetScfInvokeType())
			}
			event := req.GetEventData().AsMap()
			if event["service_gateway_target"] != server.URL {
				t.Fatalf("event service_gateway_target = %#v, want %s", event["service_gateway_target"], server.URL)
			}
			if _, ok := event["server_ip"]; ok {
				t.Fatalf("event should not include server_ip: %#v", event)
			}
			if _, ok := event["server_port"]; ok {
				t.Fatalf("event should not include server_port: %#v", event)
			}
			if event["storage_metadata_target"] != "127.0.0.1:20100" || event["storage_access_target"] != "127.0.0.1:20102" {
				t.Fatalf("event storage targets = %#v", event)
			}
			data, ok := event["data"].(map[string]any)
			if !ok || data["node_id"] != "node-a" {
				t.Fatalf("event data.node_id = %#v, want node-a", event["data"])
			}
			writeProtoJSON(t, w, &cloudnodepb.InvokeFunctionRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Scf:     &cloudnodepb.ScfInvokeResult{},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := New(Config{
		ServiceGatewayTarget:  server.URL,
		StorageMetadataTarget: "127.0.0.1:20100",
		StorageAccessTarget:   "127.0.0.1:20102",
		Auth: AuthConfig{
			AccessKey: "ak",
			SecretKey: "sk",
		},
	})

	count, err := client.WakeCollectorNodes(context.Background(), WakeOptions{
		SpaceID:  "crypto",
		JobTypes: []string{"collect.kline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || !invoked {
		t.Fatalf("wake count=%d invoked=%v, want one invoke", count, invoked)
	}
}

func TestWakeCollectorNodesPaginatesNodeList(t *testing.T) {
	pages := []uint32{}
	invoked := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/cloudnode/GetNodeList":
			var req cloudnodepb.GetNodeListReq
			if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
				t.Fatal(err)
			}
			page := req.GetPage().GetPage()
			pages = append(pages, page)
			if page == 1 {
				writeProtoJSON(t, w, &cloudnodepb.GetNodeListRsp{
					RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
					Items: []*cloudnodepb.CloudNode{
						{NodeId: "node-a", SupportedWorkloads: []string{"collect.kline"}},
					},
					Page: &commonpb.PageResult{Page: 1, Size: wakeNodeListPageSize, Total: 2, HasMore: true},
				})
				return
			}
			writeProtoJSON(t, w, &cloudnodepb.GetNodeListRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Items: []*cloudnodepb.CloudNode{
					{NodeId: "node-b", SupportedWorkloads: []string{"collect.kline"}},
				},
				Page: &commonpb.PageResult{Page: 2, Size: wakeNodeListPageSize, Total: 2, HasMore: false},
			})
		case "/api/service/cloudnode/InvokeFunction":
			var req cloudnodepb.InvokeFunctionReq
			if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
				t.Fatal(err)
			}
			invoked = append(invoked, req.GetNodeId())
			writeProtoJSON(t, w, &cloudnodepb.InvokeFunctionRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Scf:     &cloudnodepb.ScfInvokeResult{},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(Config{
		ServiceGatewayTarget: server.URL,
		Auth: AuthConfig{
			AccessKey: "ak",
			SecretKey: "sk",
		},
	})

	count, err := client.WakeCollectorNodes(context.Background(), WakeOptions{
		SpaceID:  "crypto",
		JobTypes: []string{"collect.kline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("wake count=%d, want 2", count)
	}
	if strings.Join(invoked, ",") != "node-a,node-b" {
		t.Fatalf("invoked=%v, want both pages", invoked)
	}
	if len(pages) != 2 || strings.Join([]string{fmt.Sprint(pages[0]), fmt.Sprint(pages[1])}, ",") != "1,2" {
		t.Fatalf("pages=%v, want 1,2", pages)
	}
}

func TestWakeCollectorNodesContinuesAfterFailedNode(t *testing.T) {
	invoked := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/service/cloudnode/GetNodeList":
			writeProtoJSON(t, w, &cloudnodepb.GetNodeListRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Items: []*cloudnodepb.CloudNode{
					{NodeId: "bad-node", SupportedWorkloads: []string{"collect.kline"}},
					{NodeId: "good-node", SupportedWorkloads: []string{"collect.kline"}},
				},
			})
		case "/api/service/cloudnode/InvokeFunction":
			var req cloudnodepb.InvokeFunctionReq
			if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
				t.Fatal(err)
			}
			invoked = append(invoked, req.GetNodeId())
			if req.GetNodeId() == "bad-node" {
				writeProtoJSON(t, w, &cloudnodepb.InvokeFunctionRsp{
					RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_INNER_ERR, Msg: "cloud account not found"},
				})
				return
			}
			writeProtoJSON(t, w, &cloudnodepb.InvokeFunctionRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
				Scf:     &cloudnodepb.ScfInvokeResult{},
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := New(Config{
		ServiceGatewayTarget: server.URL,
		Auth: AuthConfig{
			AccessKey: "ak",
			SecretKey: "sk",
		},
	})

	count, err := client.WakeCollectorNodes(context.Background(), WakeOptions{
		SpaceID:  "crypto",
		JobTypes: []string{"collect.kline"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 || strings.Join(invoked, ",") != "bad-node,good-node" {
		t.Fatalf("count=%d invoked=%v, want both attempted with one success", count, invoked)
	}
}

func TestScheduleWindowParsesDurationAndDayIntervals(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 17, 42, 0, time.UTC)

	tests := []struct {
		name     string
		interval string
		want     string
		ok       bool
	}{
		{name: "duration", interval: "15m", want: now.Truncate(15 * time.Minute).Format(time.RFC3339), ok: true},
		{name: "single day", interval: "d", want: now.Truncate(24 * time.Hour).Format(time.RFC3339), ok: true},
		{name: "multi day", interval: "2d", want: now.Truncate(48 * time.Hour).Format(time.RFC3339), ok: true},
		{name: "invalid falls back", interval: "bad", want: now.Truncate(30 * time.Minute).Format(time.RFC3339), ok: false},
		{name: "empty falls back", interval: "", want: now.Truncate(30 * time.Minute).Format(time.RFC3339), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseScheduleDuration(tt.interval)
			if ok != tt.ok {
				t.Fatalf("parseScheduleDuration ok=%v, want %v", ok, tt.ok)
			}
			if got := scheduleWindow(now, tt.interval); got != tt.want {
				t.Fatalf("scheduleWindow=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestPayloadAndJobItemHelpersCoverFallbacks(t *testing.T) {
	if payload := parsePayload("{bad"); len(payload) != 0 {
		t.Fatalf("parsePayload invalid = %#v, want empty", payload)
	}
	if got := valueString(map[string]any{"name": "  "}, "name", "fallback"); got != "fallback" {
		t.Fatalf("valueString blank = %q, want fallback", got)
	}
	if got := taskIDFromJobItem(&cloudnodepb.JobItem{JobItemId: " item-1 "}); got != "item-1" {
		t.Fatalf("taskIDFromJobItem fallback = %q, want item-1", got)
	}
	if got := windowedJobItemID("", "window"); got != "" {
		t.Fatalf("windowed empty task = %q, want empty", got)
	}
	if !supportsAnyJobType([]string{"collect.kline"}, []string{" kline "}) {
		t.Fatal("supportsAnyJobType should normalize collect prefix")
	}
	if supportsAnyJobType([]string{"symbol"}, []string{"kline"}) {
		t.Fatal("supportsAnyJobType should reject missing job type")
	}
}

func mapStruct(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	out, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func writeProtoJSON(t *testing.T, w http.ResponseWriter, msg proto.Message) {
	t.Helper()
	raw, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(raw)
}

func readRequestBody(t *testing.T, r *http.Request) []byte {
	t.Helper()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		t.Fatal("empty body")
	}
	return raw
}
