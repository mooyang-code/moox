package scfinvoker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

type Config struct {
	ServiceGatewayTarget string
	Auth                 runtimeapp.AuthConfig
	Timeout              time.Duration
}

const listMarketFetchersPageSize = 500

type Client struct {
	target    string
	auth      runtimeapp.AuthConfig
	http      *http.Client
	httpError error
}

type Node struct {
	NodeID       string
	FunctionName string
	Region       string
	Namespace    string
	PackageID    string
	DeploymentID string
	BizType      string
	NodeType     string
	TriggerType  string
	Workloads    []string
	Metadata     map[string]any
}

type InvocationResult struct {
	RequestID    string
	Code         int32
	Message      string
	Result       map[string]any
	DurationMS   int64
	BillDuration int64
}

func New(cfg Config) *Client {
	target := strings.TrimRight(strings.TrimSpace(cfg.ServiceGatewayTarget), "/")
	httpClient, err := runtimeapp.NewGatewayHTTPClient(cfg.Timeout, cfg.Auth)
	return &Client{target: target, auth: cfg.Auth, http: httpClient, httpError: err}
}

func (c *Client) ListMarketFetchers(ctx context.Context, spaceID string) ([]Node, error) {
	// Invoke is reserved for bounded catch-up and manual
	// probes. Realtime K-line nodes are Timer-triggered and must not be selected
	// by the legacy request scheduler.
	return c.listMarketFetchers(ctx, spaceID, "invoke")
}

func (c *Client) ListTimerMarketFetchers(ctx context.Context, spaceID string) ([]Node, error) {
	// The daily stock Instrument snapshot also uses a Timer trigger, but it is
	// not a Kline assignment target and must never consume one of the fixed N
	// Kline groups.
	nodes, err := c.listMarketFetchers(ctx, spaceID, "timer")
	if err != nil {
		return nil, err
	}
	filtered := nodes[:0]
	for _, node := range nodes {
		if IsInstrumentSnapshotNode(node) {
			continue
		}
		filtered = append(filtered, node)
	}
	return filtered, nil
}

func (c *Client) listMarketFetchers(ctx context.Context, spaceID, triggerType string) ([]Node, error) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	var all []Node
	for page := uint32(1); ; page++ {
		raw, err := protojson.Marshal(&cloudnodepb.GetNodeListReq{BizType: "market_fetcher", TriggerType: triggerType, Page: &commonpb.Page{Page: page, Size: listMarketFetchersPageSize}})
		if err != nil {
			return nil, err
		}
		var rsp cloudnodepb.GetNodeListRsp
		if err := c.post(ctx, spaceID, "GetNodeList", raw, &rsp); err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list market fetchers: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, item := range rsp.GetItems() {
			if item == nil || item.GetIsDeleted() || !isDeployed(item) {
				continue
			}
			all = append(all, nodeFromProto(item))
		}
		if rsp.GetPage() == nil || !rsp.GetPage().GetHasMore() {
			return all, nil
		}
	}
}

func isDeployed(item *cloudnodepb.CloudNode) bool {
	if item == nil || strings.TrimSpace(item.GetPackageId()) == "" {
		return false
	}
	if strings.TrimSpace(item.GetDeploymentId()) != "" {
		return true
	}
	// The CloudNode deploy RPC persists a local readiness marker because
	// Tencent SCF does not return a deployment id for an in-place code update.
	// Package id alone is intentionally insufficient: it is already present
	// during the create-before-deploy window.
	metadata := map[string]any{}
	if item.GetMetadata() != nil {
		metadata = item.GetMetadata().AsMap()
	}
	ready, _ := metadata["deployment_ready"].(bool)
	return ready
}

func nodeFromProto(item *cloudnodepb.CloudNode) Node {
	metadata := map[string]any{}
	if item.GetMetadata() != nil {
		metadata = item.GetMetadata().AsMap()
	}
	return Node{NodeID: item.GetNodeId(), FunctionName: item.GetFunctionName(), Region: item.GetRegion(), Namespace: item.GetNamespace(), PackageID: item.GetPackageId(), DeploymentID: item.GetDeploymentId(), BizType: item.GetBizType(), NodeType: item.GetNodeType(), TriggerType: item.GetTriggerType(), Metadata: metadata}
}

func IsInstrumentSnapshotNode(node Node) bool {
	if strings.EqualFold(strings.TrimSpace(fmt.Sprint(node.Metadata["function_mode"])), "instrument_snapshot") {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(node.FunctionName))
	return strings.Contains(name, "-instrument-") || strings.HasSuffix(name, "-instrument")
}

