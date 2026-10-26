package input

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
)

const tunedDSL = `name: tuned
factors:
  fast: {factor: bias}
  slow: {factor: bias}
factor_params:
  fast:
    window: "20"
rules:
  - id: r
    type: rank
    score: "fast_bias_q_20 - slow_bias_q_20"
    select: {top: 1}
    weight: 1
`

type setsFake struct {
	requests []InstanceFactorTaskRequest
	err      error
}

func (f *setsFake) EnsureInstanceFactorTask(_ context.Context, request InstanceFactorTaskRequest) (InstanceFactorTask, error) {
	f.requests = append(f.requests, request)
	return InstanceFactorTask{TaskID: "ftask_i1", ViewID: "view_factor_i1"}, f.err
}
func (f *setsFake) SetInstanceFactorTaskEnabled(context.Context, string, bool) error { return nil }
func (f *setsFake) ReleaseInstanceFactorTask(context.Context, string) error          { return nil }
func (f *setsFake) SubmitFactorParams(context.Context, ParamSubmission) ([]MemberSubmitResult, error) {
	return nil, nil
}

// lateColumns 模拟 Storage 异步扩展私有 View 的列：前几次读取只有 K 线列，之后才有成员列。
type lateColumns struct {
	*fakeClient
	reads int
	after int
}

func (c *lateColumns) GetView(ctx context.Context, space, viewID string) (ViewInfo, error) {
	view, err := c.fakeClient.GetView(ctx, space, viewID)
	if viewID != "view_factor_i1" {
		return view, err
	}
	c.reads++
	if c.reads <= c.after {
		view.Columns = []ViewColumn{plainColumn("close")}
	}
	return view, err
}

func memberColumn(name, member, factorID string) ViewColumn {
	return ViewColumn{Name: name, Attributes: map[string]string{"member_alias": member, "factor_id": factorID, "origin_factor_id": member, "factor_output": "bias_q_20", "column_kind": "output"}}
}

func paramColumn(name, member string) ViewColumn {
	return ViewColumn{Name: name, Attributes: map[string]string{"member_alias": member, "factor_id": "bias", "origin_factor_id": member, "column_kind": "param", "param_name": "window"}}
}

