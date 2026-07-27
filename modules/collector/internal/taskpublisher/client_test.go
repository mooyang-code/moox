package taskpublisher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildScheduledJobItemUsesNextBoundary(t *testing.T) {
	now := time.Date(2026, 7, 26, 10, 17, 42, 0, time.UTC)
	instances := mustPrepareScheduledInstances(t, []domain.TaskInstance{{
		SpaceID:  "space-a",
		RuleID:   "rule-1",
		TaskID:   "task-1",
		Exchange: "binance",
		DataType: "symbol",
		TaskParams: `{
			"exchange":"binance",
			"data_type":"symbol",
			"job_type":"collect.untrusted",
			"schedule_interval":"30m"
			}`,
	}}, now)
	item := mustBuildJobItem(t, instances[0])

	if item.GetSpaceId() != "space-a" || item.GetJobId() != "rule-1" {
		t.Fatalf("identity fields = space:%q job:%q item:%q", item.GetSpaceId(), item.GetJobId(), item.GetJobItemId())
	}
	if item.GetJobType() != "collect.binance.symbol" {
		t.Fatalf("job_type = %q, want collect.binance.symbol", item.GetJobType())
	}
	if item.GetParams().GetFields()["task_id"].GetStringValue() != "task-1" {
		t.Fatalf("params.task_id = %q, want task-1", item.GetParams().GetFields()["task_id"].GetStringValue())
	}
	wantExecuteAt := time.Date(2026, 7, 26, 10, 30, 0, 0, time.UTC)
	if item.GetExecuteAt() == nil || !item.GetExecuteAt().AsTime().Equal(wantExecuteAt) {
		t.Fatalf("execute_at = %v, want %v", item.GetExecuteAt(), wantExecuteAt)
	}
	if got := item.GetJobItemId(); got != "task-1:2026-07-26T10:30:00Z" {
		t.Fatalf("job_item_id = %q, want deterministic execute_at id", got)
	}
}

func TestPrepareScheduledInstancesRepeatedInSameWindowUsesSameJobItemID(t *testing.T) {
	instance := domain.TaskInstance{
		SpaceID: "space-a",
		RuleID:  "rule-1",
		TaskID:  "task-1",
		TaskParams: `{
			"exchange":"binance",
			"data_type":"kline",
				"schedule_interval":"1m"
			}`,
	}
	first := mustPrepareScheduledInstances(t, []domain.TaskInstance{instance},
		time.Date(2026, 7, 26, 10, 17, 1, 0, time.UTC))[0]
	second := mustPrepareScheduledInstances(t, []domain.TaskInstance{instance},
		time.Date(2026, 7, 26, 10, 17, 59, 0, time.UTC))[0]

	if first.CloudJobItemID != second.CloudJobItemID {
		t.Fatalf("job ids differ in one window: %q != %q", first.CloudJobItemID, second.CloudJobItemID)
	}
	if !first.ExecuteAt.Equal(second.ExecuteAt) {
		t.Fatalf("execute_at differs in one window: %v != %v", first.ExecuteAt, second.ExecuteAt)
	}
}

func TestBuildJobItemDerivesRoutingFromTaskInstance(t *testing.T) {
	instances := mustPrepareScheduledInstances(t, []domain.TaskInstance{{
		SpaceID:    "space-a",
		RuleID:     "rule-1",
		TaskID:     "task-1",
		Exchange:   " Binance ",
		DataType:   " KLINE ",
		TaskParams: `{"exchange":"tushare","data_type":"symbol","job_type":"collect.untrusted"}`,
	}}, time.Date(2026, 7, 26, 10, 17, 42, 0, time.UTC))
	item := mustBuildJobItem(t, instances[0])

	if item.GetJobType() != "collect.binance.kline" {
		t.Fatalf("job_type = %q, want collect.binance.kline", item.GetJobType())
	}
}

func TestBuildJobItemRejectsUnknownTaskInstanceRoute(t *testing.T) {
	_, err := buildJobItem(domain.TaskInstance{
		TaskID:     "task-1",
		Exchange:   "tushare",
		DataType:   "kline",
		TaskParams: `{}`,
	})
	if err == nil || !strings.Contains(err.Error(), "tushare") ||
		!strings.Contains(err.Error(), "kline") {
		t.Fatalf("buildJobItem() error = %v, want unknown route details", err)
	}
}

