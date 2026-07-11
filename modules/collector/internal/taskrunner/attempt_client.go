package taskrunner

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	nodeRuntime "github.com/mooyang-code/moox/packages/cloudruntime"
	"google.golang.org/protobuf/types/known/structpb"
)

func executeMarketWithReceipt(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	attemptNo := item.AttemptNo
	if attemptNo <= 0 {
		attemptNo = 1
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	gateway := runtime.GetServiceGatewayTarget()
	var existing pb.GetMarketAttemptReceiptRsp
	if err := postCollectMgr(ctx, httpClient, gateway, "GetMarketAttemptReceipt", &pb.GetMarketAttemptReceiptReq{JobItemId: item.JobItemID, AttemptNo: int32(attemptNo)}, &existing); err != nil {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(err, "ATTEMPT_PREFLIGHT_FAILED")
	}
	if existing.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nodeRuntime.Result{}, nodeRuntime.Retryable(fmt.Errorf("attempt preflight: %s", existing.GetRetInfo().GetMsg()), "ATTEMPT_PREFLIGHT_FAILED")
	}
	if existing.GetFound() && existing.GetReceipt().GetFinalized() {
		return nodeRuntime.Result{Summary: existing.GetReceipt().GetSummary().AsMap()}, nil
	}
	result, businessErr := executeMarketFeed(ctx, item)
	status, errorClass := "success", ""
	subjectStatus := "success"
	if businessErr != nil {
		status, subjectStatus, errorClass = "failed", "temporary", "pipeline_failed"
	}
	summary, _ := structpb.NewStruct(result.Summary)
	subjects := []*pb.MarketAttemptSubject{}
	if taskID := stringValue(item.Params, "task_id"); taskID != "" {
		subjects = append(subjects, &pb.MarketAttemptSubject{TaskId: taskID, SubjectId: stringValue(item.Params, "subject_id"), Status: subjectStatus, Rows: int64(summaryNumber(result.Summary, "unified_rows")), ErrorClass: errorClass})
	}
	outbox := marketFollowUps(item, result.Summary, businessErr)
	req := &pb.FinalizeMarketAttemptReq{JobItemId: item.JobItemID, AttemptNo: int32(attemptNo), PlanId: stringValue(item.Params, "plan_id"), MarketId: stringValue(item.Params, "market_id"), SpaceId: firstString(item.SpaceID, stringValue(item.Params, "space_id")), ProviderId: stringValue(item.Params, "provider_id"), Feed: feedForJobType(item.JobType), Phase: firstString(stringValue(item.Params, "phase"), "fetch"), WindowStart: stringValue(item.Params, "start_time"), WindowEnd: stringValue(item.Params, "end_time"), Cursor: stringValue(item.Params, "cursor"), Status: status, Summary: summary, ErrorClass: errorClass, Subjects: subjects, Outbox: outbox}
	var finalized pb.FinalizeMarketAttemptRsp
	if err := postCollectMgr(ctx, httpClient, gateway, "FinalizeMarketAttempt", req, &finalized); err != nil {
		return result, nodeRuntime.Retryable(err, "ATTEMPT_FINALIZE_FAILED")
	}
	if finalized.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return result, nodeRuntime.Retryable(fmt.Errorf("finalize market attempt: %s", finalized.GetRetInfo().GetMsg()), "ATTEMPT_FINALIZE_FAILED")
	}
	return nodeRuntime.Result{Summary: finalized.GetReceipt().GetSummary().AsMap()}, nil
}

func executeMarketFeed(ctx context.Context, item nodeRuntime.JobItem) (nodeRuntime.Result, error) {
	switch item.JobType {
	case jobs.JobTypeCollectInstrument:
		return executeMarketInstrumentJobItem(ctx, item)
	case jobs.JobTypeCollectCalendar:
		return executeMarketCalendarJobItem(ctx, item)
	default:
		return executeMarketKlineJobItem(ctx, item)
	}
}
func feedForJobType(jobType string) string {
	switch jobType {
	case jobs.JobTypeCollectInstrument:
		return "instrument"
	case jobs.JobTypeCollectCalendar:
		return "calendar"
	default:
		return "kline"
	}
}
func summaryNumber(summary map[string]any, key string) int {
	switch value := summary[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	}
	return 0
}
func marketFollowUps(item nodeRuntime.JobItem, summary map[string]any, businessErr error) []*pb.MarketAttemptOutbox {
	var kind string
	payload := cloneParams(item.Params)
	if next, _ := summary["next_cursor"].(string); next != "" {
		kind, payload["cursor"] = "continuation", next
	} else if businessErr != nil {
		chain := stringSlice(item.Params["candidate_chain"])
		current := stringValue(item.Params, "provider_id")
		for index, provider := range chain {
			if provider == current && index+1 < len(chain) {
				next := chain[index+1]
				kind, payload["provider_id"], payload["candidate_index"] = "fallback", next, index+1
				if binding := nestedStringMap(item.Params["candidate_bindings"], next); binding != nil {
					for _, key := range []string{"source_dataset_id", "provider_symbol", "quota_scope_key"} {
						if value := binding[key]; value != nil {
							payload[key] = value
						}
					}
					if value := binding["quota_windows"]; value != nil {
						payload["quota_windows"] = value
					}
				}
				break
			}
		}
	}
	if kind == "" {
		return nil
	}
	value, _ := structpb.NewStruct(payload)
	return []*pb.MarketAttemptOutbox{{Kind: kind, Payload: value}}
}
func nestedStringMap(value any, key string) map[string]any {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	result, _ := raw[key].(map[string]any)
	return result
}
func cloneParams(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
func stringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			out = append(out, text)
		}
	}
	return out
}
func stringMap(value any) map[string]string {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for key, item := range raw {
		if text, ok := item.(string); ok && text != "" {
			out[key] = text
		}
	}
	return out
}
func providerIDSlice(value any) []marketdata.ProviderID {
	values := stringSlice(value)
	out := make([]marketdata.ProviderID, 0, len(values))
	for _, item := range values {
		out = append(out, marketdata.ProviderID(item))
	}
	return out
}
