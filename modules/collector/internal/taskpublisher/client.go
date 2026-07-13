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
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/servicegateway"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const wakeNodeListPageSize uint32 = 200

// AuthConfig describes HMAC auth for /api/service/cloudnode/* calls.
type AuthConfig struct {
	Version   string
	AccessKey string
	SecretKey string
	ExpireSec int64
}

// Config configures the CloudNode gateway client.
type Config struct {
	// ServiceGatewayTarget is the public /api/service gateway used by collector and SCF callbacks.
	ServiceGatewayTarget  string
	GatewayURL            string // Deprecated: use ServiceGatewayTarget.
	StorageMetadataTarget string
	StorageAccessTarget   string
	Auth                  AuthConfig
}

// Client submits collector work to CloudNode through the admin service gateway.
type Client struct {
	serviceGatewayTarget  string
	storageMetadataTarget string
	storageAccessTarget   string
	auth                  AuthConfig
	httpClient            *http.Client
	httpClientErr         error
}

// WakeOptions describes which Collector runtime nodes should be nudged to poll queued work.
type WakeOptions struct {
	SpaceID  string
	JobTypes []string
}

// New creates a CloudNode client.
func New(cfg Config) *Client {
	target := strings.TrimSpace(cfg.ServiceGatewayTarget)
	if target == "" {
		target = strings.TrimSpace(cfg.GatewayURL)
	}
	httpClient, httpClientErr := servicegateway.NewClient(8 * time.Second)
	return &Client{
		serviceGatewayTarget:  normalizeGatewayTarget(target),
		storageMetadataTarget: strings.TrimSpace(cfg.StorageMetadataTarget),
		storageAccessTarget:   strings.TrimSpace(cfg.StorageAccessTarget),
		auth:                  cfg.Auth,
		httpClient:            httpClient,
		httpClientErr:         httpClientErr,
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
	raw, err := protojson.Marshal(&pb.SubmitJobItemsReq{Items: jobItems})
	if err != nil {
		return nil, fmt.Errorf("marshal submit job items request: %w", err)
	}
	var rsp pb.SubmitJobItemsRsp
	if err := c.postService(ctx, "cloudnode", "SubmitJobItems", raw, &rsp); err != nil {
		return nil, fmt.Errorf("submit cloud job items: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("submit cloud job items: %s", rsp.GetRetInfo().GetMsg())
	}
	return jobItemIDsByTaskID(jobItems, rsp.GetAcks()), nil
}

// WakeCollectorNodes invokes matching Collector SCF nodes so they poll queued JobItems.
func (c *Client) WakeCollectorNodes(ctx context.Context, opts WakeOptions) (int, error) {
	spaceID := strings.TrimSpace(opts.SpaceID)
	if spaceID == "" {
		return 0, fmt.Errorf("space_id is required")
	}
	nodes, err := c.listCloudNodes(ctx, spaceID)
	if err != nil {
		return 0, err
	}
	wakeEvent, err := c.buildWakeEvent()
	if err != nil {
		return 0, err
	}
	woken := 0
	var wakeErrors []error
	for _, node := range nodes {
		if !supportsAnyJobType(node.GetSupportedWorkloads(), opts.JobTypes) {
			continue
		}
		wakeStruct, err := structpb.NewStruct(wakeEventWithNodeID(wakeEvent, node.GetNodeId()))
		if err != nil {
			wakeErrors = append(wakeErrors, fmt.Errorf("build wake event for %s: %w", node.GetNodeId(), err))
			continue
		}
		req := &pb.InvokeFunctionReq{
			NodeId:        node.GetNodeId(),
			EventData:     wakeStruct,
			ScfInvokeType: pb.ScfInvokeType_SCF_INVOKE_TYPE_EVENT,
		}
		raw, err := protojson.Marshal(req)
		if err != nil {
			wakeErrors = append(wakeErrors, fmt.Errorf("marshal invoke function request for %s: %w", node.GetNodeId(), err))
			continue
		}
		var rsp pb.InvokeFunctionRsp
		if err := c.postServiceWithHeaders(ctx, "cloudnode", "InvokeFunction", raw, &rsp, spaceHeaders(spaceID)); err != nil {
			wakeErrors = append(wakeErrors, fmt.Errorf("invoke collector node %s: %w", node.GetNodeId(), err))
			continue
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			wakeErrors = append(wakeErrors, fmt.Errorf("invoke collector node %s: %s", node.GetNodeId(), rsp.GetRetInfo().GetMsg()))
			continue
		}
		if rsp.GetScf().GetCode() != 0 {
			wakeErrors = append(wakeErrors, fmt.Errorf("invoke collector node %s: %s", node.GetNodeId(), rsp.GetScf().GetMessage()))
			continue
		}
		woken++
	}
	if woken == 0 && len(wakeErrors) > 0 {
		return 0, errors.Join(wakeErrors...)
	}
	return woken, nil
}

func wakeEventWithNodeID(base map[string]any, nodeID string) map[string]any {
	out := make(map[string]any, len(base))
	for key, value := range base {
		out[key] = value
	}
	data := map[string]any{}
	if raw, ok := base["data"].(map[string]any); ok {
		for key, value := range raw {
			data[key] = value
		}
	}
	data["node_id"] = strings.TrimSpace(nodeID)
	out["data"] = data
	return out
}

func (c *Client) listCloudNodes(ctx context.Context, spaceID string) ([]*pb.CloudNode, error) {
	nodes := []*pb.CloudNode{}
	for page := uint32(1); ; page++ {
		body, err := protojson.Marshal(&pb.GetNodeListReq{
			Page: &commonpb.Page{Page: page, Size: wakeNodeListPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("marshal get node list request: %w", err)
		}
		var rsp pb.GetNodeListRsp
		if err := c.postServiceWithHeaders(ctx, "cloudnode", "GetNodeList", body, &rsp, spaceHeaders(spaceID)); err != nil {
			return nil, fmt.Errorf("list cloud nodes: %w", err)
		}
		if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list cloud nodes: %s", rsp.GetRetInfo().GetMsg())
		}
		items := rsp.GetItems()
		nodes = append(nodes, items...)
		if len(items) == 0 || !rsp.GetPage().GetHasMore() {
			break
		}
	}
	return nodes, nil
}

func (c *Client) postService(ctx context.Context, service string, method string, body []byte, out proto.Message) error {
	return c.postServiceWithHeaders(ctx, service, method, body, out, nil)
}

func (c *Client) postServiceWithHeaders(ctx context.Context, service string, method string, body []byte, out proto.Message, headers map[string]string) error {
	if c.httpClientErr != nil {
		return c.httpClientErr
	}
	if c.serviceGatewayTarget == "" {
		return fmt.Errorf("service gateway target is required")
	}
	url := fmt.Sprintf("%s/api/service/%s/%s", c.serviceGatewayTarget, service, method)
	req, err := runtimeapp.NewSignedRequestWithContext(ctx, http.MethodPost, url, body, runtimeapp.AuthConfig{
		Version:   c.auth.Version,
		AccessKey: c.auth.AccessKey,
		SecretKey: c.auth.SecretKey,
		ExpireSec: c.auth.ExpireSec,
	})
	if err != nil {
		return err
	}
	for key, value := range headers {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
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

func (c *Client) buildWakeEvent() (map[string]any, error) {
	if c.serviceGatewayTarget == "" {
		return nil, fmt.Errorf("service gateway target is required")
	}
	return map[string]any{
		"action":                  "keepalive",
		"source":                  "collector_schedule",
		"timestamp":               time.Now().UTC().Format(time.RFC3339),
		"service_gateway_target":  c.serviceGatewayTarget,
		"storage_metadata_target": c.storageMetadataTarget,
		"storage_access_target":   c.storageAccessTarget,
		"data": map[string]any{
			"wake_reason": "collector_job_items",
		},
	}, nil
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

func spaceHeaders(spaceID string) map[string]string {
	return map[string]string{"X-Space-Id": strings.TrimSpace(spaceID)}
}

func supportsAnyJobType(supported []string, required []string) bool {
	required = compactStrings(required)
	if len(required) == 0 {
		return true
	}
	allowed := make(map[string]struct{}, len(supported))
	for _, value := range supported {
		if value = normalizeJobType(value); value != "" {
			allowed[value] = struct{}{}
		}
	}
	for _, value := range required {
		if _, ok := allowed[normalizeJobType(value)]; ok {
			return true
		}
	}
	return false
}

func normalizeJobType(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "collect.") {
		return strings.TrimPrefix(value, "collect.")
	}
	return value
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func buildJobItem(instance domain.TaskInstance) *pb.JobItem {
	payload := parsePayload(instance.TaskParams)
	payload["space_id"] = instance.SpaceID
	payload["task_id"] = instance.TaskID
	window := scheduleWindow(time.Now().UTC(), valueString(payload, "schedule_interval", "30m"))
	payload["schedule_window"] = window
	payloadStruct, _ := structpb.NewStruct(payload)
	return &pb.JobItem{
		SpaceId:       instance.SpaceID,
		JobId:         instance.RuleID,
		JobItemId:     windowedJobItemID(instance.TaskID, window),
		JobType:       jobType(payload),
		CodePackageId: valueString(payload, "code_package_id", defaultCodePackageID(payload)),
		Params:        payloadStruct,
		Priority:      100,
	}
}

func jobItemIDsByTaskID(items []*pb.JobItem, acks []*pb.JobItemAck) map[string]string {
	taskByItemID := make(map[string]string, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		taskByItemID[strings.TrimSpace(item.GetJobItemId())] = taskIDFromJobItem(item)
	}
	out := make(map[string]string, len(acks))
	for _, ack := range acks {
		if ack == nil {
			continue
		}
		switch ack.GetStatus() {
		case pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
			pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED:
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
	return out
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

func windowedJobItemID(taskID string, window string) string {
	taskID = strings.TrimSpace(taskID)
	window = strings.TrimSpace(window)
	if taskID == "" || window == "" {
		return taskID
	}
	return taskID + ":" + window
}

func jobType(payload map[string]any) string {
	if value := valueString(payload, "job_type", ""); value != "" {
		return value
	}
	dataType := valueString(payload, "data_type", "kline")
	return "collect." + dataType
}

func defaultCodePackageID(_ map[string]any) string {
	return domain.DefaultCollectorCodePackageID
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

func scheduleWindow(now time.Time, interval string) string {
	duration, ok := parseScheduleDuration(interval)
	if !ok || duration <= 0 {
		duration = 30 * time.Minute
	}
	return now.Truncate(duration).Format(time.RFC3339)
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
