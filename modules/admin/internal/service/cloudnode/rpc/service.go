package rpc

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"
	cloudnodemgr "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现 pb.CloudNodeMgrService，承载 cloudnode 业务逻辑。
type Service struct {
	pb.UnimplementedCloudNodeMgr
	nodeSvc  cloudnodemgr.Service
	asyncSvc asynctask.Service
}

// NewService 创建 CloudNodeMgr RPC 实现。
func NewService(nodeSvc cloudnodemgr.Service, asyncSvc asynctask.Service) *Service {
	return &Service{
		nodeSvc:  nodeSvc,
		asyncSvc: asyncSvc,
	}
}

// ========== 节点 ==========

// GetNodeList 获取云节点列表。
func (s *Service) GetNodeList(ctx context.Context, req *pb.GetNodeListReq) (*pb.GetNodeListRsp, error) {
	rsp, err := s.nodeSvc.GetNodeList(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] GetNodeList failed: %v", err)
		return &pb.GetNodeListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询云节点列表失败")}, nil
	}
	rsp.RetInfo = retOK()
	// 补充代码包版本
	for _, node := range rsp.GetItems() {
		node.PackageVersion = "-"
		if node.GetPackageId() != "" {
			if pkg, perr := s.nodeSvc.GetPackageDetail(ctx, node.GetPackageId()); perr == nil && pkg != nil {
				node.PackageVersion = fmt.Sprintf("%s-%s", pkg.GetPackageName(), pkg.GetVersion())
			}
		}
	}
	return rsp, nil
}

// GetNodeDetail 获取节点详情。
func (s *Service) GetNodeDetail(ctx context.Context, req *pb.GetNodeDetailReq) (*pb.GetNodeDetailRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.GetNodeDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	node, err := s.nodeSvc.GetCloudNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetNodeDetailRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to get node")}, nil
	}
	if node == nil {
		return &pb.GetNodeDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "node not found")}, nil
	}
	return &pb.GetNodeDetailRsp{RetInfo: retOK(), Node: node}, nil
}

// GetSCFDeployInfo 获取 SCF 节点部署信息。
func (s *Service) GetSCFDeployInfo(ctx context.Context, req *pb.GetSCFDeployInfoReq) (*pb.GetSCFDeployInfoRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.GetSCFDeployInfoRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	node, err := s.nodeSvc.GetCloudNode(ctx, req.GetNodeId())
	if err != nil {
		return &pb.GetSCFDeployInfoRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to get node")}, nil
	}
	if node == nil {
		return &pb.GetSCFDeployInfoRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "node not found")}, nil
	}
	return &pb.GetSCFDeployInfoRsp{
		RetInfo: retOK(),
		Info: &pb.SCFDeployInfo{
			NodeId:         node.GetNodeId(),
			FunctionName:   node.GetNodeId(),
			Namespace:      node.GetNamespace(),
			Region:         node.GetRegion(),
			NodeType:       node.GetNodeType(),
			CloudAccountId: node.GetCloudAccountId(),
			ClsTopicId:     node.GetClsTopicId(),
		},
	}, nil
}

// UpdateNode 更新节点。
func (s *Service) UpdateNode(ctx context.Context, req *pb.UpdateNodeReq) (*pb.UpdateNodeRsp, error) {
	node := req.GetNode()
	if node == nil || node.GetNodeId() == "" {
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if err := s.nodeSvc.UpdateNode(ctx, node); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] UpdateNode failed: %v", err)
		return &pb.UpdateNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to update node")}, nil
	}
	return &pb.UpdateNodeRsp{RetInfo: retOK()}, nil
}

// DeleteNode 删除节点（调用云厂商API删除云函数）。
func (s *Service) DeleteNode(ctx context.Context, req *pb.DeleteNodeReq) (*pb.DeleteNodeRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.DeleteNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if err := s.nodeSvc.DeleteNode(ctx, req.GetNodeId()); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] DeleteNode failed: %v", err)
		return &pb.DeleteNodeRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to delete node")}, nil
	}
	return &pb.DeleteNodeRsp{RetInfo: retOK()}, nil
}

