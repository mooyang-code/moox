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
	// ErrCollectorPublishLeaseHeld 表示 Admin 明确答复该空间的发布租约或在途操作占用仍被他人持有（CONFLICT）。
	// 它是租约在正常工作，持续时间取决于旧作业还要跑多久；通信失败、无权限、响应异常不属于它。
	ErrCollectorPublishLeaseHeld = errors.New("collector publish lease is held")
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
	// Ready 表示函数已创建完成并部署；未就绪的节点是创建预留（创建中或创建失败后的遗留）。
	Ready bool
	// ModifiedAt 是目录记录的最近修改时间，用于判断预留是否已超时。
	ModifiedAt time.Time
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

// ListTimerMarketFetchersFresh 同步读回云端函数与定时器的实际状态后再返回，用于周期性云端校验。
// 返回的节点元数据里 timer_last_readback_at 与 timer_status_error 说明这次读回是否成功。
func (c *Client) ListTimerMarketFetchersFresh(ctx context.Context, spaceID string) ([]Node, error) {
	return c.listMarketFetcherNodesWith(ctx, spaceID, "timer", true, true)
}

func (c *Client) listMarketFetchers(ctx context.Context, spaceID, triggerType string) ([]Node, error) {
	return c.listMarketFetcherNodes(ctx, spaceID, triggerType, true)
}

// LatestPackage 返回指定名称的最新可用代码包 ID（名称精确匹配，按创建先后取最新）。
// 采集计划函数按包名取代码：上传新版本后，对账器自动把落后的函数升级到它，不需要改配置或重启。
func (c *Client) LatestPackage(ctx context.Context, spaceID, name string) (string, error) {
	if strings.TrimSpace(spaceID) == "" || strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("space_id and package name are required")
	}
	status := cloudnodepb.PackageStatus_PACKAGE_STATUS_AVAILABLE
	for page := uint32(1); ; page++ {
		request := &cloudnodepb.GetPackageListReq{PackageName: name, BizType: "market_fetcher", Status: &status, Page: &commonpb.Page{Page: page, Size: listMarketFetchersPageSize}}
		var rsp cloudnodepb.GetPackageListRsp
		if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "GetPackageList", request, &rsp, false); err != nil {
			return "", err
		}
		if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
			return "", fmt.Errorf("list packages: %s", rsp.GetRetInfo().GetMsg())
		}
		// 服务端按创建先后倒序返回，名称是模糊匹配：取第一个名称完全相同的。
		for _, item := range rsp.GetItems() {
			if item != nil && item.GetPackageName() == name && item.GetStatus() == cloudnodepb.PackageStatus_PACKAGE_STATUS_AVAILABLE {
				return item.GetPackageId(), nil
			}
		}
		if rsp.GetPage() == nil || !rsp.GetPage().GetHasMore() {
			return "", fmt.Errorf("no available code package named %q; upload one with `moox-cli collector function upload`", name)
		}
	}
}

// ListUnreadyMarketFetchers 返回目录中存在但尚未部署完成的采集函数节点（创建预留）。
// 创建失败后预留会一直留在目录里，调用方据此识别并清理，才能重新创建同名函数。
func (c *Client) ListUnreadyMarketFetchers(ctx context.Context, spaceID, triggerType string) ([]Node, error) {
	return c.listMarketFetcherNodes(ctx, spaceID, triggerType, false)
}

func (c *Client) listMarketFetcherNodes(ctx context.Context, spaceID, triggerType string, ready bool) ([]Node, error) {
	return c.listMarketFetcherNodesWith(ctx, spaceID, triggerType, ready, false)
}

