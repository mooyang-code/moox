// Package scfinvoker 是 Collector 调用 CloudNode（SCF 节点管理、函数调用）和 Admin 发布租约的客户端，
// 经 gatewayclient 以 collector 身份发送 tRPC 请求。
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
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
)

const listMarketFetchersPageSize = 500

var (
	ErrRuntimeConfigSubmissionUnknown = errors.New("runtime config submission outcome is unknown")
	ErrCollectorPublishLeaseStale     = errors.New("collector publish lease is expired or fenced")
)

// cloudNodeAPI 是 Collector 用到的 CloudNodeMgr 方法。
type cloudNodeAPI interface {
	GetNodeList(context.Context, *cloudnodepb.GetNodeListReq, ...client.Option) (*cloudnodepb.GetNodeListRsp, error)
	SubmitUpdateNodeRuntimeConfigs(context.Context, *cloudnodepb.BatchUpdateNodeRuntimeConfigsReq, ...client.Option) (*cloudnodepb.SubmitNodeBatchRsp, error)
	GetNodeBatchChange(context.Context, *cloudnodepb.GetNodeBatchChangeReq, ...client.Option) (*cloudnodepb.GetNodeBatchChangeRsp, error)
	InvokeFunction(context.Context, *cloudnodepb.InvokeFunctionReq, ...client.Option) (*cloudnodepb.InvokeFunctionRsp, error)
}

// publishLeaseAPI 是 Collector 用到的 Admin 发布租约方法。
type publishLeaseAPI interface {
	AcquireCollectorPublishLease(context.Context, *adminpb.AcquireCollectorPublishLeaseReq, ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error)
	RenewCollectorPublishLease(context.Context, *adminpb.RenewCollectorPublishLeaseReq, ...client.Option) (*adminpb.CollectorPublishLeaseRsp, error)
	ReleaseCollectorPublishLease(context.Context, *adminpb.ReleaseCollectorPublishLeaseReq, ...client.Option) (*adminpb.ReleaseCollectorPublishLeaseRsp, error)
}

// Client 调用 CloudNodeMgr 与 CollectorPublishLease。
type Client struct {
	cloudnode cloudNodeAPI
	leases    publishLeaseAPI
}

