package command

import (
	"context"
	"errors"
	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/verify"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type fakeVerifySource struct {
	reads int
	ready int
}

func (s *fakeVerifySource) read(_ context.Context, baseline map[string]int64) (verify.Input, error) {
	s.reads++
	in := verify.Input{Now: time.Now(), HasPlan: s.reads >= s.ready}
	in.Datasets = []verify.Dataset{{ID: "d", Registered: s.reads >= s.ready, Frequency: "1m", Expected: 10}}
	in.Factors = []verify.Factor{{TaskID: "f", Frequency: "1m", LastPeriod: int64(s.reads), Baseline: baseline["f"], SourceEnabled: true}}
	in.FactorDeployed = true
	return in, nil
}

func TestRunSetupVerifyWaitsUntilDataArrives(t *testing.T) {
	source := &fakeVerifySource{ready: 3}
	checks, err := runSetupVerify(context.Background(), source, time.Second, time.Millisecond)
	require.NoError(t, err)
	require.True(t, verify.Settled(checks))
	require.Equal(t, verify.Pass, checks[1].Status)
	require.Equal(t, verify.Pass, checks[2].Status)
}

func TestRunSetupVerifyFailsAfterTimeout(t *testing.T) {
	source := &fakeVerifySource{ready: 1 << 30}
	checks, err := runSetupVerify(context.Background(), source, 5*time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, verify.Fail, checks[0].Status)
}

type flakyVerifySource struct {
	fakeVerifySource
	failures int
}

func (s *flakyVerifySource) read(ctx context.Context, baseline map[string]int64) (verify.Input, error) {
	if s.failures > 0 {
		s.failures--
		return verify.Input{}, errors.New("connection reset")
	}
	return s.fakeVerifySource.read(ctx, baseline)
}

type alertingVerifySource struct {
	fakeVerifySource
	alerts []string
}

func (s *alertingVerifySource) read(ctx context.Context, baseline map[string]int64) (verify.Input, error) {
	in, err := s.fakeVerifySource.read(ctx, baseline)
	in.CriticalAlerts = s.alerts
	return in, err
}