func (c *Client) listMarketFetcherNodesWith(ctx context.Context, spaceID, triggerType string, ready, refreshReadback bool) ([]Node, error) {
	if strings.TrimSpace(spaceID) == "" {
		return nil, fmt.Errorf("space_id is required")
	}
	var all []Node
	for page := uint32(1); ; page++ {
		request := &cloudnodepb.GetNodeListReq{BizType: "market_fetcher", TriggerType: triggerType, RefreshReadback: refreshReadback, Page: &commonpb.Page{Page: page, Size: listMarketFetchersPageSize}}
		var rsp cloudnodepb.GetNodeListRsp
		if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeList", request, &rsp, false); err != nil {
			return nil, err
		}
		if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list market fetchers: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, item := range rsp.GetItems() {
			if item == nil || item.GetIsDeleted() || isDeployed(item) != ready {
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
	modifiedAt, _ := time.Parse(time.RFC3339Nano, item.GetModifyTime())
	return Node{NodeID: item.GetNodeId(), FunctionName: item.GetFunctionName(), Region: item.GetRegion(), Namespace: item.GetNamespace(), PackageID: item.GetPackageId(), DeploymentID: item.GetDeploymentId(), BizType: item.GetBizType(), NodeType: item.GetNodeType(), TriggerType: item.GetTriggerType(), Metadata: metadata, Ready: isDeployed(item), ModifiedAt: modifiedAt}
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

// SubmitDeleteNodes 在发布租约保护下提交一批函数删除任务，返回异步作业 ID。
func (c *Client) SubmitDeleteNodes(ctx context.Context, spaceID string, nodeIDs []string, lease *CollectorPublishLease) (string, error) {
	if len(nodeIDs) == 0 {
		return "", fmt.Errorf("node ids are required")
	}
	if lease == nil {
		return "", fmt.Errorf("collector publish lease is required")
	}
	request := &cloudnodepb.BatchDeleteNodesReq{NodeIds: nodeIDs, CollectorPublishLeaseId: lease.LeaseID, CollectorPublishFencingToken: lease.FencingToken}
	var rsp cloudnodepb.SubmitNodeBatchRsp
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "SubmitDeleteNodes", request, &rsp, true); err != nil {
		return "", err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("submit delete nodes: %s", rsp.GetRetInfo().GetMsg())
	}
	jobID := strings.TrimSpace(rsp.GetJobId())
	if jobID == "" {
		return "", fmt.Errorf("%w: SubmitDeleteNodes returned no job_id", ErrRuntimeConfigSubmissionUnknown)
	}
	return jobID, nil
}

// SubmitDeployNodes 在发布租约保护下把代码包部署到一批已有函数（只更新代码，环境与配置保持不变），返回异步作业 ID。
func (c *Client) SubmitDeployNodes(ctx context.Context, spaceID string, nodeIDs []string, packageID string, lease *CollectorPublishLease) (string, error) {
	if len(nodeIDs) == 0 || strings.TrimSpace(packageID) == "" {
		return "", fmt.Errorf("node ids and package id are required")
	}
	if lease == nil {
		return "", fmt.Errorf("collector publish lease is required")
	}
	deployments := make([]*cloudnodepb.NodeDeployItem, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		deployments = append(deployments, &cloudnodepb.NodeDeployItem{NodeId: id, PackageId: packageID, CollectorPublishLeaseId: lease.LeaseID, CollectorPublishFencingToken: lease.FencingToken})
	}
	var rsp cloudnodepb.SubmitNodeBatchRsp
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "SubmitDeployNodes", &cloudnodepb.BatchDeployNodesReq{Deployments: deployments}, &rsp, true); err != nil {
		return "", err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("submit deploy nodes: %s", rsp.GetRetInfo().GetMsg())
	}
	jobID := strings.TrimSpace(rsp.GetJobId())
	if jobID == "" {
		return "", fmt.Errorf("%w: SubmitDeployNodes returned no job_id", ErrRuntimeConfigSubmissionUnknown)
	}
	return jobID, nil
}

// SubmitCreateNodes 提交一批函数创建任务，返回异步作业 ID。
func (c *Client) SubmitCreateNodes(ctx context.Context, spaceID string, items []*cloudnodepb.NodeCreateItem) (string, error) {
	if len(items) == 0 {
		return "", fmt.Errorf("create node items are required")
	}
	request := &cloudnodepb.BatchCreateNodesReq{Nodes: items}
	var rsp cloudnodepb.SubmitNodeBatchRsp
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "SubmitCreateNodes", request, &rsp, true); err != nil {
		return "", err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("submit create nodes: %s", rsp.GetRetInfo().GetMsg())
	}
	jobID := strings.TrimSpace(rsp.GetJobId())
	if jobID == "" {
		return "", fmt.Errorf("%w: SubmitCreateNodes returned no job_id", ErrRuntimeConfigSubmissionUnknown)
	}
	return jobID, nil
}

// BatchItemResult 是批量作业中一个节点的结果。
type BatchItemResult struct {
	NodeID  string
	Success bool
	Failed  bool
	Message string
}

// GetBatchResults 返回批量作业的汇总与逐节点结果。
func (c *Client) GetBatchResults(ctx context.Context, spaceID, jobID string) (*cloudnodepb.NodeBatchSummary, []BatchItemResult, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, nil, fmt.Errorf("job_id is required")
	}
	var rsp cloudnodepb.GetNodeBatchChangeRsp
	if err := c.invoke(ctx, spaceID, "trpc.moox.cloudnode.CloudNodeMgr", "GetNodeBatchChange", &cloudnodepb.GetNodeBatchChangeReq{JobId: jobID}, &rsp, false); err != nil {
		return nil, nil, err
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS || rsp.GetJob() == nil {
		return nil, nil, fmt.Errorf("get node batch: %s", rsp.GetRetInfo().GetMsg())
	}
	items := make([]BatchItemResult, 0, len(rsp.GetItems()))
	for _, item := range rsp.GetItems() {
		status := item.GetStatus()
		items = append(items, BatchItemResult{
			NodeID: item.GetNodeId(), Success: status == cloudnodepb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_SUCCESS,
			Failed: status == cloudnodepb.NodeBatchItemStatus_NODE_BATCH_ITEM_STATUS_FAILED, Message: firstNonBlank(item.GetErrorMessage(), item.GetResultSummary()),
		})
	}
	return rsp.GetJob(), items, nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
	// 申请租约（不带期望令牌）时 Admin 只会因租约或操作占用未过期返回 CONFLICT。
	if response.GetRetInfo().GetCode() == adminpb.ErrorCode_CONFLICT {
		return nil, fmt.Errorf("collector publish lease rejected: %w", ErrCollectorPublishLeaseHeld)
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
