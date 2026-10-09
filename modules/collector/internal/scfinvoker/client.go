package scfinvoker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

type Config struct {
	Gateway              gatewayclient.Invoker
	ServiceGatewayTarget string
	Auth                 runtimeapp.AuthConfig
	Timeout              time.Duration
}

const listMarketFetchersPageSize = 500

var (
	ErrRuntimeConfigSubmissionUnknown = errors.New("runtime config submission outcome is unknown")
	ErrCollectorPublishLeaseStale     = errors.New("collector publish lease is expired or fenced")
)

type Client struct {
	gateway   gatewayclient.Invoker
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

type CollectorPublishLease struct {
	SpaceID      string
	LeaseID      string
	HolderID     string
	FencingToken int64
	ExpiresAt    time.Time
}

func New(cfg Config) *Client {
	target := strings.TrimRight(strings.TrimSpace(cfg.ServiceGatewayTarget), "/")
	httpClient, err := runtimeapp.NewGatewayHTTPClient(cfg.Timeout, cfg.Auth)
	return &Client{gateway: cfg.Gateway, target: target, auth: cfg.Auth, http: httpClient, httpError: err}
}

func (c *Client) ListMarketFetchers(ctx context.Context, spaceID string) ([]Node, error) {
	// Invoke nodes are the Scheduler-owned execution pool. Crypto realtime K-line
	// collection uses this path so every asynchronous execution can publish its
	// durable completion event. Timer nodes remain available to Timer-owned spaces.
	return c.listMarketFetchers(ctx, spaceID, "invoke")
}

func (c *Client) ListTimerMarketFetchers(ctx context.Context, spaceID string) ([]Node, error) {
	return c.listMarketFetchers(ctx, spaceID, "timer")
}

func (c *Client) listMarketFetchers(ctx context.Context, spaceID, triggerType string) ([]Node, error) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	var all []Node
	for page := uint32(1); ; page++ {
		request := &cloudnodepb.GetNodeListReq{BizType: "market_fetcher", TriggerType: triggerType, Page: &commonpb.Page{Page: page, Size: listMarketFetchersPageSize}}
		var rsp cloudnodepb.GetNodeListRsp
		if err := c.callCloudNode(ctx, spaceID, "GetNodeList", request, &rsp, false); err != nil {
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

// SubmitRuntimeConfigs persists one asynchronous CloudNode reconciliation job.
func (c *Client) SubmitRuntimeConfigs(ctx context.Context, spaceID string, patches []*cloudnodepb.NodeRuntimeConfigPatch) (string, error) {
	if len(patches) == 0 {
		return "", fmt.Errorf("runtime config patches are required")
	}
	request := &cloudnodepb.BatchUpdateNodeRuntimeConfigsReq{Nodes: patches}
	var rsp cloudnodepb.SubmitNodeBatchRsp
	if err := c.callCloudNode(ctx, spaceID, "SubmitUpdateNodeRuntimeConfigs", request, &rsp, true); err != nil {
		return "", err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("submit runtime configs: %s", rsp.GetRetInfo().GetMsg())
	}
	jobID := strings.TrimSpace(rsp.GetJobId())
	if jobID == "" {
		return "", fmt.Errorf("%w: SubmitUpdateNodeRuntimeConfigs returned no job_id", ErrRuntimeConfigSubmissionUnknown)
	}
	return jobID, nil
}

// GetRuntimeConfigBatchStatus is used by Collector to distinguish an
// accepted asynchronous job from a configuration that actually reached every
// Tencent function. Runtime reconciliation must not report success before
// CloudNode's durable worker finishes.
func (c *Client) GetRuntimeConfigBatchStatus(ctx context.Context, spaceID, jobID string) (*cloudnodepb.NodeBatchSummary, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	request := &cloudnodepb.GetNodeBatchChangeReq{JobId: jobID}
	var rsp cloudnodepb.GetNodeBatchChangeRsp
	if err := c.callCloudNode(ctx, spaceID, "GetNodeBatchChange", request, &rsp, false); err != nil {
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

func (c *Client) AcquireCollectorPublishLease(ctx context.Context, spaceID, holderID string) (*CollectorPublishLease, error) {
	spaceID, holderID = strings.TrimSpace(spaceID), strings.TrimSpace(holderID)
	if spaceID == "" || holderID == "" {
		return nil, fmt.Errorf("space_id and holder_id are required")
	}
	var response struct {
		RetInfo struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"ret_info"`
		SpaceID      string          `json:"space_id"`
		LeaseID      string          `json:"lease_id"`
		FencingToken json.RawMessage `json:"fencing_token"`
		ExpiresAt    string          `json:"expires_at"`
	}
	body, err := json.Marshal(map[string]string{"space_id": spaceID, "holder_id": holderID})
	if err != nil {
		return nil, err
	}
	if err := c.postService(ctx, spaceID, "AcquireCollectorPublishLease", body, &response); err != nil {
		return nil, err
	}
	if response.RetInfo.Code != 0 {
		return nil, fmt.Errorf("acquire collector publish lease: %s", response.RetInfo.Msg)
	}
	token, err := parsePublishFencingToken(response.FencingToken)
	if err != nil {
		return nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("decode collector publish lease expiry: %w", err)
	}
	if response.LeaseID == "" || token < 1 {
		return nil, fmt.Errorf("collector publish lease response is incomplete")
	}
	return &CollectorPublishLease{SpaceID: response.SpaceID, LeaseID: response.LeaseID, HolderID: holderID, FencingToken: token, ExpiresAt: expiresAt}, nil
}

func (c *Client) RenewCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) (*CollectorPublishLease, error) {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return nil, fmt.Errorf("collector publish lease identity is incomplete")
	}
	return c.updateCollectorPublishLease(ctx, lease, "RenewCollectorPublishLease")
}

func (c *Client) ReleaseCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	var response struct {
		RetInfo struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"ret_info"`
		Released bool `json:"released"`
	}
	body, err := json.Marshal(map[string]string{"space_id": lease.SpaceID, "lease_id": lease.LeaseID, "fencing_token": strconv.FormatInt(lease.FencingToken, 10)})
	if err != nil {
		return err
	}
	if err := c.postService(ctx, lease.SpaceID, "ReleaseCollectorPublishLease", body, &response); err != nil {
		return err
	}
	if response.RetInfo.Code != 0 || !response.Released {
		return fmt.Errorf("release collector publish lease rejected: %s", response.RetInfo.Msg)
	}
	return nil
}

func (c *Client) updateCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease, method string) (*CollectorPublishLease, error) {
	var response struct {
		RetInfo struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"ret_info"`
		SpaceID      string          `json:"space_id"`
		LeaseID      string          `json:"lease_id"`
		FencingToken json.RawMessage `json:"fencing_token"`
		ExpiresAt    string          `json:"expires_at"`
	}
	body, err := json.Marshal(map[string]string{"space_id": lease.SpaceID, "lease_id": lease.LeaseID, "fencing_token": strconv.FormatInt(lease.FencingToken, 10)})
	if err != nil {
		return nil, err
	}
	if err := c.postService(ctx, lease.SpaceID, method, body, &response); err != nil {
		return nil, err
	}
	if response.RetInfo.Code != 0 {
		if method == "RenewCollectorPublishLease" && response.RetInfo.Code == int(commonpb.ErrorCode_CONFLICT) {
			return nil, fmt.Errorf("%w: %s", ErrCollectorPublishLeaseStale, response.RetInfo.Msg)
		}
		return nil, fmt.Errorf("%s: %s", method, response.RetInfo.Msg)
	}
	token, err := parsePublishFencingToken(response.FencingToken)
	if err != nil {
		return nil, err
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("decode collector publish lease expiry: %w", err)
	}
	if response.LeaseID != lease.LeaseID || token != lease.FencingToken {
		return nil, fmt.Errorf("%s returned a different lease identity", method)
	}
	return &CollectorPublishLease{SpaceID: lease.SpaceID, LeaseID: lease.LeaseID, HolderID: lease.HolderID, FencingToken: token, ExpiresAt: expiresAt}, nil
}

func parsePublishFencingToken(raw json.RawMessage) (int64, error) {
	value := strings.TrimSpace(string(raw))
	if len(value) > 1 && value[0] == '"' {
		var decoded string
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return 0, err
		}
		value = decoded
	}
	token, err := strconv.ParseInt(value, 10, 64)
	if err != nil || token < 1 {
		return 0, fmt.Errorf("collector publish lease fencing_token is invalid")
	}
	return token, nil
}

func (c *Client) Invoke(ctx context.Context, spaceID, nodeID string, event map[string]any, invokeType cloudnodepb.ScfInvokeType) (InvocationResult, error) {
	if strings.TrimSpace(nodeID) == "" {
		return InvocationResult{}, fmt.Errorf("node_id is required")
	}
	value, err := structpb.NewStruct(event)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("build invoke event: %w", err)
	}
	request := &cloudnodepb.InvokeFunctionReq{NodeId: nodeID, EventData: value, ScfInvokeType: invokeType}
	var rsp cloudnodepb.InvokeFunctionRsp
	if err := c.callCloudNode(ctx, spaceID, "InvokeFunction", request, &rsp, false); err != nil {
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

func (c *Client) callCloudNode(ctx context.Context, spaceID, method string, request, response proto.Message, unknownAfterSend bool) error {
	if c == nil || c.gateway == nil {
		return errors.New("CloudNode requires the process gateway client")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	err := c.gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), "trpc.moox.cloudnode.CloudNodeMgr", method, request, response)
	if err != nil && unknownAfterSend {
		return fmt.Errorf("%w: CloudNode %s: %w", ErrRuntimeConfigSubmissionUnknown, method, err)
	}
	return err
}

func (c *Client) postService(ctx context.Context, spaceID, method string, body []byte, out any) error {
	return c.postPath(ctx, spaceID, "/api/service/publishlease/"+method, "publishlease "+method, body, out)
}

func (c *Client) postPath(ctx context.Context, spaceID, path, label string, body []byte, out any) error {
	if c == nil {
		return errors.New("SCF invoker is nil")
	}
	if c.httpError != nil {
		return c.httpError
	}
	if c.target == "" {
		return errors.New("service gateway target is required")
	}
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
		return fmt.Errorf("%s status=%d body=%s", label, response.StatusCode, string(responseBody))
	}
	if len(bytes.TrimSpace(responseBody)) == 0 || out == nil {
		return nil
	}
	if err := json.Unmarshal(responseBody, out); err != nil {
		return fmt.Errorf("decode %s response: %w", label, err)
	}
	return nil
}

func (c *Client) LogNode(node Node) {
	log.Debugf("market fetcher node=%s region=%s function=%s", node.NodeID, node.Region, node.FunctionName)
}