func TestClientGetTerminalStateUsesCloudNodeAuthority(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/service/cloudnode/GetJobItem" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		raw, err := protojson.Marshal(&cloudnodepb.GetJobItemRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Item: &cloudnodepb.JobItemDetail{
				SpaceId: "crypto", JobItemId: "item-1",
				Status:           cloudnodepb.JobItemStatus_JOB_ITEM_STATUS_FAILED,
				ResultSummary:    mustStruct(t, map[string]any{"attempts": float64(4)}),
				LastErrorMessage: "upstream timeout", ExecutionNode: "scf-a",
			},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	client := New(Config{
		ServiceGatewayTarget: server.URL,
		Auth:                 AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "control"},
	})
	state, err := client.GetTerminalState(context.Background(), "crypto", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Terminal || state.Status != domain.InstanceStatusFailed || state.NodeID != "scf-a" {
		t.Fatalf("state = %+v", state)
	}
	if !strings.Contains(state.Result, "upstream timeout") {
		t.Fatalf("result = %s", state.Result)
	}
}

func TestClientGetTerminalStateKeepsEnqueueFailedNonTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		raw, err := protojson.Marshal(&cloudnodepb.GetJobItemRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Item: &cloudnodepb.JobItemDetail{
				SpaceId: "crypto", JobItemId: "item-1",
				Status: cloudnodepb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED,
			},
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()

	client := New(Config{
		ServiceGatewayTarget: server.URL,
		Auth:                 AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "control"},
	})
	state, err := client.GetTerminalState(context.Background(), "crypto", "item-1")
	if err != nil {
		t.Fatal(err)
	}
	if state.Terminal {
		t.Fatalf("enqueue_failed must remain recoverable: %+v", state)
	}
}

func mustStruct(t *testing.T, values map[string]any) *structpb.Struct {
	t.Helper()
	value, err := structpb.NewStruct(values)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSubmitCollectorJobItemsRejectsInvalidItemsBeforeRequest(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	client := New(Config{ServiceGatewayTarget: server.URL})

	tests := []struct {
		name     string
		instance domain.TaskInstance
	}{
		{
			name: "malformed params",
			instance: domain.TaskInstance{
				TaskID: "task-bad-json", Exchange: "binance", DataType: "kline", TaskParams: `{bad`,
			},
		},
		{
			name: "unknown route",
			instance: domain.TaskInstance{
				TaskID: "task-unknown-route", Exchange: "tushare", DataType: "kline", TaskParams: `{}`,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := client.SubmitCollectorJobItems(context.Background(), []domain.TaskInstance{tt.instance})
			if err == nil || !strings.Contains(err.Error(), tt.instance.TaskID) {
				t.Fatalf("SubmitCollectorJobItems() error = %v, want task identity", err)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("requests = %d, want 0", requests)
	}
}

func TestPrepareScheduledInstancesRejectsMalformedTaskParams(t *testing.T) {
	_, err := PrepareScheduledInstances([]domain.TaskInstance{{
		TaskID:     "task-1",
		TaskParams: `{bad`,
	}}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "task-1") {
		t.Fatalf("PrepareScheduledInstances() error = %v, want task identity", err)
	}
}

func TestJobItemIDsByTaskIDReturnsRejectedAckDetailsAndSuccessfulIDs(t *testing.T) {
	got, err := jobItemIDsByTaskID(
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
			{
				JobItemId:    "task-1:2026-07-04T09:07:00Z",
				Status:       cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED,
				RejectReason: "invalid collector params",
			},
			{JobItemId: "task-2:2026-07-04T09:07:00Z", Status: cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED},
			{JobItemId: "missing", Status: cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED},
		},
	)

	if err == nil ||
		!strings.Contains(err.Error(), "task-1:2026-07-04T09:07:00Z") ||
		!strings.Contains(err.Error(), "invalid collector params") {
		t.Fatalf("error = %v, want rejected job_item_id and reject_reason", err)
	}
	if len(got) != 1 || got["task-2"] != "task-2:2026-07-04T09:07:00Z" {
		t.Fatalf("ids = %#v, want task-2 mapped to windowed job item id", got)
	}
}

func TestSubmitCollectorJobItemsBatchesLargeRequests(t *testing.T) {
	requestSizes := []int{}
	var requestSizesMu sync.Mutex
	inFlight := 0
	maxInFlight := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cloudnodepb.SubmitJobItemsReq
		if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
			t.Fatal(err)
		}
		requestSizesMu.Lock()
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		requestSizes = append(requestSizes, len(req.GetItems()))
		requestSizesMu.Unlock()
		time.Sleep(20 * time.Millisecond)
		defer func() {
			requestSizesMu.Lock()
			inFlight--
			requestSizesMu.Unlock()
		}()
		acks := make([]*cloudnodepb.JobItemAck, 0, len(req.GetItems()))
		for _, item := range req.GetItems() {
			acks = append(acks, &cloudnodepb.JobItemAck{
				JobItemId: item.GetJobItemId(),
				Status:    cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
			})
		}
		writeProtoJSON(t, w, &cloudnodepb.SubmitJobItemsRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Acks:    acks,
		})
	}))
	defer server.Close()

	instances := make([]domain.TaskInstance, 0, submitJobItemBatchSize+1)
	for i := 0; i < submitJobItemBatchSize+1; i++ {
		instances = append(instances, domain.TaskInstance{
			SpaceID:    "crypto",
			RuleID:     "binance_spot_kline_1m",
			TaskID:     fmt.Sprintf("task-%d", i),
			Exchange:   "binance",
			DataType:   "kline",
			TaskParams: `{"data_type":"kline","schedule_interval":"1m"}`,
		})
	}
	instances = mustPrepareScheduledInstances(t, instances, time.Date(2026, 7, 26, 10, 17, 42, 0, time.UTC))
	client := New(Config{
		ServiceGatewayTarget: server.URL,
		Auth:                 AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway-gz-122"},
	})

	ids, err := client.SubmitCollectorJobItems(context.Background(), instances)
	if err != nil {
		t.Fatal(err)
	}
	if len(requestSizes) != 2 || requestSizes[0] != submitJobItemBatchSize || requestSizes[1] != 1 {
		t.Fatalf("request sizes = %v, want [%d 1]", requestSizes, submitJobItemBatchSize)
	}
	if maxInFlight != 1 {
		t.Fatalf("max in-flight submit batches = %d, want 1", maxInFlight)
	}
	if len(ids) != len(instances) {
		t.Fatalf("acked task ids = %d, want %d", len(ids), len(instances))
	}
}

