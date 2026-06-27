// Package rpc 提供 asynctask 对外的 trpc 普通 RPC 服务实现，
// 由统一 HTTP 转发层（/api/admin/asynctask/{method}）调度。
package rpc

import (
	"context"

	"github.com/mooyang-code/moox/modules/admin/internal/service/asynctask"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现 pb.AsyncTaskService，承载 asynctask 业务逻辑。
//
// 设计说明：service 层已直接使用 PB 类型，rpc 层仅为薄包装——
// 调用 service 方法并补充 common.RetInfo，不再做 DTO↔PB 翻译。
type Service struct {
	pb.UnimplementedAsyncTask
	svc asynctask.Service
}

// NewService 创建 AsyncTask RPC 实现。
func NewService(svc asynctask.Service) *Service {
	return &Service{svc: svc}
}

// CreateAsyncJob 创建异步 Job。
func (s *Service) CreateAsyncJob(ctx context.Context, req *pb.CreateAsyncJobReq) (*pb.CreateAsyncJobRsp, error) {
	if len(req.GetTasks()) == 0 {
		return &pb.CreateAsyncJobRsp{
			RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "tasks cannot be empty"),
		}, nil
	}

	jobID, err := s.svc.AsyncJobCreate(ctx, req.GetTasks())
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] AsyncJobCreate failed: %v", err)
		return &pb.CreateAsyncJobRsp{
			RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to create job"),
		}, nil
	}

	return &pb.CreateAsyncJobRsp{
		RetInfo:      retOK(),
		JobId:        jobID,
		TotalTaskCnt: int32(len(req.GetTasks())),
	}, nil
}

// QueryAsyncJob 查询 Job 状态。
func (s *Service) QueryAsyncJob(ctx context.Context, req *pb.QueryAsyncJobReq) (*pb.QueryAsyncJobRsp, error) {
	if req.GetJobId() == "" {
		return &pb.QueryAsyncJobRsp{
			RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "job_id is required"),
		}, nil
	}

	rsp, err := s.svc.AsyncJobQuery(ctx, req.GetJobId())
	if err != nil {
		log.ErrorContextf(ctx, "[AsyncTask] AsyncJobQuery failed: %v", err)
		return &pb.QueryAsyncJobRsp{
			RetInfo: retErr(pb.ErrorCode_INNER_ERR, "failed to query job"),
		}, nil
	}

	// service 层返回的 PB 响应未带 RetInfo，此处补成功标识
	rsp.RetInfo = retOK()
	return rsp, nil
}

// retOK 构造成功 RetInfo。
func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

// retErr 构造错误 RetInfo。
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}