// New 用给定的 tRPC 客户端选项（gatewayclient）创建客户端。
func New(options []client.Option) *Client {
	return &Client{cloudnode: cloudnodepb.NewCloudNodeMgrClientProxy(options...), leases: adminpb.NewCollectorPublishLeaseClientProxy(options...)}
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

// spaceOption 把 space 写入 tRPC 元数据，CloudNode 从中取得调用所属的 space。
func spaceOption(spaceID string) client.Option {
	return client.WithMetaData(gatewayroute.MetadataSpaceID, []byte(spaceID))
}

func (c *Client) ready() error {
	if c == nil || c.cloudnode == nil || c.leases == nil {
		return errors.New("SCF invoker is not configured")
	}
	return nil
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
	if err := c.ready(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(spaceID) == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	var all []Node
	for page := uint32(1); ; page++ {
		rsp, err := c.cloudnode.GetNodeList(ctx, &cloudnodepb.GetNodeListReq{
			BizType: "market_fetcher", TriggerType: triggerType, Page: &commonpb.Page{Page: page, Size: listMarketFetchersPageSize},
		}, spaceOption(spaceID))
		if err != nil {
			return nil, fmt.Errorf("list market fetchers: %w", err)
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
	if err := c.ready(); err != nil {
		return "", err
	}
	if len(patches) == 0 {
		return "", fmt.Errorf("runtime config patches are required")
	}
	rsp, err := c.cloudnode.SubmitUpdateNodeRuntimeConfigs(ctx, &cloudnodepb.BatchUpdateNodeRuntimeConfigsReq{Nodes: patches}, spaceOption(spaceID))
	if err != nil {
		if requestNotSent(err) {
			return "", fmt.Errorf("submit runtime configs: %w", err)
		}
		return "", fmt.Errorf("%w: SubmitUpdateNodeRuntimeConfigs: %v", ErrRuntimeConfigSubmissionUnknown, err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return "", fmt.Errorf("%w: SubmitUpdateNodeRuntimeConfigs returned an empty response", ErrRuntimeConfigSubmissionUnknown)
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

// requestNotSent 判断错误是否说明请求没有到达 CloudNode：连接失败、编码失败，或主机网关在转发前拒绝。
// 其他错误（超时、连接中断、服务端错误）都可能发生在 CloudNode 已经接受之后。
func requestNotSent(err error) bool {
	switch errs.Code(err) {
	case errs.RetClientConnectFail, errs.RetClientEncodeFail, errs.RetClientRouteErr, errs.RetClientValidateFail,
		gatewayroute.RetUnauthenticated, gatewayroute.RetForbidden, gatewayroute.RetServiceNotHere,
		gatewayroute.RetBodyTooLarge, gatewayroute.RetHostDisabled:
		return true
	default:
		return false
	}
}

// GetRuntimeConfigBatchStatus is used by Collector to distinguish an
// accepted asynchronous job from a configuration that actually reached every
// Tencent function. Runtime reconciliation must not report success before
// CloudNode's durable worker finishes.
func (c *Client) GetRuntimeConfigBatchStatus(ctx context.Context, spaceID, jobID string) (*cloudnodepb.NodeBatchSummary, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(jobID) == "" {
		return nil, fmt.Errorf("job_id is required")
	}
	rsp, err := c.cloudnode.GetNodeBatchChange(ctx, &cloudnodepb.GetNodeBatchChangeReq{JobId: jobID}, spaceOption(spaceID))
	if err != nil {
		return nil, fmt.Errorf("get runtime config batch: %w", err)
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
	if err := c.ready(); err != nil {
		return nil, err
	}
	spaceID, holderID = strings.TrimSpace(spaceID), strings.TrimSpace(holderID)
	if spaceID == "" || holderID == "" {
		return nil, fmt.Errorf("space_id and holder_id are required")
	}
	rsp, err := c.leases.AcquireCollectorPublishLease(ctx, &adminpb.AcquireCollectorPublishLeaseReq{SpaceId: spaceID, HolderId: holderID}, spaceOption(spaceID))
	if err != nil {
		return nil, fmt.Errorf("acquire collector publish lease: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return nil, fmt.Errorf("acquire collector publish lease: %s", rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetLeaseId() == "" || rsp.GetFencingToken() < 1 {
		return nil, fmt.Errorf("collector publish lease response is incomplete")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, rsp.GetExpiresAt())
	if err != nil {
		return nil, fmt.Errorf("decode collector publish lease expiry: %w", err)
	}
	return &CollectorPublishLease{SpaceID: rsp.GetSpaceId(), LeaseID: rsp.GetLeaseId(), HolderID: holderID, FencingToken: rsp.GetFencingToken(), ExpiresAt: expiresAt}, nil
}

func (c *Client) RenewCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) (*CollectorPublishLease, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return nil, fmt.Errorf("collector publish lease identity is incomplete")
	}
	rsp, err := c.leases.RenewCollectorPublishLease(ctx, &adminpb.RenewCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	}, spaceOption(lease.SpaceID))
	if err != nil {
		return nil, fmt.Errorf("RenewCollectorPublishLease: %w", err)
	}
	if code := rsp.GetRetInfo().GetCode(); code != commonpb.ErrorCode_SUCCESS {
		if code == commonpb.ErrorCode_CONFLICT {
			return nil, fmt.Errorf("%w: %s", ErrCollectorPublishLeaseStale, rsp.GetRetInfo().GetMsg())
		}
		return nil, fmt.Errorf("RenewCollectorPublishLease: %s", rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetLeaseId() != lease.LeaseID || rsp.GetFencingToken() != lease.FencingToken {
		return nil, fmt.Errorf("RenewCollectorPublishLease returned a different lease identity")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, rsp.GetExpiresAt())
	if err != nil {
		return nil, fmt.Errorf("decode collector publish lease expiry: %w", err)
	}
	return &CollectorPublishLease{SpaceID: lease.SpaceID, LeaseID: lease.LeaseID, HolderID: lease.HolderID, FencingToken: lease.FencingToken, ExpiresAt: expiresAt}, nil
}

func (c *Client) ReleaseCollectorPublishLease(ctx context.Context, lease *CollectorPublishLease) error {
	if err := c.ready(); err != nil {
		return err
	}
	if lease == nil || lease.SpaceID == "" || lease.LeaseID == "" || lease.FencingToken < 1 {
		return fmt.Errorf("collector publish lease identity is incomplete")
	}
	rsp, err := c.leases.ReleaseCollectorPublishLease(ctx, &adminpb.ReleaseCollectorPublishLeaseReq{
		SpaceId: lease.SpaceID, LeaseId: lease.LeaseID, FencingToken: lease.FencingToken,
	}, spaceOption(lease.SpaceID))
	if err != nil {
		return fmt.Errorf("release collector publish lease: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS || !rsp.GetReleased() {
		return fmt.Errorf("release collector publish lease rejected: %s", rsp.GetRetInfo().GetMsg())
	}
	return nil
}

func (c *Client) Invoke(ctx context.Context, spaceID, nodeID string, event map[string]any, invokeType cloudnodepb.ScfInvokeType) (InvocationResult, error) {
	if err := c.ready(); err != nil {
		return InvocationResult{}, err
	}
	if strings.TrimSpace(nodeID) == "" {
		return InvocationResult{}, fmt.Errorf("node_id is required")
	}
	value, err := structpb.NewStruct(event)
	if err != nil {
		return InvocationResult{}, fmt.Errorf("build invoke event: %w", err)
	}
	rsp, err := c.cloudnode.InvokeFunction(ctx, &cloudnodepb.InvokeFunctionReq{NodeId: nodeID, EventData: value, ScfInvokeType: invokeType}, spaceOption(spaceID))
	if err != nil {
		return InvocationResult{}, fmt.Errorf("invoke market fetcher: %w", err)
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

func (c *Client) LogNode(node Node) {
	log.Debugf("market fetcher node=%s region=%s function=%s", node.NodeID, node.Region, node.FunctionName)
}