func TestSubmitCollectorJobItemsReturnsSuccessfulBatchesWithError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cloudnodepb.SubmitJobItemsReq
		if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
			t.Fatal(err)
		}
		if len(req.GetItems()) == 1 {
			writeProtoJSON(t, w, &cloudnodepb.SubmitJobItemsRsp{
				RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_INNER_ERR, Msg: "rejected batch"},
			})
			return
		}
		acks := make([]*cloudnodepb.JobItemAck, 0, len(req.GetItems()))
		for _, item := range req.GetItems() {
			acks = append(acks, &cloudnodepb.JobItemAck{JobItemId: item.GetJobItemId(), Status: cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED})
		}
		writeProtoJSON(t, w, &cloudnodepb.SubmitJobItemsRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"}, Acks: acks,
		})
	}))
	defer server.Close()

	instances := make([]domain.TaskInstance, 0, submitJobItemBatchSize+1)
	for i := 0; i < submitJobItemBatchSize+1; i++ {
		instances = append(instances, domain.TaskInstance{
			SpaceID: "crypto", RuleID: "rule", TaskID: fmt.Sprintf("task-%d", i),
			Exchange: "binance", DataType: "kline", TaskParams: `{}`,
		})
	}
	instances = mustPrepareScheduledInstances(t, instances, time.Date(2026, 7, 26, 10, 17, 42, 0, time.UTC))
	client := New(Config{ServiceGatewayTarget: server.URL, Auth: AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway"}})
	ids, err := client.SubmitCollectorJobItems(context.Background(), instances)
	if err == nil || !strings.Contains(err.Error(), "rejected batch") {
		t.Fatalf("error = %v, want rejected batch", err)
	}
	if len(ids) != submitJobItemBatchSize {
		t.Fatalf("successful ids = %d, want %d", len(ids), submitJobItemBatchSize)
	}
}

func TestSubmitCollectorJobItemsKeepsSuccessfulIDsFromRejectedAckBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cloudnodepb.SubmitJobItemsReq
		if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
			t.Fatal(err)
		}
		writeProtoJSON(t, w, &cloudnodepb.SubmitJobItemsRsp{
			RetInfo: &cloudnodepb.RetInfo{Code: cloudnodepb.ErrorCode_SUCCESS, Msg: "ok"},
			Acks: []*cloudnodepb.JobItemAck{
				{
					JobItemId: req.GetItems()[0].GetJobItemId(),
					Status:    cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
				},
				{
					JobItemId:    req.GetItems()[1].GetJobItemId(),
					Status:       cloudnodepb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED,
					RejectReason: "unsupported job type",
				},
			},
		})
	}))
	defer server.Close()

	instances := mustPrepareScheduledInstances(t, []domain.TaskInstance{
		{SpaceID: "crypto", RuleID: "rule", TaskID: "task-ok", Exchange: "binance", DataType: "kline", TaskParams: `{}`},
		{SpaceID: "crypto", RuleID: "rule", TaskID: "task-bad", Exchange: "binance", DataType: "symbol", TaskParams: `{}`},
	}, time.Date(2026, 7, 26, 10, 17, 42, 0, time.UTC))
	client := New(Config{ServiceGatewayTarget: server.URL, Auth: AuthConfig{AccessKey: "ak", SecretKey: "sk", TargetNode: "gateway"}})

	ids, err := client.SubmitCollectorJobItems(context.Background(), instances)
	if err == nil || !strings.Contains(err.Error(), instances[1].CloudJobItemID) ||
		!strings.Contains(err.Error(), "unsupported job type") {
		t.Fatalf("error = %v, want rejected ACK details", err)
	}
	if got := ids["task-ok"]; got != instances[0].CloudJobItemID {
		t.Fatalf("successful id = %q, want %q", got, instances[0].CloudJobItemID)
	}
}

