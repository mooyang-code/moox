package input

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

// 策略实例的私有因子任务与参数提交（因子动态调参执行计划 2.3、2.8）。声明了 factors 的策略在启用实例时由 FactorMgr
// 建立私有因子任务（成员恰好是声明的因子，实例是唯一的参数写入方），实例绑定它的结果 View 求值；每期求出的下一期参数
// 经结果库里的待提交记录异步提交。

// ParamDecl 是因子定义声明的一个可调参数。
type ParamDecl = dsl.ParamDecl

// InstanceFactorTaskRequest 是为实例建立或对齐私有因子任务的请求。
type InstanceFactorTaskRequest struct {
	InstanceID      string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	Members         []dsl.FactorDecl
}

// InstanceFactorTask 是私有因子任务：集合 ID 与它的结果 View。
type InstanceFactorTask struct {
	TaskID string
	ViewID string
}

// ParamValue 是一个参数一期的取值：统一值加按标的覆盖。
type ParamValue struct {
	Default   float64            `json:"default"`
	Overrides map[string]float64 `json:"overrides,omitempty"`
}

// ParamSubmission 是一期的参数提交：写入方、集合、期次与各成员的完整快照。
type ParamSubmission struct {
	RequestID     string                           `json:"request_id"`
	Writer        string                           `json:"writer"`
	TaskID        string                           `json:"task_id"`
	PeriodTime    time.Time                        `json:"period_time"`
	BasedOnPeriod time.Time                        `json:"based_on_period"`
	Members       map[string]map[string]ParamValue `json:"members"`
}

// MemberSubmitResult 是一个成员的提交结果：accepted、too_late 或 rejected。
type MemberSubmitResult struct {
	Member string `json:"member"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// 提交结果。
const (
	SubmitAccepted = "accepted"
	SubmitTooLate  = "too_late"
	SubmitRejected = "rejected"
)

// FactorTasks 是私有因子任务与参数提交用到的 FactorMgr 接口。
type FactorTasks interface {
	EnsureInstanceFactorTask(ctx context.Context, request InstanceFactorTaskRequest) (InstanceFactorTask, error)
	SetInstanceFactorTaskEnabled(ctx context.Context, instanceID string, enabled bool) error
	ReleaseInstanceFactorTask(ctx context.Context, instanceID string) error
	SubmitFactorParams(ctx context.Context, submission ParamSubmission) ([]MemberSubmitResult, error)
}

var _ FactorTasks = (*RPCClient)(nil)

// EnsureInstanceFactorTask 建立或对齐实例的私有因子任务（幂等）。
func (c *RPCClient) EnsureInstanceFactorTask(ctx context.Context, request InstanceFactorTaskRequest) (InstanceFactorTask, error) {
	if c == nil || c.Factor == nil {
		return InstanceFactorTask{}, errors.New("Factor 客户端未配置")
	}
	req := &factorpb.EnsureInstanceFactorTaskReq{InstanceId: request.InstanceID, SpaceId: request.SpaceID,
		SourceDatasetId: request.SourceDatasetID, Freq: request.Freq}
	for _, member := range request.Members {
		req.Members = append(req.Members, &factorpb.InstanceTaskMember{MemberAlias: member.Alias, FactorId: member.FactorID})
	}
	rsp, err := c.Factor.EnsureInstanceFactorTask(ctx, req)
	if err != nil {
		return InstanceFactorTask{}, transport("建立实例的私有因子任务", err)
	}
	if err := retError("建立实例的私有因子任务", rsp.GetRetInfo()); err != nil {
		return InstanceFactorTask{}, err
	}
	set := InstanceFactorTask{TaskID: rsp.GetFactorTask().GetTaskId(), ViewID: strings.TrimSpace(rsp.GetViewId())}
	if set.TaskID == "" || set.ViewID == "" {
		return InstanceFactorTask{}, errors.New("FactorMgr 没有返回私有因子任务或它的 View")
	}
	return set, nil
}

// SetInstanceFactorTaskEnabled 停用或重新启用实例的私有因子任务；停用没有私有因子任务的实例视为成功。
func (c *RPCClient) SetInstanceFactorTaskEnabled(ctx context.Context, instanceID string, enabled bool) error {
	if c == nil || c.Factor == nil {
		return errors.New("Factor 客户端未配置")
	}
	rsp, err := c.Factor.SetInstanceFactorTaskEnabled(ctx, &factorpb.SetInstanceFactorTaskEnabledReq{InstanceId: instanceID, Enabled: enabled})
	if err != nil {
		return transport("切换实例私有因子任务的启用状态", err)
	}
	return retError("切换实例私有因子任务的启用状态", rsp.GetRetInfo())
}

// ReleaseInstanceFactorTask 删除实例的私有因子任务与它的结果数据集（幂等）。
func (c *RPCClient) ReleaseInstanceFactorTask(ctx context.Context, instanceID string) error {
	if c == nil || c.Factor == nil {
		return errors.New("Factor 客户端未配置")
	}
	rsp, err := c.Factor.ReleaseInstanceFactorTask(ctx, &factorpb.ReleaseInstanceFactorTaskReq{InstanceId: instanceID})
	if err != nil {
		return transport("删除实例的私有因子任务", err)
	}
	return retError("删除实例的私有因子任务", rsp.GetRetInfo())
}

// SubmitFactorParams 提交一期的参数快照。
func (c *RPCClient) SubmitFactorParams(ctx context.Context, submission ParamSubmission) ([]MemberSubmitResult, error) {
	if c == nil || c.Factor == nil {
		return nil, errors.New("Factor 客户端未配置")
	}
	req := &factorpb.SubmitFactorParamsReq{Writer: submission.Writer, TaskId: submission.TaskID, RequestId: submission.RequestID,
		PeriodTime: submission.PeriodTime.Unix()}
	if !submission.BasedOnPeriod.IsZero() {
		req.BasedOnPeriod = submission.BasedOnPeriod.Unix()
	}
	for _, alias := range sortedMapKeys(submission.Members) {
		member := &factorpb.MemberParams{MemberAlias: alias, Params: map[string]*factorpb.ParamValue{}}
		for name, value := range submission.Members[alias] {
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
		results = append(results, MemberSubmitResult{Member: item.GetMemberAlias(), Result: item.GetResult(), Reason: item.GetReason()})
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

// CheckFactorParams 对照因子定义检查 factor_params：参数名必须是该因子声明的参数。
func CheckFactorParams(strategy dsl.Strategy, definitions map[string]FactorInfo) error {
	byAlias := make(map[string]string, len(strategy.Factors))
	for _, decl := range strategy.Factors {
		byAlias[decl.Alias] = decl.FactorID
	}
	for _, output := range strategy.FactorParamOutputs() {
		factor := definitions[byAlias[output.Alias]]
		found := false
		for _, decl := range factor.Params {
			if decl.Name == output.Name {
				found = true
				break
			}
		}
		if !found {
			names := make([]string, 0, len(factor.Params))
			for _, decl := range factor.Params {
				names = append(names, decl.Name)
			}
			available := "没有可调参数"
			if len(names) > 0 {
				available = "可调参数：" + strings.Join(names, "、")
			}
			return fmt.Errorf("factor_params.%s.%s：因子 %s 没有参数 %s（%s）", output.Alias, output.Name, factor.FactorID, output.Name, available)
		}
	}
	return nil
}
