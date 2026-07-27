// Package taskpublisher contains Collector clients for CloudNode.
package taskpublisher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/jobs"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const submitJobItemBatchSize = 25

// AuthConfig describes HMAC auth for /api/service/cloudnode/* calls.
type AuthConfig struct {
	AccessKey   string
	SecretKey   string
	TargetNode  string
	CAFile      string
	CAPEMBase64 string
	ExpireSec   int64
}

// Config configures the CloudNode gateway client.
type Config struct {
	// ServiceGatewayTarget is the control-plane /api/service gateway used by Collector.
	ServiceGatewayTarget string
	Auth                 AuthConfig
}

// Client submits collector work to CloudNode through the admin service gateway.
type Client struct {
	serviceGatewayTarget string
	auth                 AuthConfig
	httpClient           *http.Client
	httpClientErr        error
}

// TerminalState is the CloudNode-authoritative terminal state of a JobItem.
type TerminalState struct {
	Terminal bool
	Status   int
	NodeID   string
	Result   string
}

// New creates a CloudNode client.
func New(cfg Config) *Client {
	controlTarget := strings.TrimSpace(cfg.ServiceGatewayTarget)
	httpClient, httpClientErr := runtimeapp.NewGatewayHTTPClient(8*time.Second, runtimeapp.AuthConfig{
		AccessKey:   cfg.Auth.AccessKey,
		SecretKey:   cfg.Auth.SecretKey,
		TargetNode:  cfg.Auth.TargetNode,
		CAFile:      cfg.Auth.CAFile,
		CAPEMBase64: cfg.Auth.CAPEMBase64,
		ExpireSec:   cfg.Auth.ExpireSec,
	})
	return &Client{
		serviceGatewayTarget: normalizeGatewayTarget(controlTarget),
		auth:                 cfg.Auth,
		httpClient:           httpClient,
		httpClientErr:        httpClientErr,
	}
}

// SubmitCollectorJobItems submits task instances as async CloudNode JobItems.
func (c *Client) SubmitCollectorJobItems(ctx context.Context, instances []domain.TaskInstance) (map[string]string, error) {
	jobItems := make([]*pb.JobItem, 0, len(instances))
	for _, instance := range instances {
		item, err := buildJobItem(instance)
		if err != nil {
			return nil, fmt.Errorf("build collector job item task_id=%s: %w", strings.TrimSpace(instance.TaskID), err)
		}
		jobItems = append(jobItems, item)
	}
	if len(jobItems) == 0 {
		return map[string]string{}, nil
	}
	idsByTaskID := make(map[string]string, len(jobItems))
	for start := 0; start < len(jobItems); start += submitJobItemBatchSize {
		if err := ctx.Err(); err != nil {
			return idsByTaskID, err
		}
		end := start + submitJobItemBatchSize
		if end > len(jobItems) {
			end = len(jobItems)
		}
		batch := append([]*pb.JobItem(nil), jobItems[start:end]...)
		ids, err := c.submitCollectorJobItemBatch(ctx, batch, start, end)
		for taskID, jobItemID := range ids {
			idsByTaskID[taskID] = jobItemID
		}
		if err != nil {
			return idsByTaskID, err
		}
	}
	return idsByTaskID, nil
}

