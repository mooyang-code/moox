package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/verify"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
)

// setupVerifyPoll 是验收轮询的间隔：K 线按分钟到，不用查得更勤。
const setupVerifyPoll = 10 * time.Second

func newSetupVerifyCommand(deps setupDeps) *cobra.Command {
	var file, spaceID string
	var timeout time.Duration
	var options unitdeploy.DeployOptions
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "验收一个空间：K 线入库、因子前进、策略在运行才算通过，只读",
		Long: `按已启用的任务逐项检查，等到通过或超时。开头没有计划、第一根 K 线还没收盘时算等待，到超时才失败。
K 线缺失不超过 5% 算通过：到截止时间就带缺口结算，缺口靠之后补采。因子预热期状态是 degraded，也算通过。
没有部署因子引擎、没有启用的策略实例时，对应的检查跳过。空间没有任何 K 线任务，或任务都停用（还没运行 setup start）时直接失败。
休市时 K 线检查跳过，因子只看最近一期没有失败。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(spaceID) == "" {
				return errors.New("setup verify 要带 --space-id")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := resolveSetupUnitOptions(snapshot, "", &options); err != nil {
				return err
			}
			checks, err := runSetupVerifySpace(cmd.Context(), file, snapshot, options, spaceID, timeout)
			if err != nil && checks == nil {
				return err
			}
			if writeErr := writeSetupJSON(cmd, map[string]any{"space_id": spaceID, "checks": checks}); writeErr != nil {
				return writeErr
			}
			return err
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&spaceID, "space-id", "", "要验收的空间")
	cmd.Flags().DurationVar(&timeout, "timeout", 6*time.Minute, "最长等多久")
	cmd.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录（默认和 bootstrap 相同）")
	cmd.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return cmd
}

// runSetupVerifySpace 验收一个空间，通过时写记录。检查项有失败时同时返回 checks 和错误。
func runSetupVerifySpace(ctx context.Context, file string, snapshot *setupconfig.Snapshot, options unitdeploy.DeployOptions, spaceID string, timeout time.Duration) ([]verify.Check, error) {
	gateway, err := openCommandGateway(ctx, file, snapshot)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	reader := setupVerifyReader{gateway: gateway, spaceID: spaceID, factorDeployed: len(snapshot.Manifest.ComponentHost("factor-engine")) != 0}
	checks, err := runSetupVerify(ctx, reader, timeout, setupVerifyPoll)
	if err != nil {
		return nil, err
	}
	for _, check := range checks {
		if check.Status == verify.Fail && check.Code == verify.CodeMonitorUnhealthy {
			return checks, fmt.Errorf("%s: %s。先看 moox-cli doctor alerts；远程健康检查超时多半是端口没放行，运行 moox-cli setup firewall --yes", check.Code, check.Detail)
		}
		if check.Status == verify.Fail {
			return checks, fmt.Errorf("%s: %s。先看 moox-cli collector status --space-id %s，再用 setup logs 查对应组件", check.Code, check.Detail, spaceID)
		}
	}
	return checks, unitdeploy.MarkStep(options.StateDirectory, unitdeploy.VerifiedStep(spaceID))
}

// setupVerifySource 读一轮验收的数据。baseline 是本次验收开始时各因子任务的最后一期。
type setupVerifySource interface {
	read(ctx context.Context, baseline map[string]int64) (verify.Input, error)
}

func runSetupVerify(ctx context.Context, source setupVerifySource, timeout, poll time.Duration) ([]verify.Check, error) {
	deadline := time.Now().Add(timeout)
	baseline := map[string]int64{}
	started := false
	for {
		input, err := source.read(ctx, baseline)
		var checks []verify.Check
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, ctx.Err()
		case err != nil:
			// 组件可能还在启动或瞬断，和数据没到一样等到超时。
			checks = []verify.Check{{Name: "read", Status: verify.Wait, Code: verify.CodeReadFailed, Detail: err.Error()}}
		case !started:
			// 第一次读成功才有基准，之后才能判断因子有没有前进。
			for _, factor := range input.Factors {
				baseline[factor.TaskID] = factor.LastPeriod
			}
			started = true
			continue
		default:
			checks = verify.Evaluate(input)
		}
		if verify.Settled(checks) {
			return checks, nil
		}
		if !time.Now().Before(deadline) {
			return verify.Finish(checks), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// setupVerifyGateway 同时用于直接调用和带空间头的 JSON 调用。
type setupVerifyGateway interface {
	gatewayclient.Invoker
	adminclient.GatewayForwarder
}

type setupVerifyReader struct {
	gateway        setupVerifyGateway
	spaceID        string
	factorDeployed bool
}

func (r setupVerifyReader) read(ctx context.Context, baseline map[string]int64) (verify.Input, error) {
	now := time.Now().UTC()
	input := verify.Input{Now: now, FactorDeployed: r.factorDeployed}
	enabled := true
	tasks := &collectorpb.GetTaskListRsp{}
	if err := r.gateway.Invoke(ctx, collectMgr, "GetTaskList", &collectorpb.GetTaskListReq{SpaceId: r.spaceID, DataType: "kline", Enabled: &enabled}, tasks); err != nil {
		return input, fmt.Errorf("read collector tasks: %w", err)
	}
	if err := collectorRetError("read collector tasks", tasks.GetRetInfo()); err != nil {
		return input, err
	}
	if len(tasks.GetTasks()) == 0 {
		all := &collectorpb.GetTaskListRsp{}
		if err := r.gateway.Invoke(ctx, collectMgr, "GetTaskList", &collectorpb.GetTaskListReq{SpaceId: r.spaceID, DataType: "kline"}, all); err != nil {
			return input, fmt.Errorf("read collector tasks: %w", err)
		}
		if err := collectorRetError("read collector tasks", all.GetRetInfo()); err != nil {
			return input, err
		}
		input.TasksDisabled = len(all.GetTasks()) != 0
	}
	status := &collectorpb.GetCollectorStatusRsp{}
	if err := r.gateway.Invoke(ctx, collectMgr, "GetCollectorStatus", &collectorpb.GetCollectorStatusReq{SpaceId: r.spaceID}, status); err != nil {
		return input, fmt.Errorf("read collector status: %w", err)
	}
	if err := collectorRetError("read collector status", status.GetRetInfo()); err != nil {
		return input, err
	}
	input.HasPlan = status.GetOpen() != nil || status.GetApplied() != nil
	input.PlanIssue = strings.Join(status.GetReasons(), "；")
	input.StreamOnline = status.GetStream().GetOnline()
	for _, mode := range status.GetIngestModes() {
		if mode.GetMode() == "stream" || mode.GetExecutor() == "local" {
			input.NeedsStream = true
		}
	}
	registered := map[string]*collectorpb.CollectorDatasetStatus{}
	for _, dataset := range status.GetDatasets() {
		registered[dataset.GetDatasetId()] = dataset
	}
	sources := map[string]bool{}
	from, to := now.Truncate(time.Minute).Add(-3*time.Minute).Format(time.RFC3339), now.Format(time.RFC3339)
	for _, task := range tasks.GetTasks() {
		id := task.GetResult().GetDatasetId()
		sources[id] = true
		dataset := verify.Dataset{ID: id}
		if entry := registered[id]; entry != nil {
			dataset.Registered, dataset.Frequency = true, entry.GetFrequency()
			dataset.EffectiveFrom, _ = time.Parse(time.RFC3339, entry.GetEffectiveFrom())
			check := &collectorpb.CheckKlineRsp{}
			if err := r.gateway.Invoke(ctx, collectMgr, "CheckKline", &collectorpb.CheckKlineReq{SpaceId: r.spaceID, DatasetId: id, From: from, To: to}, check); err != nil {
				return input, fmt.Errorf("check kline: %w", err)
			}
			if err := collectorRetError("check kline", check.GetRetInfo()); err != nil {
				return input, err
			}
			dataset.Expected, dataset.Missing = int(check.GetExpected()), int(check.GetMissing())
		}
		input.Datasets = append(input.Datasets, dataset)
	}
	if r.factorDeployed {
		rsp := &factorpb.ListFactorTasksRsp{}
		if err := r.gateway.Invoke(ctx, factorMgr, "ListFactorTasks", &factorpb.ListFactorTasksReq{Status: "enabled"}, rsp); err != nil {
			return input, fmt.Errorf("read factor tasks: %w", err)
		}
		if ret := rsp.GetRetInfo(); ret.GetCode() != 0 {
			return input, fmt.Errorf("read factor tasks: %s (code %d)", ret.GetMsg(), ret.GetCode())
		}
		for _, info := range rsp.GetFactorTasks() {
			task := info.GetFactorTask()
			if !strings.EqualFold(task.GetSpaceId(), r.spaceID) {
				continue
			}
			input.Factors = append(input.Factors, verify.Factor{
				TaskID: task.GetTaskId(), Frequency: task.GetFreq(), LastPeriod: info.GetLastRun().GetLastPeriodTime(), Baseline: baseline[task.GetTaskId()],
				LastStatus: info.GetLastRun().GetLastStatus(), SourceEnabled: sources[task.GetSourceDatasetId()],
			})
		}
	}
	instances, err := readStrategyInstances(ctx, r.gateway, r.spaceID)
	if err != nil {
		return input, err
	}
	input.Instances = instances
	if input.CriticalAlerts, err = readCriticalAlerts(ctx, r.gateway); err != nil {
		return input, err
	}
	return input, nil
}

// readCriticalAlerts 读全部空间：防火墙、组件这些问题不属于某个空间。
func readCriticalAlerts(ctx context.Context, gateway setupVerifyGateway) ([]string, error) {
	overview, err := doctorcli.New(gateway).GetHealthOverview(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("read monitor overview: %w", err)
	}
	var alerts []string
	for _, item := range doctorcli.BuildAlertsReport(overview).Items {
		if item.Severity != doctorcli.AlertSeverityCritical {
			continue
		}
		parts := slices.DeleteFunc([]string{item.Object, item.Title, item.Reason}, func(text string) bool { return text == "" })
		alerts = append(alerts, strings.Join(parts, " "))
	}
	return alerts, nil
}

// readStrategyInstances 读空间的策略实例。CLI 不依赖策略模块的协议包，这里按 JSON 取需要的字段。
func readStrategyInstances(ctx context.Context, gateway adminclient.GatewayForwarder, spaceID string) ([]verify.Instance, error) {
	var rsp json.RawMessage
	client := &adminclient.Client{Gateway: gateway, SpaceID: spaceID}
	if err := client.CallGatewayJSON(ctx, strategyMgr, "ListStrategyInstances", map[string]any{"space_id": spaceID, "page": map[string]any{"page": 1, "page_size": 100}}, &rsp); err != nil {
		return nil, fmt.Errorf("read strategy instances: %w", err)
	}
	var parsed struct {
		RetInfo struct {
			Code json.RawMessage `json:"code"`
			Msg  string          `json:"msg"`
		} `json:"ret_info"`
		Instances []struct {
			InstanceID string `json:"instance_id"`
			Enabled    bool   `json:"enabled"`
			SessionID  string `json:"session_id"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(rsp, &parsed); err != nil {
		return nil, fmt.Errorf("read strategy instances: %w", err)
	}
	// JSON 里枚举可能是数字或名字，成功时也可能省略。
	if code := strings.Trim(string(parsed.RetInfo.Code), `"`); code != "" && code != "0" && code != "SUCCESS" {
		return nil, fmt.Errorf("read strategy instances: %s (code %s)", parsed.RetInfo.Msg, code)
	}
	instances := make([]verify.Instance, 0, len(parsed.Instances))
	for _, item := range parsed.Instances {
		instances = append(instances, verify.Instance{ID: item.InstanceID, Enabled: item.Enabled, SessionID: item.SessionID})
	}
	slices.SortFunc(instances, func(a, b verify.Instance) int { return strings.Compare(a.ID, b.ID) })
	return instances, nil
}
