package upstream

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/spacecontext"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorname"
)

// 策略实例与因子任务的关联、参数提交（设计 6.2、8.1、8.3）。实例创建时选定因子任务；声明了 factor_params 的实例第一次启用时
// 调用 BindFactorTask 与主帧任务正式关联（独占、永久，幂等），其余实例不关联、只读任务的列；每期求出的下一期参数经结果表里的
// 待投递记录异步提交。

// ParamDecl 是因子定义声明的一个可调参数。
type ParamDecl = dsl.ParamDecl

// DatasetIDOfTask 由因子任务 ID 推导它的结果数据集。
func DatasetIDOfTask(taskID string) (string, error) {
	suffix, err := factorname.ParseTaskID(strings.TrimSpace(taskID))
	if err != nil {
		return "", err
	}
	return factorname.ResultDatasetID(suffix)
}

// ParamValue 是一个参数一期的取值：统一值加按标的覆盖。
type ParamValue struct {
	Default   float64            `json:"default"`
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

// ParamSubmission 是一期的参数提交：写入方、任务、目标期次、幂等键与各可变成员的完整快照。
type ParamSubmission struct {
	RequestID  string                           `json:"request_id"`
	Writer     string                           `json:"writer"`
	TaskID     string                           `json:"task_id"`
	PeriodTime time.Time                        `json:"period_time"`
	Members    map[string]map[string]ParamValue `json:"members"`
}

// MemberSubmitResult 是一个成员的提交结果：accepted、duplicate（幂等重复）或 rejected。
type MemberSubmitResult struct {
	Member string `json:"member"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// 提交结果。
const (
	SubmitAccepted  = "accepted"
	SubmitDuplicate = "duplicate"
	SubmitRejected  = "rejected"
)

// BoundTask 是 BindFactorTask 返回的任务状态。
type BoundTask struct {
	TaskID          string
	Status          string
	BoundInstanceID string
}

// ErrFactorTaskNotFound 表示因子任务在 Factor 里已经不存在（例如随实例删除到一半）。
var ErrFactorTaskNotFound = errors.New("因子任务不存在")

// FactorTaskRecord 是启用与删除实例时要看的任务信息：所在空间、状态、结果数据集与关联的实例。
type FactorTaskRecord struct {
	SpaceID         string
	Status          string
	ResultDatasetID string
	BoundInstanceID string
}

// GetFactorTask 读取一个因子任务；任务不存在时返回 ErrFactorTaskNotFound。
func (c *RPCClient) GetFactorTask(ctx context.Context, taskID string) (FactorTaskRecord, error) {
	if c == nil || c.Factor == nil {
		return FactorTaskRecord{}, errors.New("Factor 客户端未配置")
	}
	rsp, err := c.Factor.GetFactorTask(ctx, &factorpb.GetFactorTaskReq{TaskId: taskID})
	if err != nil {
		return FactorTaskRecord{}, transport("读取因子任务", err)
	}
	if rsp.GetRetInfo().GetCode() == commonpb.ErrorCode_NOT_FOUND {
		return FactorTaskRecord{}, fmt.Errorf("%w：%s", ErrFactorTaskNotFound, taskID)
	}
	if err := retError("读取因子任务", rsp.GetRetInfo()); err != nil {
		return FactorTaskRecord{}, err
	}
	task := rsp.GetFactorTask()
	return FactorTaskRecord{SpaceID: task.GetSpaceId(), Status: task.GetStatus(), ResultDatasetID: task.GetResultDatasetId(), BoundInstanceID: task.GetBoundInstanceId()}, nil
}

// factorTaskPageSize 与 factorTaskMaxPages 是列出全部因子任务时的每页条数与翻页上限。
const (
	factorTaskPageSize = 500
	factorTaskMaxPages = 100
)

// ListFactorTaskIDs 分页列出 Factor 里现有的因子任务 ID；ctx 带空间身份时按空间过滤，避免跨空间扫描。
func (c *RPCClient) ListFactorTaskIDs(ctx context.Context) (map[string]bool, error) {
	if c == nil || c.Factor == nil {
		return nil, errors.New("Factor 客户端未配置")
	}
	spaceID, _ := spacecontext.FromContext(ctx)
	ids := map[string]bool{}
	for page := uint32(1); page <= factorTaskMaxPages; page++ {
		rsp, err := c.Factor.ListFactorTasks(ctx, &factorpb.ListFactorTasksReq{
			SpaceId: spaceID,
			Page:    &commonpb.Page{Page: page, Size: factorTaskPageSize},
		})
		if err != nil {
			return nil, transport("列出因子任务", err)
		}
		if err := retError("列出因子任务", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, info := range rsp.GetFactorTasks() {
			ids[info.GetFactorTask().GetTaskId()] = true
		}
		if !rsp.GetPageResult().GetHasMore() || len(rsp.GetFactorTasks()) == 0 {
			return ids, nil
		}
	}
	return nil, fmt.Errorf("列出因子任务失败：超过 %d 页仍没有取完", factorTaskMaxPages)
}

// RejectedError 是 Factor 明确拒绝（INVALID_PARAM）的删除：原因原样透传给调用方，不当成依赖不可用。
type RejectedError struct {
	Operation string
	Msg       string
}

func (e *RejectedError) Error() string { return e.Operation + "被拒绝：" + e.Msg }

// CheckBoundTask 只做 DeleteBoundTask 动手之前的全部检查（关联、补算、结果数据集能否删），不做任何修改。
func (c *RPCClient) CheckBoundTask(ctx context.Context, taskID, instanceID string) error {
	return c.deleteBoundTask(ctx, "检查因子任务能否删除", &factorpb.DeleteBoundTaskReq{TaskId: taskID, BoundInstanceId: instanceID, CheckOnly: true})
}

// DeleteBoundTask 删除关联到实例的因子任务，连同成员与结果数据集；任务不存在也返回成功。
func (c *RPCClient) DeleteBoundTask(ctx context.Context, taskID, instanceID string) error {
	return c.deleteBoundTask(ctx, "删除因子任务", &factorpb.DeleteBoundTaskReq{TaskId: taskID, BoundInstanceId: instanceID})
}

func (c *RPCClient) deleteBoundTask(ctx context.Context, operation string, req *factorpb.DeleteBoundTaskReq) error {
	if c == nil || c.Factor == nil {
		return errors.New("Factor 客户端未配置")
	}
	rsp, err := c.Factor.DeleteBoundTask(ctx, req)
	if err != nil {
		return transport(operation, err)
	}
	if rsp.GetRetInfo().GetCode() == commonpb.ErrorCode_INVALID_PARAM {
		return &RejectedError{Operation: operation, Msg: rsp.GetRetInfo().GetMsg()}
	}
	return retError(operation, rsp.GetRetInfo())
}

// FactorTasks 是任务关联、启用前核对任务状态与参数提交用到的 FactorMgr 接口。
type FactorTasks interface {
	BindFactorTask(ctx context.Context, taskID, instanceID string) (BoundTask, error)
	// GetFactorTask 读取任务；任务不存在时返回 ErrFactorTaskNotFound。
	GetFactorTask(ctx context.Context, taskID string) (FactorTaskRecord, error)
	SubmitFactorParams(ctx context.Context, submission ParamSubmission) ([]MemberSubmitResult, error)
}

var _ FactorTasks = (*RPCClient)(nil)

// BindFactorTask 把任务与实例永久关联（幂等：同一个实例重复调用直接成功，换一个实例会被拒绝）。
func (c *RPCClient) BindFactorTask(ctx context.Context, taskID, instanceID string) (BoundTask, error) {
	if c == nil || c.Factor == nil {
		return BoundTask{}, errors.New("Factor 客户端未配置")
	}
	rsp, err := c.Factor.BindFactorTask(ctx, &factorpb.BindFactorTaskReq{TaskId: taskID, InstanceId: instanceID})
	if err != nil {
		return BoundTask{}, transport("关联因子任务", err)
	}
	if err := retError("关联因子任务", rsp.GetRetInfo()); err != nil {
		return BoundTask{}, err
	}
	task := rsp.GetFactorTask()
	return BoundTask{TaskID: task.GetTaskId(), Status: task.GetStatus(), BoundInstanceID: task.GetBoundInstanceId()}, nil
}

// SubmitFactorParams 提交一期的参数快照。
func (c *RPCClient) SubmitFactorParams(ctx context.Context, submission ParamSubmission) ([]MemberSubmitResult, error) {
	if c == nil || c.Factor == nil {
		return nil, errors.New("Factor 客户端未配置")
	}
	req := &factorpb.SubmitFactorParamsReq{Writer: submission.Writer, TaskId: submission.TaskID, RequestId: submission.RequestID,
		PeriodTime: submission.PeriodTime.Unix()}
	for _, key := range sortedKeys(submission.Members) {
		member := &factorpb.MemberParams{MemberKey: key, Params: map[string]*factorpb.ParamValue{}}
		for name, value := range submission.Members[key] {
			member.Params[name] = &factorpb.ParamValue{DefaultValue: value.Default, Overrides: value.Overrides}
		}
		req.Members = append(req.Members, member)
	}
	rsp, err := c.Factor.SubmitFactorParams(ctx, req)
	if err != nil {
		return nil, transport("提交因子参数", err)
	}
	if err := retError("提交因子参数", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	results := make([]MemberSubmitResult, 0, len(rsp.GetResults()))
	for _, item := range rsp.GetResults() {
		results = append(results, MemberSubmitResult{Member: item.GetMemberKey(), Result: item.GetResult(), Reason: item.GetReason()})
	}
	return results, nil
}
