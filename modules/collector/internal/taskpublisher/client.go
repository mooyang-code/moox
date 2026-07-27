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
	"sync"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	submitJobItemBatchSize   = 25
	submitJobItemConcurrency = 8
)

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
		jobItems = append(jobItems, buildJobItem(instance))
	}
	if len(jobItems) == 0 {
		return map[string]string{}, nil
	}
	idsByTaskID := make(map[string]string, len(jobItems))
	var idsMu sync.Mutex
	var group errgroup.Group
	semaphore := make(chan struct{}, submitJobItemConcurrency)
	var batchErrors []error
	for start := 0; start < len(jobItems); start += submitJobItemBatchSize {
		end := start + submitJobItemBatchSize
		if end > len(jobItems) {
			end = len(jobItems)
		}
		batchStart, batchEnd := start, end
		batch := append([]*pb.JobItem(nil), jobItems[start:end]...)
		group.Go(func() error {
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				idsMu.Lock()
				batchErrors = append(batchErrors, ctx.Err())
				idsMu.Unlock()
				return nil
			}
			defer func() { <-semaphore }()

			ids, err := c.submitCollectorJobItemBatch(ctx, batch, batchStart, batchEnd)
			idsMu.Lock()
			for taskID, jobItemID := range ids {
				idsByTaskID[taskID] = jobItemID
			}
			if err != nil {
				batchErrors = append(batchErrors, err)
				idsMu.Unlock()
				return nil
			}
			idsMu.Unlock()
			return nil
		})
	}
	_ = group.Wait()
	return idsByTaskID, errors.Join(batchErrors...)
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
func PrepareScheduledInstances(instances []domain.TaskInstance, now time.Time) []domain.TaskInstance {
	prepared := append([]domain.TaskInstance(nil), instances...)
	for i := range prepared {
		payload := parsePayload(prepared[i].TaskParams)
		executeAt := nextExecuteAt(now, valueString(payload, "schedule_interval", "30m"))
		prepared[i].ExecuteAt = executeAt
		prepared[i].CloudJobItemID = scheduledJobItemID(prepared[i].TaskID, executeAt)
	}
	return prepared
}

func buildJobItem(instance domain.TaskInstance) *pb.JobItem {
	payload := parsePayload(instance.TaskParams)
	payload["space_id"] = instance.SpaceID
	payload["task_id"] = instance.TaskID
	if !instance.ExecuteAt.IsZero() {
		payload["schedule_window"] = instance.ExecuteAt.UTC().Format(time.RFC3339)
	}
	payloadStruct, _ := structpb.NewStruct(payload)
	item := &pb.JobItem{
		SpaceId:   instance.SpaceID,
		JobId:     instance.RuleID,
		JobItemId: strings.TrimSpace(instance.CloudJobItemID),
		JobType:   jobType(payload),
		Params:    payloadStruct,
		Priority:  100,
	}
	if item.JobItemId == "" {
		item.JobItemId = strings.TrimSpace(instance.TaskID)
	}
	if !instance.ExecuteAt.IsZero() {
		item.ExecuteAt = timestamppb.New(instance.ExecuteAt.UTC())
	}
	return item
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

func jobType(payload map[string]any) string {
	if value := valueString(payload, "job_type", ""); value != "" {
		return value
	}
	dataType := valueString(payload, "data_type", "kline")
	return "collect." + dataType
}

func parsePayload(raw string) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return map[string]any{}
	}
	return payload
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