func tunedClient() *fakeClient {
	client := newFakeClient("spot")
	client.factors["bias"] = FactorInfo{FactorID: "bias", DefinitionHash: "sha256:bias", Params: []ParamDecl{{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}}
	client.views["view_kline"] = ViewInfo{ViewID: "view_kline", DatasetID: "ds_kline", Frequency: "1h", Status: "active", ActiveIndexID: "idx_k", Columns: []ViewColumn{plainColumn("close")}}
	client.views["view_factor_i1"] = ViewInfo{ViewID: "view_factor_i1", DatasetID: "ds_factor", Frequency: "1h", Status: "active", ActiveIndexID: "idx_f", Columns: []ViewColumn{
		plainColumn("close"), memberColumn("fast_bias_q_20", "fast", "bias"), memberColumn("slow_bias_q_20", "slow", "bias"), paramColumn("fast__window", "fast"), paramColumn("slow__window", "slow"),
	}}
	return client
}

// 声明了 factors 的策略：先为实例建立私有因子任务（成员恰好是声明的因子），再绑定它的结果 View。同一定义的两个别名
// 各有自己的列绑定与指纹；参数列不当作因子列；启用时固化私有因子任务、K 线 View 与各成员的参数声明。
func TestResolveInstanceBindsPrivateFactorTask(t *testing.T) {
	client, sets := tunedClient(), &setsFake{}
	service := Service{Client: client, Sets: sets}
	resolved, program, err := service.ResolveInstance(context.Background(), "i1", "space", "view_kline", parseStrategy(t, tunedDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(sets.requests) != 1 {
		t.Fatalf("应建立一次私有因子任务：%+v", sets.requests)
	}
	request := sets.requests[0]
	if request.InstanceID != "i1" || request.SpaceID != "space" || request.SourceDatasetID != "ds_kline" || request.Freq != "1h" ||
		len(request.Members) != 2 || request.Members[0] != (dsl.FactorDecl{Alias: "fast", FactorID: "bias"}) {
		t.Fatalf("建集合请求不符：%+v", request)
	}
	if resolved.ViewID != "view_factor_i1" || resolved.SourceViewID != "view_kline" || resolved.FactorTaskID != "ftask_i1" {
		t.Fatalf("应绑定私有 View 并记下 K 线 View 与集合：%+v", resolved)
	}
	if got := resolved.Columns["fast_bias_q_20"]; got.Source != SourceFactor || got.Member != "fast" || got.FactorID != "bias" || got.DefinitionHash != "sha256:bias" {
		t.Fatalf("fast 的列绑定不符：%+v", got)
	}
	if resolved.Factors["fast"] != "sha256:bias" || resolved.Factors["slow"] != "sha256:bias" || len(resolved.Factors) != 2 {
		t.Fatalf("指纹应按成员别名固化：%v", resolved.Factors)
	}
	if got := resolved.ColumnsOfFactor("slow"); len(got) != 1 || got[0] != "slow_bias_q_20" {
		t.Fatalf("slow 成员的列不符：%v", got)
	}
	if len(resolved.FactorParams["fast"]) != 1 || resolved.FactorParams["fast"][0].Max != 60 {
		t.Fatalf("应固化参数声明：%+v", resolved.FactorParams)
	}
	if len(program.Params) != 1 || program.Params[0].Alias != "fast" {
		t.Fatalf("参数输出应已编译：%+v", program.Params)
	}
}

func TestResolveInstanceRejectsUnknownParamAndMissingDependencies(t *testing.T) {
	strategy := parseStrategy(t, strings.Replace(tunedDSL, `    window: "20"`, `    period: "20"`, 1))
	_, _, err := Service{Client: tunedClient(), Sets: &setsFake{}}.ResolveInstance(context.Background(), "i1", "space", "view_kline", strategy)
	if err == nil || !strings.Contains(err.Error(), "factor_params.fast.period") || !strings.Contains(err.Error(), "window") {
		t.Fatalf("应指出没有这个参数并列出可调参数：%v", err)
	}
	if _, _, err := (Service{Client: tunedClient()}).ResolveInstance(context.Background(), "i1", "space", "view_kline", parseStrategy(t, tunedDSL)); err == nil {
		t.Fatal("没有接线 Factor 时不能启用声明了 factors 的策略")
	}
	// 没有声明 factors 的策略不建私有因子任务。
	sets := &setsFake{}
	if _, _, err := (Service{Client: newFakeClient("spot"), Sets: sets}).ResolveInstance(context.Background(), "i1", "space", "view_factor_1h", parseStrategy(t, exampleDSL)); err != nil || len(sets.requests) != 0 {
		t.Fatalf("普通策略应直接解析：err=%v requests=%+v", err, sets.requests)
	}
}

// 结果 View 的成员列由 Storage 异步补齐：补齐之前重试，补齐后解析成功；等待超时则报最后一次的缺列错误。
func TestResolveInstanceWaitsForMemberColumns(t *testing.T) {
	late := &lateColumns{fakeClient: tunedClient(), after: 3}
	service := Service{Client: late, Sets: &setsFake{}, ReadyWait: 2 * time.Second, ReadyStep: time.Millisecond}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", "view_kline", parseStrategy(t, tunedDSL)); err != nil {
		t.Fatalf("补齐后应解析成功：%v", err)
	}
	if late.reads <= 3 {
		t.Fatalf("应重试到列补齐：reads=%d", late.reads)
	}
	never := &lateColumns{fakeClient: tunedClient(), after: 1 << 30}
	service = Service{Client: never, Sets: &setsFake{}, ReadyWait: 20 * time.Millisecond, ReadyStep: 5 * time.Millisecond}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", "view_kline", parseStrategy(t, tunedDSL)); err == nil || !strings.Contains(err.Error(), "fast_bias_q_20") {
		t.Fatalf("超时应报缺失的列：%v", err)
	}
}