// 数据都到了但 Monitor 还有 critical 告警时，不能算装好。
func TestRunSetupVerifyFailsWhenMonitorKeepsCriticalAlerts(t *testing.T) {
	source := &alertingVerifySource{alerts: []string{"storage host-agent 健康检查超时"}}
	checks, err := runSetupVerify(context.Background(), source, 5*time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	last := checks[len(checks)-1]
	require.Equal(t, "monitor", last.Name)
	require.Equal(t, verify.Fail, last.Status)
	require.Equal(t, verify.CodeMonitorUnhealthy, last.Code)
	require.Contains(t, last.Detail, "host-agent")
}

type flappingVerifySource struct {
	alertingVerifySource
	reads int
}

// 第一轮之后交替出现告警：碰巧干净的一轮不能算通过。
func (s *flappingVerifySource) read(ctx context.Context, baseline map[string]int64) (verify.Input, error) {
	s.reads++
	s.alerts = nil
	if s.reads%2 == 0 {
		s.alerts = []string{"storage host-agent 反复退出"}
	}
	return s.alertingVerifySource.read(ctx, baseline)
}

func TestRunSetupVerifyRequiresConsecutivePasses(t *testing.T) {
	source := &flappingVerifySource{}
	checks, err := runSetupVerify(context.Background(), source, 20*time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	require.False(t, verify.Settled(checks) && checks[len(checks)-1].Status == verify.Pass && source.reads < 4)
	require.Greater(t, source.reads, 4)
}

func TestCriticalAlertLinesCountEachComponentOnce(t *testing.T) {
	lines := criticalAlertLines([]doctorcli.AlertItem{
		{Source: doctorcli.AlertSourceAlert, Severity: doctorcli.AlertSeverityCritical, Object: "component/storage/host-agent", Title: "健康检查超时"},
		{Source: doctorcli.AlertSourceComponent, Severity: doctorcli.AlertSeverityCritical, Object: "storage/host-agent", Title: "storage / host-agent"},
		{Source: doctorcli.AlertSourceComponent, Severity: doctorcli.AlertSeverityWarning, Object: "storage/access", Title: "storage / access"},
		{Source: doctorcli.AlertSourceHost, Severity: doctorcli.AlertSeverityCritical, Object: "overseas", Title: "主机 overseas"},
	})
	require.Equal(t, []string{"storage/host-agent storage / host-agent", "overseas 主机 overseas"}, lines)
}

func TestSetupVerifyMonitorErrorKeepsOtherChecks(t *testing.T) {
	checks := verify.Evaluate(verify.Input{Now: time.Now(), MonitorError: "connection refused"})
	last := checks[len(checks)-1]
	require.Equal(t, verify.Wait, last.Status)
	require.Equal(t, verify.CodeMonitorUnreadable, last.Code)
	require.Contains(t, last.Detail, "读不到 Monitor")
}

func TestSetupVerifyListsOnlyTheFirstAlerts(t *testing.T) {
	alerts := []string{"a", "b", "c", "d", "e", "f", "g"}
	checks := verify.Evaluate(verify.Input{Now: time.Now(), CriticalAlerts: alerts})
	detail := checks[len(checks)-1].Detail
	require.Contains(t, detail, "共 7 项")
	require.NotContains(t, detail, "f")
}

func TestRunSetupVerifySurvivesTransientReadErrors(t *testing.T) {
	source := &flakyVerifySource{fakeVerifySource: fakeVerifySource{ready: 1}, failures: 2}
	checks, err := runSetupVerify(context.Background(), source, time.Second, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, verify.Pass, checks[2].Status, "基准要在第一次读成功时取")
	source = &flakyVerifySource{failures: 1 << 30}
	checks, err = runSetupVerify(context.Background(), source, 5*time.Millisecond, time.Millisecond)
	require.NoError(t, err)
	require.Equal(t, verify.CodeReadFailed, checks[0].Code)
	require.Equal(t, verify.Fail, checks[0].Status)
}

// fakeVerifyGateway 按线上的形状回答：视图 ID 带类型和频率，数据集 ID 不带。
type fakeVerifyGateway struct {
	strategy      string
	factors       *factorpb.ListFactorTasksRsp
	tasksDisabled bool
}

func (g fakeVerifyGateway) Invoke(_ context.Context, _, method string, req, rsp any) error {
	switch out := rsp.(type) {
	case *collectorpb.GetTaskListRsp:
		if g.tasksDisabled && req.(*collectorpb.GetTaskListReq).Enabled != nil {
			return nil
		}
		out.Tasks = []*collectorpb.CollectionTask{{TaskId: "task_a", Result: &collectorpb.TaskResult{ViewId: "view_task_a_kline_1m", DatasetId: "dataset_task_a"}}}
	case *collectorpb.GetCollectorStatusRsp:
		out.Datasets = []*collectorpb.CollectorDatasetStatus{{DatasetId: "dataset_task_a", Frequency: "1m"}}
		out.Applied = &collectorpb.FetchPlanSummary{}
	case *collectorpb.CheckKlineRsp:
		out.Expected, out.Missing = 3, 0
	case *factorpb.ListFactorTasksRsp:
		proto.Merge(out, g.factors)
	case *monitorpb.GetHealthOverviewRsp:
		out.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS}
		out.Overview = &monitorpb.HealthOverview{}
	default:
		return errors.New("unexpected " + method)
	}
	return nil
}

func (g fakeVerifyGateway) Forward(context.Context, string, string, int, []byte) ([]byte, error) {
	return []byte(g.strategy), nil
}

func TestSetupVerifyReaderMatchesDatasetsByTaskResult(t *testing.T) {
	factors := &factorpb.ListFactorTasksRsp{FactorTasks: []*factorpb.FactorTaskInfo{{FactorTask: &factorpb.FactorTask{TaskId: "f", SpaceId: "crypto", Freq: "1m", SourceDatasetId: "dataset_task_a"}}}}
	reader := setupVerifyReader{gateway: fakeVerifyGateway{strategy: `{"instances":[]}`, factors: factors}, spaceID: "crypto", factorDeployed: true}
	input, err := reader.read(context.Background(), map[string]int64{})
	require.NoError(t, err)
	require.True(t, input.Datasets[0].Registered, "数据集要按任务结果对上")
	require.Equal(t, 3, input.Datasets[0].Expected)
	require.True(t, input.Factors[0].SourceEnabled)

	reader.gateway = fakeVerifyGateway{strategy: `{"instances":[]}`, factors: factors, tasksDisabled: true}
	input, err = reader.read(context.Background(), map[string]int64{})
	require.NoError(t, err)
	require.True(t, input.TasksDisabled, "有任务但全停用")

	reader.gateway = fakeVerifyGateway{strategy: `{"ret_info":{"code":"INNER_ERR","msg":"boom"}}`, factors: factors}
	_, err = reader.read(context.Background(), map[string]int64{})
	require.ErrorContains(t, err, "boom", "策略后端报错不能当作没有实例")

	factors.RetInfo = &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: "factor down"}
	_, err = reader.read(context.Background(), map[string]int64{})
	require.ErrorContains(t, err, "factor down", "因子后端报错不能当作没有任务")
}