// GetTerminalState returns a terminal state only after CloudNode has accepted
// the worker's final report. Pending queue items are deliberately not inferred
// from elapsed wall-clock time.
func (c *Client) GetTerminalState(ctx context.Context, spaceID, jobItemID string) (TerminalState, error) {
	raw, err := protojson.Marshal(&pb.GetJobItemReq{
		SpaceId: strings.TrimSpace(spaceID), JobItemId: strings.TrimSpace(jobItemID),
	})
	if err != nil {
		return TerminalState{}, fmt.Errorf("marshal get job item request: %w", err)
	}
	var rsp pb.GetJobItemRsp
	if err := c.postService(ctx, "cloudnode", "GetJobItem", raw, &rsp); err != nil {
		return TerminalState{}, fmt.Errorf("get cloud job item: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return TerminalState{}, fmt.Errorf("get cloud job item: %s", rsp.GetRetInfo().GetMsg())
	}
	item := rsp.GetItem()
	if item == nil {
		return TerminalState{}, fmt.Errorf("get cloud job item: empty item")
	}
	state := TerminalState{NodeID: strings.TrimSpace(item.GetExecutionNode())}
	switch item.GetStatus() {
	case pb.JobItemStatus_JOB_ITEM_STATUS_SUCCESS:
		state.Terminal = true
		state.Status = domain.InstanceStatusSuccess
	case pb.JobItemStatus_JOB_ITEM_STATUS_FAILED,
		pb.JobItemStatus_JOB_ITEM_STATUS_ENQUEUE_FAILED:
		state.Terminal = true
		state.Status = domain.InstanceStatusFailed
	default:
		return state, nil
	}
	result := map[string]any{}
	if item.GetResultSummary() != nil {
		result = item.GetResultSummary().AsMap()
	}
	if item.GetLastErrorMessage() != "" {
		result["error"] = item.GetLastErrorMessage()
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return TerminalState{}, fmt.Errorf("marshal cloud job item result: %w", err)
	}
	state.Result = string(encoded)
	return state, nil
}

func (c *Client) submitCollectorJobItemBatch(ctx context.Context, batch []*pb.JobItem, start, end int) (map[string]string, error) {
	raw, err := protojson.Marshal(&pb.SubmitJobItemsReq{Items: batch})
	if err != nil {
		return nil, fmt.Errorf("marshal submit job items request: %w", err)
	}
	var rsp pb.SubmitJobItemsRsp
	if err := c.postService(ctx, "cloudnode", "SubmitJobItems", raw, &rsp); err != nil {
		return nil, fmt.Errorf("submit cloud job items batch %d-%d: %w", start, end, err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("submit cloud job items batch %d-%d: %s", start, end, rsp.GetRetInfo().GetMsg())
	}
	return jobItemIDsByTaskID(batch, rsp.GetAcks())
}

func (c *Client) postService(ctx context.Context, service string, method string, body []byte, out proto.Message) error {
	if c.httpClientErr != nil {
		return c.httpClientErr
	}
	if c.serviceGatewayTarget == "" {
		return fmt.Errorf("service gateway target is required")
	}
	url := fmt.Sprintf("%s/api/service/%s/%s", c.serviceGatewayTarget, service, method)
	req, err := runtimeapp.NewSignedRequestWithContextAndHeaders(ctx, http.MethodPost, url, body, nil, runtimeapp.AuthConfig{
		AccessKey:   c.auth.AccessKey,
		SecretKey:   c.auth.SecretKey,
		TargetNode:  c.auth.TargetNode,
		CAFile:      c.auth.CAFile,
		CAPEMBase64: c.auth.CAPEMBase64,
		ExpireSec:   c.auth.ExpireSec,
	})
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status=%d body=%s", resp.StatusCode, string(respBody))
	}
	if len(bytes.TrimSpace(respBody)) == 0 {
		return nil
	}
	if out == nil {
		return nil
	}
	if err := protojson.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func normalizeGatewayTarget(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	return "http://" + raw
}

// PrepareScheduledInstances assigns the next execution boundary and stable queue identity.
func PrepareScheduledInstances(instances []domain.TaskInstance, now time.Time) ([]domain.TaskInstance, error) {
	prepared := append([]domain.TaskInstance(nil), instances...)
	for i := range prepared {
		payload, err := parsePayload(prepared[i].TaskParams)
		if err != nil {
			return nil, fmt.Errorf("parse task params task_id=%s: %w", strings.TrimSpace(prepared[i].TaskID), err)
		}
		executeAt := nextExecuteAt(now, valueString(payload, "schedule_interval", "30m"))
		prepared[i].ExecuteAt = executeAt
		prepared[i].CloudJobItemID = scheduledJobItemID(prepared[i].TaskID, executeAt)
	}
	return prepared, nil
}

func buildJobItem(instance domain.TaskInstance) (*pb.JobItem, error) {
	payload, err := parsePayload(instance.TaskParams)
	if err != nil {
		return nil, fmt.Errorf("parse task params: %w", err)
	}
	route, ok := jobs.JobRouteFor(instance.Exchange, instance.DataType)
	if !ok {
		return nil, fmt.Errorf("collector job route not found: exchange=%s data_type=%s",
			strings.TrimSpace(instance.Exchange), strings.TrimSpace(instance.DataType))
	}
	payload["space_id"] = instance.SpaceID
	payload["task_id"] = instance.TaskID
	if !instance.ExecuteAt.IsZero() {
		payload["schedule_window"] = instance.ExecuteAt.UTC().Format(time.RFC3339)
	}
	payloadStruct, err := structpb.NewStruct(payload)
	if err != nil {
		return nil, fmt.Errorf("build job item params: %w", err)
	}
	item := &pb.JobItem{
		SpaceId:   instance.SpaceID,
		JobId:     instance.RuleID,
		JobItemId: strings.TrimSpace(instance.CloudJobItemID),
		JobType:   route.JobType,
		Params:    payloadStruct,
		Priority:  100,
	}
	if item.JobItemId == "" {
		item.JobItemId = strings.TrimSpace(instance.TaskID)
	}
	if !instance.ExecuteAt.IsZero() {
		item.ExecuteAt = timestamppb.New(instance.ExecuteAt.UTC())
	}
	return item, nil
}

func jobItemIDsByTaskID(items []*pb.JobItem, acks []*pb.JobItemAck) (map[string]string, error) {
	taskByItemID := make(map[string]string, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		taskByItemID[strings.TrimSpace(item.GetJobItemId())] = taskIDFromJobItem(item)
	}
	out := make(map[string]string, len(acks))
	var rejected []error
	for _, ack := range acks {
		if ack == nil {
			continue
		}
		switch ack.GetStatus() {
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
			pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED:
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED:
			reason := strings.TrimSpace(ack.GetRejectReason())
			if reason == "" {
				reason = "rejected"
			}
			rejected = append(rejected, fmt.Errorf("job_item_id=%s rejected: %s",
				strings.TrimSpace(ack.GetJobItemId()), reason))
			continue
		default:
			continue
		}
		jobItemID := strings.TrimSpace(ack.GetJobItemId())
		taskID := taskByItemID[jobItemID]
		if taskID == "" || jobItemID == "" {
			continue
		}
		out[taskID] = jobItemID
	}
	return out, errors.Join(rejected...)
}

func taskIDFromJobItem(item *pb.JobItem) string {
	if item == nil {
		return ""
	}
	if params := item.GetParams(); params != nil {
		if value := params.GetFields()["task_id"].GetStringValue(); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(item.GetJobItemId())
}

func scheduledJobItemID(taskID string, executeAt time.Time) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || executeAt.IsZero() {
		return taskID
	}
	return taskID + ":" + executeAt.UTC().Format(time.RFC3339)
}

func parsePayload(raw string) (map[string]any, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		return nil, fmt.Errorf("task params must be a JSON object")
	}
	return payload, nil
}

func valueString(payload map[string]any, key string, fallback string) string {
	if v, ok := payload[key].(string); ok && strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

func nextExecuteAt(now time.Time, interval string) time.Time {
	duration, ok := parseScheduleDuration(interval)
	if !ok || duration <= 0 {
		duration = 30 * time.Minute
	}
	return now.UTC().Truncate(duration).Add(duration)
}

func parseScheduleDuration(interval string) (time.Duration, bool) {
	interval = strings.TrimSpace(interval)
	if interval == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(interval); err == nil {
		return d, true
	}
	if strings.HasSuffix(interval, "d") {
		n := strings.TrimSuffix(interval, "d")
		if n == "" {
			n = "1"
		}
		var days int
		if _, err := fmt.Sscanf(n, "%d", &days); err == nil && days > 0 {
			return time.Duration(days) * 24 * time.Hour, true
		}
	}
	return 0, false
}