func TestNextExecuteAtParsesDurationAndDayIntervals(t *testing.T) {
	now := time.Date(2026, 7, 12, 10, 17, 42, 0, time.UTC)

	tests := []struct {
		name     string
		interval string
		want     string
		ok       bool
	}{
		{name: "duration", interval: "15m", want: now.Truncate(15 * time.Minute).Add(15 * time.Minute).Format(time.RFC3339), ok: true},
		{name: "single day", interval: "d", want: now.Truncate(24 * time.Hour).Add(24 * time.Hour).Format(time.RFC3339), ok: true},
		{name: "multi day", interval: "2d", want: now.Truncate(48 * time.Hour).Add(48 * time.Hour).Format(time.RFC3339), ok: true},
		{name: "invalid falls back", interval: "bad", want: now.Truncate(30 * time.Minute).Add(30 * time.Minute).Format(time.RFC3339), ok: false},
		{name: "empty falls back", interval: "", want: now.Truncate(30 * time.Minute).Add(30 * time.Minute).Format(time.RFC3339), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseScheduleDuration(tt.interval)
			if ok != tt.ok {
				t.Fatalf("parseScheduleDuration ok=%v, want %v", ok, tt.ok)
			}
			if got := nextExecuteAt(now, tt.interval).Format(time.RFC3339); got != tt.want {
				t.Fatalf("nextExecuteAt=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestPayloadAndJobItemHelpersCoverFallbacks(t *testing.T) {
	if got := valueString(map[string]any{"name": "  "}, "name", "fallback"); got != "fallback" {
		t.Fatalf("valueString blank = %q, want fallback", got)
	}
	if got := taskIDFromJobItem(&cloudnodepb.JobItem{JobItemId: " item-1 "}); got != "item-1" {
		t.Fatalf("taskIDFromJobItem fallback = %q, want item-1", got)
	}
	if got := scheduledJobItemID("", time.Now()); got != "" {
		t.Fatalf("scheduled empty task = %q, want empty", got)
	}
}

func mustPrepareScheduledInstances(t *testing.T, instances []domain.TaskInstance, now time.Time) []domain.TaskInstance {
	t.Helper()
	prepared, err := PrepareScheduledInstances(instances, now)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func mustBuildJobItem(t *testing.T, instance domain.TaskInstance) *cloudnodepb.JobItem {
	t.Helper()
	item, err := buildJobItem(instance)
	if err != nil {
		t.Fatal(err)
	}
	return item
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