// InvokeFunction 调用云函数。
func (s *Service) InvokeFunction(ctx context.Context, req *pb.InvokeFunctionReq) (*pb.InvokeFunctionRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	eventData := structToInterface(req.GetEventData())
	if eventData == nil {
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "event_data is required")}, nil
	}
	rsp, err := s.nodeSvc.InvokeFunction(ctx, req.GetNodeId(), eventData)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] InvokeFunction failed: %v", err)
		return &pb.InvokeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "调用云函数失败")}, nil
	}
	rsp.RetInfo = retOK()
	return rsp, nil
}

// UpdateNodeFunction 更新节点代码包。
func (s *Service) UpdateNodeFunction(ctx context.Context, req *pb.UpdateNodeFunctionReq) (*pb.UpdateNodeFunctionRsp, error) {
	if req.GetNodeId() == "" {
		return &pb.UpdateNodeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id is required")}, nil
	}
	if err := s.nodeSvc.UpdateNodePackageID(ctx, req.GetNodeId(), req.GetPackageId()); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] UpdateNodeFunction failed: %v", err)
		return &pb.UpdateNodeFunctionRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to update node function")}, nil
	}
	return &pb.UpdateNodeFunctionRsp{RetInfo: retOK()}, nil
}

// ========== 批量操作 ==========

// BatchCreateNodes 批量创建节点（提交异步任务）。
func (s *Service) BatchCreateNodes(ctx context.Context, req *pb.BatchCreateNodesReq) (*pb.BatchOperationRsp, error) {
	nodes := req.GetNodes()
	if len(nodes) == 0 {
		return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "nodes cannot be empty")}, nil
	}
	tasks := make([]*pb.TaskRequestItem, 0, len(nodes))
	for _, n := range nodes {
		item := map[string]interface{}{
			"cloud_account_id": n.GetCloudAccountId(),
			"node_type":        n.GetNodeType(),
			"runtime":          n.GetRuntime(),
			"handler":          n.GetHandler(),
			"region":           n.GetRegion(),
			"namespace":        n.GetNamespace(),
			"package_id":       n.GetPackageId(),
		}
		if cfg := n.GetConfig(); len(cfg) > 0 {
			item["config"] = cfg
		}
		if env := n.GetEnvironment(); len(env) > 0 {
			item["environment"] = env
		}
		if md := n.GetMetadata(); md != nil {
			item["metadata"] = structToInterface(md)
		}
		paramsJSON, err := json.Marshal(item)
		if err != nil {
			return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid node parameters")}, nil
		}
		tasks = append(tasks, &pb.TaskRequestItem{
			TaskType:      asynctask.TaskTypeCreateNode,
			RequestParams: string(paramsJSON),
		})
	}
	jobID, err := s.asyncSvc.AsyncJobCreate(ctx, tasks)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] BatchCreateNodes failed: %v", err)
		return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to create batch job")}, nil
	}
	return &pb.BatchOperationRsp{
		RetInfo:      retOK(),
		JobId:        jobID,
		TotalTaskCnt: int32(len(tasks)),
		Message:      "批量创建任务已提交，请通过job_id查询任务状态",
	}, nil
}

// BatchDeleteNodes 批量删除节点（提交异步任务）。
func (s *Service) BatchDeleteNodes(ctx context.Context, req *pb.BatchDeleteNodesReq) (*pb.BatchOperationRsp, error) {
	nodeIDs := req.GetNodeIds()
	if len(nodeIDs) == 0 {
		return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_ids cannot be empty")}, nil
	}
	tasks := make([]*pb.TaskRequestItem, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		item := map[string]string{"node_id": nodeID}
		paramsJSON, err := json.Marshal(item)
		if err != nil {
			return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid node_id")}, nil
		}
		tasks = append(tasks, &pb.TaskRequestItem{
			TaskType:      asynctask.TaskTypeDeleteNode,
			RequestParams: string(paramsJSON),
		})
	}
	jobID, err := s.asyncSvc.AsyncJobCreate(ctx, tasks)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] BatchDeleteNodes failed: %v", err)
		return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to create batch job")}, nil
	}
	return &pb.BatchOperationRsp{
		RetInfo:      retOK(),
		JobId:        jobID,
		TotalTaskCnt: int32(len(tasks)),
		Message:      "批量删除任务已提交，请通过job_id查询任务状态",
	}, nil
}

