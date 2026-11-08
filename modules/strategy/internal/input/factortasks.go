package input

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/factorname"
)

// 策略实例与因子任务的关联、参数提交（设计 6.2、8.1、8.3）。实例创建时选定因子任务；第一次启用时调用 BindFactorTask
// 与任务正式关联（独占、永久，幂等）；每期求出的下一期参数经结果表里的待投递记录异步提交。

// ParamDecl 是因子定义声明的一个可调参数。
type ParamDecl = dsl.ParamDecl

// ViewIDOfTask 由因子任务 ID 推导它的结果数据集的默认 View。
func ViewIDOfTask(taskID string) (string, error) {
	suffix, err := factorname.ParseTaskID(strings.TrimSpace(taskID))
	if err != nil {
		return "", err
	}
	return factorname.ResultViewID(suffix)
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

// FactorTaskRecord 是删除实例时要看的任务信息：所在空间、结果数据集与关联的实例。
type FactorTaskRecord struct {
	SpaceID         string
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
	return FactorTaskRecord{SpaceID: task.GetSpaceId(), ResultDatasetID: task.GetResultDatasetId(), BoundInstanceID: task.GetBoundInstanceId()}, nil
}

// factorTaskPageSize 与 factorTaskMaxPages 是列出全部因子任务时的每页条数与翻页上限。
const (
	factorTaskPageSize = 500
	factorTaskMaxPages = 100
)

// ListFactorTaskIDs 分页列出 Factor 里现有的全部因子任务 ID。
func (c *RPCClient) ListFactorTaskIDs(ctx context.Context) (map[string]bool, error) {
	if c == nil || c.Factor == nil {
		return nil, errors.New("Factor 客户端未配置")
	}
	ids := map[string]bool{}
	for page := uint32(1); page <= factorTaskMaxPages; page++ {
		rsp, err := c.Factor.ListFactorTasks(ctx, &factorpb.ListFactorTasksReq{Page: &commonpb.Page{Page: page, Size: factorTaskPageSize}})
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

// FactorTasks 是任务关联与参数提交用到的 FactorMgr 接口。
type FactorTasks interface {
	BindFactorTask(ctx context.Context, taskID, instanceID string) (BoundTask, error)
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
	for _, key := range sortedMapKeys(submission.Members) {
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

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	return keys
}

// VarMember 是关联任务里的一个可变成员：成员键、引用的因子定义和定义声明的可调参数。
type VarMember struct {
	Key      string
	FactorID string
	Params   []ParamDecl
}

// CheckFactorParams 对照任务的可变成员检查 DSL（设计 8.1、8.2）：factor_params 与 params() 引用的成员必须是任务的可变成员，
// 参数名必须是该因子声明的参数；任务的每个可变成员都要在 factor_params 里有输出（可变成员与 factor_params 一一对应）。
func CheckFactorParams(strategy dsl.Strategy, members map[string]VarMember, refs []dsl.ParamRef) error {
	available := func() string {
		keys := make([]string, 0, len(members))
		for key := range members {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) == 0 {
			return "该任务没有可变成员"
		}
		return "任务的可变成员：" + strings.Join(keys, "、")
	}
	declared := func(member VarMember, name string) bool {
		for _, decl := range member.Params {
			if decl.Name == name {
				return true
			}
		}
		return false
	}
	check := func(where, key, name string) error {
		member, ok := members[key]
		if !ok {
			return fmt.Errorf("%s：成员 %s 不是关联任务的可变成员（%s）", where, key, available())
		}
		if !declared(member, name) {
			names := make([]string, 0, len(member.Params))
			for _, decl := range member.Params {
				names = append(names, decl.Name)
			}
			return fmt.Errorf("%s：因子 %s 没有参数 %s（可调参数：%s）", where, member.FactorID, name, strings.Join(names, "、"))
		}
		return nil
	}
	written := make(map[string]struct{})
	for _, output := range strategy.FactorParamOutputs() {
		if err := check("factor_params."+output.Member+"."+output.Name, output.Member, output.Name); err != nil {
			return err
		}
		written[output.Member] = struct{}{}
	}
	for _, ref := range refs {
		if err := check(fmt.Sprintf(`params("%s", "%s")`, ref.Member, ref.Name), ref.Member, ref.Name); err != nil {
			return err
		}
	}
	for _, key := range sortedMapKeys(members) {
		if _, ok := written[key]; !ok {
			return fmt.Errorf("关联任务的可变成员 %s 在 factor_params 里没有输出：可变成员的参数由策略逐期提交，没有输出就永远用默认参数", key)
		}
	}
	return nil
}
