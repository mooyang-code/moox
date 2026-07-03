// Package taskpublisher contains Collector clients for CloudNode.
package taskpublisher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// AuthConfig describes HMAC auth for /api/service/cloudnode/* calls.
type AuthConfig struct {
	Version   string
	AccessKey string
	SecretKey string
	ExpireSec int64
}

// Config configures the CloudNode gateway client.
type Config struct {
	GatewayURL string
	Auth       AuthConfig
}

// Client submits collector work to CloudNode through the admin service gateway.
type Client struct {
	gatewayURL string
	auth       AuthConfig
	httpClient *http.Client
}

// New creates a CloudNode client.
func New(cfg Config) *Client {
	return &Client{
		gatewayURL: strings.TrimRight(strings.TrimSpace(cfg.GatewayURL), "/"),
		auth:       cfg.Auth,
		httpClient: &http.Client{Timeout: 8 * time.Second},
	}
}

// SubmitCollectorWorkItems submits task instances as async CloudNode work items.
func (c *Client) SubmitCollectorWorkItems(ctx context.Context, instances []domain.TaskInstance) error {
	workItems := make([]*pb.CloudWorkItem, 0, len(instances))
	for _, instance := range instances {
		workItems = append(workItems, buildCloudWorkItem(instance))
	}
	if len(workItems) == 0 {
		return nil
	}
	raw, err := protojson.Marshal(&pb.SubmitWorkItemsReq{WorkItems: workItems})
	if err != nil {
		return fmt.Errorf("marshal submit work items request: %w", err)
	}
	var rsp pb.SubmitWorkItemsRsp
	if err := c.postService(ctx, "cloudnode", "SubmitWorkItems", raw, &rsp); err != nil {
		return fmt.Errorf("submit cloud work items: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		return fmt.Errorf("submit cloud work items: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}

func (c *Client) postService(ctx context.Context, service string, method string, body []byte, out proto.Message) error {
	if c.gatewayURL == "" {
		return fmt.Errorf("admin gateway url is required")
	}
	url := fmt.Sprintf("%s/api/service/%s/%s", c.gatewayURL, service, method)
	req, err := runtimeapp.NewSignedRequestWithContext(ctx, http.MethodPost, url, body, runtimeapp.AuthConfig{
		Version:   c.auth.Version,
		AccessKey: c.auth.AccessKey,
		SecretKey: c.auth.SecretKey,
		ExpireSec: c.auth.ExpireSec,
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

func buildCloudWorkItem(instance domain.TaskInstance) *pb.CloudWorkItem {
	payload := parsePayload(instance.TaskParams)
	payload["space_id"] = instance.SpaceID
	payload["task_id"] = instance.TaskID
	payload["schedule_window"] = scheduleWindow(time.Now().UTC(), valueString(payload, "schedule_interval", "30m"))
	payloadStruct, _ := structpb.NewStruct(payload)
	return &pb.CloudWorkItem{
		SpaceId:        instance.SpaceID,
		OwnerService:   "collector",
		OwnerRef:       instance.TaskID,
		WorkloadType:   valueString(payload, "workload_type", "collector.binance.spot.kline"),
		DeploymentId:   valueString(payload, "deployment_id", "collector-binance-kline-v1"),
		Payload:        payloadStruct,
		Priority:       100,
		LeaseTimeoutMs: int64((10 * time.Minute) / time.Millisecond),
	}
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