// BatchDeployNodes 批量部署节点（提交异步任务）。
func (s *Service) BatchDeployNodes(ctx context.Context, req *pb.BatchDeployNodesReq) (*pb.BatchOperationRsp, error) {
	deployments := req.GetDeployments()
	if len(deployments) == 0 {
		return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "deployments cannot be empty")}, nil
	}
	tasks := make([]*pb.TaskRequestItem, 0, len(deployments))
	for _, d := range deployments {
		item := map[string]string{
			"node_id":    d.GetNodeId(),
			"package_id": d.GetPackageId(),
		}
		paramsJSON, err := json.Marshal(item)
		if err != nil {
			return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid deployment parameters")}, nil
		}
		tasks = append(tasks, &pb.TaskRequestItem{
			TaskType:      asynctask.TaskTypeDeployNode,
			RequestParams: string(paramsJSON),
		})
	}
	jobID, err := s.asyncSvc.AsyncJobCreate(ctx, tasks)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] BatchDeployNodes failed: %v", err)
		return &pb.BatchOperationRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to create batch job")}, nil
	}
	return &pb.BatchOperationRsp{
		RetInfo:      retOK(),
		JobId:        jobID,
		TotalTaskCnt: int32(len(tasks)),
		Message:      "批量部署任务已提交，请通过job_id查询任务状态",
	}, nil
}

// ========== 云账户 ==========

// ListCloudAccounts 获取云账户列表。
func (s *Service) ListCloudAccounts(ctx context.Context, req *pb.ListCloudAccountsReq) (*pb.ListCloudAccountsRsp, error) {
	var accounts []*pb.CloudAccount
	var err error
	if provider := req.GetProvider(); provider != "" {
		accounts, err = s.nodeSvc.ListAccountsByProvider(ctx, provider)
	} else {
		accounts, err = s.nodeSvc.ListAccounts(ctx)
	}
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] ListCloudAccounts failed: %v", err)
		return &pb.ListCloudAccountsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询云账户列表失败")}, nil
	}
	return &pb.ListCloudAccountsRsp{
		RetInfo:  retOK(),
		Accounts: accounts,
		Total:    int64(len(accounts)),
	}, nil
}