// SubmitRuntimeConfigs persists one asynchronous CloudNode reconciliation job.
func (c *Client) SubmitRuntimeConfigs(ctx context.Context, spaceID string, patches []*cloudnodepb.NodeRuntimeConfigPatch) (string, error) {
	if len(patches) == 0 {
		return "", fmt.Errorf("runtime config patches are required")
	}
	raw, err := protojson.Marshal(&cloudnodepb.BatchUpdateNodeRuntimeConfigsReq{Nodes: patches})
	if err != nil {
		return "", err
	}
	var rsp cloudnodepb.SubmitNodeBatchRsp
	if err := c.post(ctx, spaceID, "SubmitUpdateNodeRuntimeConfigs", raw, &rsp); err != nil {
		return "", err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("submit runtime configs: %s", rsp.GetRetInfo().GetMsg())
	}
	return rsp.GetJobId(), nil
}

// GetRuntimeConfigBatchStatus is used by Collector to distinguish an
// accepted asynchronous job from a configuration that actually reached every
// Tencent function. Runtime reconciliation must not report success before
// CloudNode's durable worker finishes.
func (c *Client) GetRuntimeConfigBatchStatus(ctx context.Context, spaceID, jobID string) (*cloudnodepb.NodeBatchSummary, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	raw, err := protojson.Marshal(&cloudnodepb.GetNodeBatchChangeReq{JobId: jobID})
	if err != nil {
		return nil, err
	}
	var rsp cloudnodepb.GetNodeBatchChangeRsp
	if err := c.post(ctx, spaceID, "GetNodeBatchChange", raw, &rsp); err != nil {
		return nil, err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("get runtime config batch: %s", rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetJob() == nil {
		return nil, fmt.Errorf("get runtime config batch: empty job")
	}
	return rsp.GetJob(), nil
}

func (c *Client) Invoke(ctx context.Context, spaceID, nodeID string, event map[string]any, invokeType cloudnodepb.ScfInvokeType) (InvocationResult, error) {
	if strings.TrimSpace(nodeID) == "" {
		return InvocationResult{}, fmt.Errorf("node_id is required")
	}
	value, err := structpb.NewStruct(event)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("build invoke event: %w", err)
	}
	raw, err := protojson.Marshal(&cloudnodepb.InvokeFunctionReq{NodeId: nodeID, EventData: value, ScfInvokeType: invokeType})
	if err != nil {
		return InvocationResult{}, err
	}
	var rsp cloudnodepb.InvokeFunctionRsp
	if err := c.post(ctx, spaceID, "InvokeFunction", raw, &rsp); err != nil {
		return InvocationResult{}, err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return InvocationResult{}, fmt.Errorf("invoke market fetcher: %s", rsp.GetRetInfo().GetMsg())
	}
	result := rsp.GetScf()
	if result == nil {
		return InvocationResult{}, fmt.Errorf("invoke market fetcher: empty SCF result")
	}
	if result.GetCode() != 0 {
		return InvocationResult{RequestID: result.GetRequestId(), Code: result.GetCode(), Message: result.GetMessage()}, fmt.Errorf("SCF invocation failed: %s", result.GetMessage())
	}
	resultMap := map[string]any{}
	if result.GetResult() != nil {
		resultMap = result.GetResult().AsMap()
	}
	return InvocationResult{RequestID: result.GetRequestId(), Code: result.GetCode(), Message: result.GetMessage(), Result: resultMap, DurationMS: result.GetDuration(), BillDuration: result.GetBillDuration()}, nil
}

func (c *Client) post(ctx context.Context, spaceID, method string, body []byte, out proto.Message) error {
	if c == nil || c.httpError != nil {
		if c == nil {
			return fmt.Errorf("SCF invoker is nil")
		}
		return c.httpError
	}
	if c.target == "" {
		return fmt.Errorf("service gateway target is required")
	}
	path := "/api/service/cloudnode/" + method
	req, err := runtimeapp.NewSignedRequestWithContextAndHeaders(ctx, http.MethodPost, c.target+path, body, map[string]string{"X-Space-Id": spaceID}, c.auth)
	if err != nil {
		return err
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("cloudnode %s status=%d body=%s", method, response.StatusCode, string(responseBody))
	}
	if len(bytes.TrimSpace(responseBody)) == 0 || out == nil {
		return nil
	}
	if err := protojson.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode cloudnode %s response: %w", method, err)
	}
	return nil
}

func (c *Client) LogNode(node Node) {
	log.Debugf("market fetcher node=%s region=%s function=%s", node.NodeID, node.Region, node.FunctionName)
}
