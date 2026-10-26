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