// GetCloudAccount 获取云账户详情。
func (s *Service) GetCloudAccount(ctx context.Context, req *pb.GetCloudAccountReq) (*pb.GetCloudAccountRsp, error) {
	if req.GetAccountId() == "" {
		return &pb.GetCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	account, err := s.nodeSvc.GetAccount(ctx, req.GetAccountId())
	if err != nil {
		return &pb.GetCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if account == nil {
		return &pb.GetCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	return &pb.GetCloudAccountRsp{RetInfo: retOK(), Account: account}, nil
}

// CreateCloudAccount 创建云账户。
func (s *Service) CreateCloudAccount(ctx context.Context, req *pb.CreateCloudAccountReq) (*pb.CreateCloudAccountRsp, error) {
	account := req.GetAccount()
	if account == nil {
		return &pb.CreateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account is required")}, nil
	}
	if err := s.nodeSvc.CreateAccount(ctx, account); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] CreateCloudAccount failed: %v", err)
		return &pb.CreateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	// 脱敏后返回
	account.SecretKey = "****"
	if len(account.GetSecretId()) > 4 {
		account.SecretId = account.GetSecretId()[:4] + "****"
	}
	return &pb.CreateCloudAccountRsp{RetInfo: retOK(), Account: account}, nil
}

// UpdateCloudAccount 更新云账户。
func (s *Service) UpdateCloudAccount(ctx context.Context, req *pb.UpdateCloudAccountReq) (*pb.UpdateCloudAccountRsp, error) {
	account := req.GetAccount()
	if account == nil || account.GetAccountId() == "" {
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	if err := s.nodeSvc.UpdateAccount(ctx, account); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] UpdateCloudAccount failed: %v", err)
		return &pb.UpdateCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.UpdateCloudAccountRsp{RetInfo: retOK()}, nil
}

// DeleteCloudAccount 删除云账户。
func (s *Service) DeleteCloudAccount(ctx context.Context, req *pb.DeleteCloudAccountReq) (*pb.DeleteCloudAccountRsp, error) {
	if req.GetAccountId() == "" {
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	if err := s.nodeSvc.DeleteAccount(ctx, req.GetAccountId()); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] DeleteCloudAccount failed: %v", err)
		return &pb.DeleteCloudAccountRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.DeleteCloudAccountRsp{RetInfo: retOK()}, nil
}

// GetCOSAccountInfo 获取 COS 账户信息（reveal=true 时含明文凭证，仅供 HMAC 鉴权路径）。
func (s *Service) GetCOSAccountInfo(ctx context.Context, req *pb.GetCOSAccountInfoReq) (*pb.GetCOSAccountInfoRsp, error) {
	if req.GetAccountId() == "" {
		return &pb.GetCOSAccountInfoRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "account_id is required")}, nil
	}
	info, err := s.nodeSvc.GetCOSAccountInfo(ctx, req.GetAccountId())
	if err != nil {
		return &pb.GetCOSAccountInfoRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "获取 COS 账户信息失败")}, nil
	}
	if info == nil {
		return &pb.GetCOSAccountInfoRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "cloud account not found")}, nil
	}
	resp := &pb.COSAccountInfo{
		AccountId: req.GetAccountId(),
		Provider:  info.GetProvider(),
		AppId:     info.GetAppId(),
		CosRegion: info.GetCosRegion(),
		CosBucket: info.GetCosBucket(),
	}
	if req.GetReveal() {
		resp.SecretId = info.GetSecretId()
		resp.SecretKey = info.GetSecretKey()
	} else {
		resp.SecretId = maskSecret(info.GetSecretId())
		resp.SecretKey = maskSecret(info.GetSecretKey())
	}
	return &pb.GetCOSAccountInfoRsp{RetInfo: retOK(), Info: resp}, nil
}

// ========== 云地区 ==========

// ListCloudRegions 获取云地区列表。
func (s *Service) ListCloudRegions(ctx context.Context, req *pb.ListCloudRegionsReq) (*pb.ListCloudRegionsRsp, error) {
	regions := getRegionsByProvider(req.GetProvider())
	pbRegions := make([]*pb.CloudRegion, 0, len(regions))
	for _, r := range regions {
		pbRegions = append(pbRegions, &pb.CloudRegion{
			Code:                     r.Code,
			Name:                     r.Name,
			Tag:                      r.Tag,
			MaxNodes:                 int32(r.MaxNodes),
			MaxNamespacesPerRegion:   int32(r.MaxNamespacesPerRegion),
			MaxFunctionsPerNamespace: int32(r.MaxFunctionsPerNamespace),
		})
	}
	return &pb.ListCloudRegionsRsp{
		RetInfo: retOK(),
		Regions: pbRegions,
		Total:   int64(len(pbRegions)),
	}, nil
}

// ========== 代码包 ==========

// GetPackageList 获取代码包列表。
func (s *Service) GetPackageList(ctx context.Context, req *pb.GetPackageListReq) (*pb.GetPackageListRsp, error) {
	rsp, err := s.nodeSvc.GetPackageList(ctx, req)
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] GetPackageList failed: %v", err)
		return &pb.GetPackageListRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询失败")}, nil
	}
	rsp.RetInfo = retOK()
	return rsp, nil
}

// GetPackageDetail 获取代码包详情。
func (s *Service) GetPackageDetail(ctx context.Context, req *pb.GetPackageDetailReq) (*pb.GetPackageDetailRsp, error) {
	if req.GetPackageId() == "" {
		return &pb.GetPackageDetailRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_id is required")}, nil
	}
	pkg, err := s.nodeSvc.GetPackageDetail(ctx, req.GetPackageId())
	if err != nil {
		return &pb.GetPackageDetailRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "代码包")}, nil
	}
	return &pb.GetPackageDetailRsp{RetInfo: retOK(), Detail: pkg}, nil
}

