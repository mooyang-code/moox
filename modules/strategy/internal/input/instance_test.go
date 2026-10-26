package input

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

const tunedDSL = `name: tuned
factor_params:
  bias__var:
    window: 20
rules:
  - id: r
    type: rank
    score: "bias__var - ma"
    select: {top: 1}
    weight: 1
`

// tasksFake 是 FactorMgr 任务关联接口的替身。
type tasksFake struct {
	bound  BoundTask
	err    error
	binds  []string
	submit []ParamSubmission
}

func (f *tasksFake) BindFactorTask(_ context.Context, taskID, instanceID string) (BoundTask, error) {
	f.binds = append(f.binds, taskID+"@"+instanceID)
	if f.err != nil {
		return BoundTask{}, f.err
	}
	if f.bound.TaskID == "" {
		return BoundTask{TaskID: taskID, Status: "enabled", BoundInstanceID: instanceID}, nil
	}
	return f.bound, nil
}

func (f *tasksFake) SubmitFactorParams(_ context.Context, submission ParamSubmission) ([]MemberSubmitResult, error) {
	f.submit = append(f.submit, submission)
	return nil, nil
}

// lateColumns 模拟 Storage 异步扩展结果 View 的列：前几次读取只有 K 线列，之后才有成员列。
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

// tunedClient 是任务 ftask_i1 的结果 View：一个固定成员 ma、一个可变成员 bias__var（bias 的 window 参数可调）。
func tunedClient() *fakeClient {
	client := newFakeClient("spot")
	client.factors["bias"] = FactorInfo{FactorID: "bias", DefinitionHash: "sha256:bias", Params: []ParamDecl{{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}}
	client.views["view_factor_i1"] = ViewInfo{ViewID: "view_factor_i1", DatasetID: "ds_factor", Frequency: "1h", Status: "active", ActiveIndexID: "idx_f", Columns: []ViewColumn{
		plainColumn("close"), memberColumn("ma", "ma", "ma", "fixed"), memberColumn("bias__var", "bias__var", "bias", "var"), paramsColumn(),
	}}
	return client
}

// 启用时：先把任务与实例永久关联，再绑定它的结果 View；可变成员按列属性识别，固化成员契约与参数声明，参数输出已编译。
func TestResolveInstanceBindsFactorTask(t *testing.T) {
	client, tasks := tunedClient(), &tasksFake{}
	service := Service{Client: client, Sets: tasks}
	resolved, program, err := service.ResolveInstance(context.Background(), "i1", "space", "ftask_i1", parseStrategy(t, tunedDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if len(tasks.binds) != 1 || tasks.binds[0] != "ftask_i1@i1" {
		t.Fatalf("应关联一次任务：%v", tasks.binds)
	}
	if resolved.ViewID != "view_factor_i1" || resolved.FactorTaskID != "ftask_i1" {
		t.Fatalf("应绑定任务的结果 View：%+v", resolved)
	}
	if got := resolved.Columns["bias__var"]; got.Source != SourceFactor || got.Member != "bias__var" || got.FactorID != "bias" || got.MemberKind != "var" || got.DefinitionHash != "sha256:bias" {
		t.Fatalf("可变成员的列绑定不符：%+v", got)
	}
	if got := resolved.Columns["ma"]; got.MemberKind != "fixed" || got.DefinitionHash != "sha256:ma" {
		t.Fatalf("固定成员的列绑定不符：%+v", got)
	}
	if resolved.Factors["bias__var"] != readiness.Contract("sha256:bias", "var") || resolved.Factors["ma"] != readiness.Contract("sha256:ma", "fixed") || len(resolved.Factors) != 2 {
		t.Fatalf("成员契约应按成员键固化：%v", resolved.Factors)
	}
	if params := resolved.FactorParams["bias__var"]; len(params) != 1 || params[0].Max != 60 || len(resolved.FactorParams) != 1 {
		t.Fatalf("应只固化可变成员的参数声明：%+v", resolved.FactorParams)
	}
	if len(program.Params) != 1 || program.Params[0].Member != "bias__var" {
		t.Fatalf("参数输出应已编译：%+v", program.Params)
	}
}

// 可变成员与 factor_params 一一对应；参数名必须是定义声明的；params() 只能读可变成员。
func TestResolveRejectsMismatchedFactorParams(t *testing.T) {
	resolve := func(raw string) error {
		_, _, err := Service{Client: tunedClient(), Sets: &tasksFake{}}.ResolveInstance(context.Background(), "i1", "space", "ftask_i1", parseStrategy(t, raw))
		return err
	}
	if err := resolve(strings.Replace(tunedDSL, "window: 20", "period: 20", 1)); err == nil || !strings.Contains(err.Error(), "factor_params.bias__var.period") || !strings.Contains(err.Error(), "window") {
		t.Fatalf("应指出没有这个参数并列出可调参数：%v", err)
	}
	if err := resolve(strings.Replace(tunedDSL, "bias__var:\n    window", "ma__var:\n    window", 1)); err == nil || !strings.Contains(err.Error(), "成员 ma__var 不是关联任务的可变成员") {
		t.Fatalf("任务里没有的可变成员不能写 factor_params：%v", err)
	}
	if err := resolve(strings.Replace(tunedDSL, "factor_params:\n  bias__var:\n    window: 20\n", "", 1)); err == nil || !strings.Contains(err.Error(), "bias__var 在 factor_params 里没有输出") {
		t.Fatalf("可变成员没有输出应拒绝：%v", err)
	}
	withParams := strings.Replace(tunedDSL, "select: {top: 1}", `select: {top: 1}
    filter: params("ma__var", "window") > 0`, 1)
	if err := resolve(withParams); err == nil || !strings.Contains(err.Error(), `params("ma__var", "window")`) {
		t.Fatalf("params() 读任务里没有的可变成员应拒绝：%v", err)
	}
}

// params() 读取的可变成员也要固化成员契约；没有可变成员的任务不需要 factor_params。
func TestResolveParamsFunctionBindsMemberContract(t *testing.T) {
	strategy := parseStrategy(t, strings.Replace(tunedDSL, "select: {top: 1}", `select: {top: 1}
    filter: params("bias__var", "window") > 0`, 1))
	resolved, program, err := Service{Client: tunedClient(), Sets: &tasksFake{}}.ResolveInstance(context.Background(), "i1", "space", "ftask_i1", strategy)
	if err != nil {
		t.Fatal(err)
	}
	if len(program.ParamRefs) != 1 || resolved.Factors["bias__var"] != readiness.Contract("sha256:bias", "var") {
		t.Fatalf("params() 引用不符：%+v %v", program.ParamRefs, resolved.Factors)
	}
	// 没有可变成员的任务：普通策略直接解析。
	if _, _, err := (Service{Client: newFakeClient("spot"), Sets: &tasksFake{}}).ResolveInstance(context.Background(), "i1", "space", "ftask_1h", parseStrategy(t, exampleDSL)); err != nil {
		t.Fatalf("没有可变成员的任务应直接解析：%v", err)
	}
}

// 任务必须已启用，且关联的就是本实例；缺依赖时不能启用。
func TestResolveInstanceChecksTaskBinding(t *testing.T) {
	strategy := parseStrategy(t, tunedDSL)
	other := &tasksFake{bound: BoundTask{TaskID: "ftask_i1", Status: "enabled", BoundInstanceID: "i2"}}
	if _, _, err := (Service{Client: tunedClient(), Sets: other}).ResolveInstance(context.Background(), "i1", "space", "ftask_i1", strategy); err == nil || !strings.Contains(err.Error(), "关联的是实例") {
		t.Fatalf("任务关联了别的实例应拒绝：%v", err)
	}
	disabled := &tasksFake{bound: BoundTask{TaskID: "ftask_i1", Status: "disabled", BoundInstanceID: "i1"}}
	if _, _, err := (Service{Client: tunedClient(), Sets: disabled}).ResolveInstance(context.Background(), "i1", "space", "ftask_i1", strategy); err == nil || !strings.Contains(err.Error(), "已启用的任务") {
		t.Fatalf("任务未启用应拒绝：%v", err)
	}
	if _, _, err := (Service{Client: tunedClient()}).ResolveInstance(context.Background(), "i1", "space", "ftask_i1", strategy); err == nil {
		t.Fatal("没有接线 Factor 时不能启用实例")
	}
	if _, _, err := (Service{Client: tunedClient(), Sets: &tasksFake{err: context.DeadlineExceeded}}).ResolveInstance(context.Background(), "i1", "space", "ftask_i1", strategy); err == nil {
		t.Fatal("关联失败应拒绝")
	}
}

// 结果 View 的成员列由 Storage 异步补齐：补齐之前重试，补齐后解析成功；等待超时则报最后一次的缺列错误。
func TestResolveInstanceWaitsForMemberColumns(t *testing.T) {
	late := &lateColumns{fakeClient: tunedClient(), after: 3}
	service := Service{Client: late, Sets: &tasksFake{}, ReadyWait: 2 * time.Second, ReadyStep: time.Millisecond}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", "ftask_i1", parseStrategy(t, tunedDSL)); err != nil {
		t.Fatalf("补齐后应解析成功：%v", err)
	}
	if late.reads <= 3 {
		t.Fatalf("应重试到列补齐：reads=%d", late.reads)
	}
	never := &lateColumns{fakeClient: tunedClient(), after: 1 << 30}
	service = Service{Client: never, Sets: &tasksFake{}, ReadyWait: 20 * time.Millisecond, ReadyStep: 5 * time.Millisecond}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", "ftask_i1", parseStrategy(t, tunedDSL)); err == nil || !strings.Contains(err.Error(), "bias__var") {
		t.Fatalf("超时应报缺失的列：%v", err)
	}
}
