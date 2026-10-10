package scfinvoker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

type Config struct {
	Gateway gatewayclient.Invoker
}

const listMarketFetchersPageSize = 500

var (
	ErrRuntimeConfigSubmissionUnknown = errors.New("runtime config submission outcome is unknown")
	ErrCollectorPublishLeaseStale     = errors.New("collector publish lease is expired or fenced")
)

// Client borrows the process gateway for CloudNode and Admin publish leases.
type Client struct {
	gateway gatewayclient.Invoker
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
	return &Client{gateway: cfg.Gateway}
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
		if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeList", request, &rsp, false); err != nil {
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
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "SubmitUpdateNodeRuntimeConfigs", request, &rsp, true); err != nil {
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
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeBatchChange", request, &rsp, false); err != nil {
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
	var response adminpb.CollectorPublishLeaseRsp
	if err := c.invoke(ctx, spaceID, "trpc.moox.admin.CollectorPublishLease", "AcquireCollectorPublishLease",
		&adminpb.AcquireCollectorPublishLeaseReq{SpaceId: spaceID, HolderId: holderID}, &response, false); err != nil {
		return nil, err
	}
	return collectorLeaseFromResponse(spaceID, holderID, &response)
}

func (c *Client) RenewCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) (*CollectorPublishLease, error) {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return nil, fmt.Errorf("collector publish lease identity is incomplete")
	}
	var response adminpb.CollectorPublishLeaseRsp
	if err := c.invoke(ctx, lease.SpaceID, "trpc.moox.admin.CollectorPublishLease", "RenewCollectorPublishLease",
		&adminpb.RenewCollectorPublishLeaseReq{SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken}, &response, false); err != nil {
		return nil, err
	}
	if response.GetRetInfo().GetCode() == adminpb.ErrorCode_CONFLICT {
		return nil, fmt.Errorf("%w: %s", ErrCollectorPublishLeaseStale, response.GetRetInfo().GetMsg())
	}
	renewed, err := collectorLeaseFromResponse(lease.SpaceID, lease.HolderID, &response)
	if err != nil {
		return nil, err
	}
	if renewed.LeaseID != lease.LeaseID || renewed.FencingToken != lease.FencingToken {
		return nil, fmt.Errorf("RenewCollectorPublishLease returned a different lease identity")
	}
	return renewed, nil
}

func (c *Client) ReleaseCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) error {
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	var response adminpb.ReleaseCollectorPublishLeaseRsp
	if err := c.invoke(ctx, lease.SpaceID, "trpc.moox.admin.CollectorPublishLease", "ReleaseCollectorPublishLease",
		&adminpb.ReleaseCollectorPublishLeaseReq{SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken}, &response, false); err != nil {
		return err
	}
	if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || !response.GetReleased() {
		return fmt.Errorf("release collector publish lease rejected: %s", response.GetRetInfo().GetMsg())
	}
	return nil
}

func collectorLeaseFromResponse(spaceID, holderID string, response *adminpb.CollectorPublishLeaseRsp) (*CollectorPublishLease, error) {
	if response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("collector publish lease rejected: %s", response.GetRetInfo().GetMsg())
	}
	if response.GetSpaceId() != spaceID || strings.TrimSpace(response.GetLeaseId()) == "" || response.GetFencingToken() < 1 {
		return nil, fmt.Errorf("collector publish lease response identity is incomplete or inconsistent")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, response.GetExpiresAt())
	if err != nil {
		return nil, fmt.Errorf("decode collector publish lease expiry: %w", err)
	}
	return &CollectorPublishLease{SpaceID: spaceID, LeaseID: response.GetLeaseId(), HolderID: holderID, FencingToken: response.GetFencingToken(), ExpiresAt: expiresAt}, nil
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
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "InvokeFunction", request, &rsp, false); err != nil {
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

func (c *Client) invoke(ctx context.Context, spaceID, service, method string, request, response proto.Message, unknownAfterSend bool) error {
	if c == nil || c.gateway == nil {
		return errors.New("SCF control requires the process gateway client")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := gatewayclient.CallMetadataFromContext(ctx)
	metadata.SpaceID = spaceID
	err := c.gateway.Invoke(gatewayclient.WithCallMetadata(ctx, metadata), service, method, request, response)
	if err != nil && unknownAfterSend {
		return fmt.Errorf("%w: CloudNode %s: %w", ErrRuntimeConfigSubmissionUnknown, method, err)
	}
	return err
}

func (c *Client) LogNode(node Node) {
	log.Debugf("market fetcher node=%s region=%s function=%s", node.NodeID, node.Region, node.FunctionName)
}