// DeletePackage 删除代码包。
func (s *Service) DeletePackage(ctx context.Context, req *pb.DeletePackageReq) (*pb.DeletePackageRsp, error) {
	if req.GetPackageId() == "" {
		return &pb.DeletePackageRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_id is required")}, nil
	}
	if err := s.nodeSvc.DeletePackage(ctx, req.GetPackageId()); err != nil {
		log.ErrorContextf(ctx, "[CloudNode] DeletePackage failed: %v", err)
		return &pb.DeletePackageRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "删除失败")}, nil
	}
	return &pb.DeletePackageRsp{RetInfo: retOK()}, nil
}

// GetPackageDownloadURL 获取代码包下载 URL。
func (s *Service) GetPackageDownloadURL(ctx context.Context, req *pb.GetPackageDownloadURLReq) (*pb.GetPackageDownloadURLRsp, error) {
	if req.GetPackageId() == "" {
		return &pb.GetPackageDownloadURLRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "package_id is required")}, nil
	}
	url, err := s.nodeSvc.GetPackageDownloadURL(ctx, req.GetPackageId())
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] GetPackageDownloadURL failed: %v", err)
		return &pb.GetPackageDownloadURLRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "获取下载URL失败")}, nil
	}
	return &pb.GetPackageDownloadURLRsp{RetInfo: retOK(), Url: url}, nil
}

// GetPackageOptions 获取代码包选项。
func (s *Service) GetPackageOptions(ctx context.Context, req *pb.GetPackageOptionsReq) (*pb.GetPackageOptionsRsp, error) {
	listReq := &pb.GetPackageListReq{
		Query: &pb.PackageListRequest{
			Page:        1,
			PageSize:    1000,
			PackageType: req.GetPackageType(),
			Status:      int32Ptr(1),
		},
	}
	rsp, err := s.nodeSvc.GetPackageList(ctx, listReq)
	if err != nil {
		return &pb.GetPackageOptionsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "查询失败")}, nil
	}
	options := make([]*pb.PackageOption, 0, len(rsp.GetItems()))
	for _, item := range rsp.GetItems() {
		displayName := item.GetPackageName()
		if item.GetPackageType() == "data_collector" {
			displayName = "数据采集器"
		}
		options = append(options, &pb.PackageOption{
			PackageId:   item.GetPackageId(),
			Label:       fmt.Sprintf("[%s] %s %s (%s) - %s", item.GetPackageTypeLabel(), displayName, item.GetVersion(), item.GetRuntime(), trimDate(item.GetCreatedTime())),
			PackageName: item.GetPackageName(),
			Version:     item.GetVersion(),
			Runtime:     item.GetRuntime(),
			PackageType: item.GetPackageType(),
		})
	}
	return &pb.GetPackageOptionsRsp{RetInfo: retOK(), Options: options}, nil
}

// int32Ptr 返回 int32 指针。
func int32Ptr(v int32) *int32 { return &v }

// trimDate 截取日期部分（前10位）。
func trimDate(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// ========== 心跳 ==========

// ReportHeartbeat 心跳上报。
func (s *Service) ReportHeartbeat(ctx context.Context, req *pb.ReportHeartbeatReq) (*pb.ReportHeartbeatRsp, error) {
	if req.GetNodeId() == "" || req.GetNodeType() == "" {
		return &pb.ReportHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "node_id and node_type are required")}, nil
	}
	resp, err := s.nodeSvc.ReportHeartbeat(ctx, reportHeartbeatReqToTypes(req))
	if err != nil {
		log.ErrorContextf(ctx, "[CloudNode] ReportHeartbeat failed: %v", err)
		return &pb.ReportHeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "heartbeat failed")}, nil
	}
	return reportHeartbeatRspToPB(resp), nil
}
