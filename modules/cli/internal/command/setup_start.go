package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

// spaceExample 是 setup start 给一个空间创建的默认示例：观察实例（不挂账户）、它读的因子任务和策略文件。用固定 ID，重复运行能识别。
type spaceExample struct {
	StrategyID string
	InstanceID string
	FactorTask string
	DSL        string
}

// spaceExamples 按空间登记默认示例，没有登记的空间不建示例策略。
var spaceExamples = map[string]spaceExample{
	"crypto":  {StrategyID: "default_dual_ma_swap_1m", InstanceID: "default_dual_ma_swap_1m", FactorTask: "ftask_swap_1m", DSL: "examples/strategies/dual_ma_swap_1m.yaml"},
	"stockcn": {StrategyID: "default_dual_ma_cn_5m", InstanceID: "default_dual_ma_cn_5m", FactorTask: "ftask_cn_5m", DSL: "examples/strategies/dual_ma_cn_5m.yaml"},
}

const (
	// streamSpace 是走 collector-stream（WS 推送）的空间；其他空间只能走云函数。
	streamSpace           = "crypto"
	setupStartStreamWait  = 90 * time.Second
	setupStartPlanWait    = 90 * time.Second
	setupStartSubjectWait = 60 * time.Second
	setupStartPollDefault = 3 * time.Second
)

// setupStartSummary 是 setup start 的结果，跳过的内容写在 Notes 里交给用户。
type setupStartSummary struct {
	Status       string   `json:"status"`
	SpaceID      string   `json:"space_id"`
	TasksEnabled int      `json:"tasks_enabled"`
	Plan         string   `json:"plan"`
	Strategy     string   `json:"strategy"`
	Notes        []string `json:"notes,omitempty"`
}

// setupStartClient 是 setup start 要用的线上操作。
type setupStartClient interface {
	Tasks(ctx context.Context, spaceID string) ([]adminclient.CollectionTaskSummary, error)
	EnableTask(ctx context.Context, spaceID, taskID string) error
	// SyncSubjects 立即同步一个自动标签，返回同步后的成员数；Collector 正在跑别的同步时返回 errSubjectSyncBusy。
	SyncSubjects(ctx context.Context, spaceID, tagID string) (int, error)
	StreamOnline(ctx context.Context, spaceID string) (bool, error)
	Trigger(ctx context.Context, spaceID string) error
	PlanState(ctx context.Context, spaceID string) (bool, string, error)
	Instances(ctx context.Context, spaceID string) ([]setupStartInstance, error)
	EnsureStrategy(ctx context.Context, spaceID, strategyID, dsl string) error
	CreateInstance(ctx context.Context, instance setupStartInstance) error
	EnableInstance(ctx context.Context, spaceID, instanceID string) error
}

// setupStartInstance 是策略实例里 setup start 关心的字段。
type setupStartInstance struct {
	InstanceID   string `json:"instance_id"`
	StrategyID   string `json:"strategy_id"`
	SpaceID      string `json:"space_id"`
	FactorTaskID string `json:"factor_task_id"`
	Enabled      bool   `json:"enabled"`
}

func newSetupStartCommand(deps setupDeps) *cobra.Command {
	var file, spaceID string
	var options unitdeploy.DeployOptions
	cmd := &cobra.Command{
		Use:   "start",
		Short: "启动一个空间：启用采集任务，导入因子，创建默认示例策略",
		Long:  "crypto 走 collector-stream 和出口代理，其他空间要配 [scf_plan]。重复运行不会重复创建。没有因子机时跳过因子和默认策略。",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if strings.TrimSpace(spaceID) == "" {
				return errors.New("setup start 要带 --space-id")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if err := resolveSetupUnitOptions(snapshot, "", &options); err != nil {
				return err
			}
			if err := setupStartPrerequisites(snapshot.Manifest, spaceID); err != nil {
				return err
			}
			client, closeClient, err := deps.openStart(cmd.Context(), file, snapshot)
			if err != nil {
				return err
			}
			defer closeClient()
			summary, err := runSetupStart(cmd.Context(), deps, client, snapshot, filepath.Dir(file), options.RepositoryRoot, spaceID, setupStartPollDefault)
			if err != nil {
				return err
			}
			if err := unitdeploy.MarkStep(options.StateDirectory, unitdeploy.StartedStep(spaceID)); err != nil {
				return err
			}
			return writeSetupJSON(cmd, summary)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&spaceID, "space-id", "", "要启动的空间，比如 crypto")
	cmd.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录（默认和 bootstrap 相同）")
	cmd.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return cmd
}

// setupStartPrerequisites 只查这个空间需要的东西：走 collector-stream 的空间要海外机，其他空间只能走云函数。
func setupStartPrerequisites(manifest setupconfig.Manifest, spaceID string) error {
	if spaceID == streamSpace {
		if manifest.ComponentHost("collector-stream") == "" || manifest.ComponentHost("egress-proxy") == "" {
			return errors.New("start_prerequisite_missing: crypto 需要海外机上的 collector-stream 和出口代理，运行 setup validate 看拓扑")
		}
		return nil
	}
	if manifest.SCFEnabled() && slices.ContainsFunc(manifest.SCFPlan.Spaces, func(space setupconfig.SCFPlanSpace) bool { return space.SpaceID == spaceID }) {
		return nil
	}
	return fmt.Errorf("start_prerequisite_missing: %s 目前只能走云函数，要在 [scf_plan] 里配置这个空间", spaceID)
}

func runSetupStart(ctx context.Context, deps setupDeps, client setupStartClient, snapshot *setupconfig.Snapshot, configRoot, repository, spaceID string, poll time.Duration) (setupStartSummary, error) {
	summary := setupStartSummary{Status: "started", SpaceID: spaceID, Strategy: "skipped"}
	manifest := snapshot.Manifest
	tasks, err := client.Tasks(ctx, spaceID)
	if err != nil {
		return summary, err
	}
	if len(tasks) == 0 {
		return summary, fmt.Errorf("no_collection_tasks: 空间 %s 没有 K 线任务", spaceID)
	}
	if spaceID == streamSpace {
		// 触发时 collector-stream 不在线，这一轮只会记下原因，要等到下一分钟。
		if err := setupStartWaitStream(ctx, client, spaceID, poll); err != nil {
			return summary, err
		}
	}
	for _, task := range tasks {
		if task.Enabled {
			continue
		}
		if err := setupStartEnableTask(ctx, client, spaceID, task, poll); err != nil {
			return summary, err
		}
		summary.TasksEnabled++
	}
	// 全部启用后才触发一次：逐个触发会把任务拆成两份计划，第二份还要等第一份生效。
	if err := client.Trigger(ctx, spaceID); err != nil {
		return summary, err
	}
	summary.Plan = setupStartWaitPlan(ctx, client, spaceID, poll)
	if manifest.ComponentHost("factor-engine") == "" {
		summary.Notes = append(summary.Notes, "没有部署因子引擎，跳过因子和默认策略")
		return summary, nil
	}
	imported, err := setupStartFactors(ctx, deps, snapshot, configRoot, spaceID)
	if err != nil {
		return summary, err
	}
	if !imported {
		summary.Notes = append(summary.Notes, "配置里没有这个空间的因子任务，或 [factors] 没有打开，跳过因子和默认策略")
		return summary, nil
	}
	example, ok := spaceExamples[spaceID]
	if !ok || !slices.ContainsFunc(manifest.Factors.Tasks, func(task setupconfig.FactorSetupTask) bool { return task.ID == example.FactorTask }) {
		return summary, nil
	}
	summary.Strategy, err = setupStartStrategy(ctx, client, repository, spaceID, example, &summary)
	return summary, err
}

// errSubjectSyncBusy 表示 Collector 正在跑别的标的同步，稍后重试即可。
var errSubjectSyncBusy = errors.New("collector 正在同步标的")

// setupStartEnableTask 启用任务。全新部署时任务绑定的自动标签可能还没同步过（如 A 股的沪深 300 成分标签要到次日 08:40
// 才定时同步），控制面会以“没有有效标的”拒绝启用：这时立即同步任务绑定的标签再重试；标的刚同步完、标签暂时解析不出成员也等一会儿再试。
// 其他错误原样返回。
func setupStartEnableTask(ctx context.Context, client setupStartClient, spaceID string, task adminclient.CollectionTaskSummary, poll time.Duration) error {
	deadline := time.Now().Add(setupStartSubjectWait)
	synced := false
	for {
		err := client.EnableTask(ctx, spaceID, task.TaskID)
		if err == nil || !strings.Contains(err.Error(), "没有有效标的") || !time.Now().Before(deadline) {
			return err
		}
		if !synced {
			done, syncErr := setupStartSyncTags(ctx, client, spaceID, task.TagIDs)
			if syncErr != nil {
				return fmt.Errorf("任务 %s 的标签同步失败：%w", task.TaskID, syncErr)
			}
			synced = done
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// setupStartSyncTags 逐个同步任务绑定的标签：Collector 正忙时返回 false 让调用方稍后再来；同步成功但没有成员时报错。
func setupStartSyncTags(ctx context.Context, client setupStartClient, spaceID string, tagIDs []string) (bool, error) {
	for _, tagID := range tagIDs {
		subjects, err := client.SyncSubjects(ctx, spaceID, tagID)
		if errors.Is(err, errSubjectSyncBusy) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if subjects == 0 {
			return false, fmt.Errorf("标签 %s 同步后没有成员", tagID)
		}
	}
	return true, nil
}

func setupStartWaitStream(ctx context.Context, client setupStartClient, spaceID string, poll time.Duration) error {
	deadline := time.Now().Add(setupStartStreamWait)
	for {
		online, err := client.StreamOnline(ctx, spaceID)
		if err == nil && online {
			return nil
		}
		if !time.Now().Before(deadline) {
			return errors.New("collection_not_started: collector-stream 不在线，用 setup logs --component collector-stream 查原因")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// setupStartWaitPlan 等计划建出来；只是报告，真正的验收在 setup verify。
func setupStartWaitPlan(ctx context.Context, client setupStartClient, spaceID string, poll time.Duration) string {
	deadline := time.Now().Add(setupStartPlanWait)
	reason := ""
	for {
		ready, detail, err := client.PlanState(ctx, spaceID)
		if err == nil && ready {
			return "ready"
		}
		if detail != "" {
			reason = detail
		}
		if !time.Now().Before(deadline) {
			return strings.TrimSpace("waiting " + reason)
		}
		select {
		case <-ctx.Done():
			return "waiting"
		case <-time.After(poll):
		}
	}
}

// setupStartFactors 导入这个空间的因子任务，定义是全局的，一起导入。
func setupStartFactors(ctx context.Context, deps setupDeps, snapshot *setupconfig.Snapshot, configRoot, spaceID string) (bool, error) {
	plan, err := loadSetupFactors(snapshot.Manifest, configRoot)
	if err != nil {
		return false, err
	}
	plan.Tasks = slices.DeleteFunc(plan.Tasks, func(task setupFactorTask) bool { return task.SpaceID != spaceID })
	reportFactorWarnings(plan.Warnings)
	if len(plan.Tasks) == 0 {
		return false, nil
	}
	service, err := deps.openInitFactor(ctx, snapshot)
	if err != nil {
		return false, err
	}
	_, applyErr := service.Apply(ctx, plan)
	return true, errors.Join(applyErr, service.Close())
}

// setupStartStrategy 创建空间的默认示例并启用。用户已经把这个因子任务绑给别的实例时跳过：一个因子任务只能有一个实例。
func setupStartStrategy(ctx context.Context, client setupStartClient, repository, spaceID string, example spaceExample, summary *setupStartSummary) (string, error) {
	instances, err := client.Instances(ctx, spaceID)
	if err != nil {
		return "", err
	}
	var existing *setupStartInstance
	for i, instance := range instances {
		switch {
		case instance.InstanceID == example.InstanceID:
			existing = &instances[i]
		case instance.FactorTaskID == example.FactorTask:
			summary.Notes = append(summary.Notes, fmt.Sprintf("因子任务 %s 已绑定实例 %s，跳过默认示例", example.FactorTask, instance.InstanceID))
			return "skipped", nil
		}
	}
	if existing != nil {
		if existing.Enabled {
			return "unchanged", nil
		}
		return "enabled", client.EnableInstance(ctx, spaceID, example.InstanceID)
	}
	dsl, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(example.DSL)))
	if err != nil {
		return "", fmt.Errorf("读取默认策略: %w", err)
	}
	if err := client.EnsureStrategy(ctx, spaceID, example.StrategyID, string(dsl)); err != nil {
		return "", err
	}
	return "created", client.CreateInstance(ctx, setupStartInstance{InstanceID: example.InstanceID, StrategyID: example.StrategyID, SpaceID: spaceID, FactorTaskID: example.FactorTask, Enabled: true})
}

// gatewayStartClient 是 setupStartClient 的线上实现。
type gatewayStartClient struct{ gateway setupVerifyGateway }

func defaultSetupOpenStart(ctx context.Context, file string, snapshot *setupconfig.Snapshot) (setupStartClient, func(), error) {
	gateway, err := openCommandGateway(ctx, file, snapshot)
	if err != nil {
		return nil, nil, err
	}
	return gatewayStartClient{gateway}, func() { gateway.Close() }, nil
}

func (c gatewayStartClient) admin(spaceID string) *adminclient.Client {
	return &adminclient.Client{Gateway: c.gateway, SpaceID: spaceID}
}

func (c gatewayStartClient) Tasks(ctx context.Context, spaceID string) ([]adminclient.CollectionTaskSummary, error) {
	return c.admin(spaceID).ListTasks(ctx, spaceID, "kline", nil)
}

func (c gatewayStartClient) EnableTask(ctx context.Context, spaceID, taskID string) error {
	return c.admin(spaceID).EnableCollectionTask(ctx, spaceID, taskID)
}

func (c gatewayStartClient) SyncSubjects(ctx context.Context, spaceID, tagID string) (int, error) {
	rsp := &collectorpb.SyncSubjectsRsp{}
	if err := c.gateway.Invoke(ctx, collectMgr, "SyncSubjects", &collectorpb.SyncSubjectsReq{SpaceId: spaceID, TagId: tagID}, rsp); err != nil {
		return 0, fmt.Errorf("sync subjects: %w", err)
	}
	if rsp.GetRetInfo().GetCode() == collectorpb.ErrorCode_CONFLICT {
		return 0, errSubjectSyncBusy
	}
	if err := collectorRetError("sync subjects", rsp.GetRetInfo()); err != nil {
		return 0, err
	}
	subjects := 0
	for _, tag := range rsp.GetTags() {
		if tag.GetStatus() == "failed" {
			return 0, fmt.Errorf("标签 %s 同步失败：%s", tag.GetTagId(), tag.GetMessage())
		}
		if tag.GetTagId() == tagID {
			subjects = int(tag.GetSubjects())
		}
	}
	return subjects, nil
}

func (c gatewayStartClient) status(ctx context.Context, spaceID string) (*collectorpb.GetCollectorStatusRsp, error) {
	status := &collectorpb.GetCollectorStatusRsp{}
	if err := c.gateway.Invoke(ctx, collectMgr, "GetCollectorStatus", &collectorpb.GetCollectorStatusReq{SpaceId: spaceID}, status); err != nil {
		return nil, fmt.Errorf("read collector status: %w", err)
	}
	return status, collectorRetError("read collector status", status.GetRetInfo())
}

func (c gatewayStartClient) StreamOnline(ctx context.Context, spaceID string) (bool, error) {
	status, err := c.status(ctx, spaceID)
	if err != nil {
		return false, err
	}
	return status.GetStream().GetOnline(), nil
}

func (c gatewayStartClient) Trigger(ctx context.Context, spaceID string) error {
	rsp := &collectorpb.TriggerFetchPlanRsp{}
	if err := c.gateway.Invoke(ctx, collectMgr, "TriggerFetchPlan", &collectorpb.TriggerFetchPlanReq{SpaceId: spaceID}, rsp); err != nil {
		return fmt.Errorf("trigger fetch plan: %w", err)
	}
	return collectorRetError("trigger fetch plan", rsp.GetRetInfo())
}

func (c gatewayStartClient) PlanState(ctx context.Context, spaceID string) (bool, string, error) {
	status, err := c.status(ctx, spaceID)
	if err != nil {
		return false, "", err
	}
	return status.GetOpen() != nil || status.GetApplied() != nil, strings.Join(status.GetReasons(), "；"), nil
}

// strategyCall 以空间 spaceID 的身份调 StrategyMgr 并检查返回码。
func (c gatewayStartClient) strategyCall(ctx context.Context, spaceID, method string, request, response any) error {
	var raw json.RawMessage
	if err := c.admin(spaceID).CallGatewayJSON(ctx, strategyMgr, method, request, &raw); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if err := rpcRetError(raw); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if response != nil {
		return json.Unmarshal(raw, response)
	}
	return nil
}

func (c gatewayStartClient) Instances(ctx context.Context, spaceID string) ([]setupStartInstance, error) {
	var parsed struct {
		Instances []setupStartInstance `json:"instances"`
	}
	err := c.strategyCall(ctx, spaceID, "ListStrategyInstances", map[string]any{"space_id": spaceID, "page": map[string]any{"page": 1, "page_size": 100}}, &parsed)
	return parsed.Instances, err
}

func (c gatewayStartClient) EnsureStrategy(ctx context.Context, spaceID, strategyID, dsl string) error {
	if c.strategyCall(ctx, spaceID, "GetStrategy", map[string]any{"strategy_id": strategyID}, nil) == nil {
		return nil
	}
	return c.strategyCall(ctx, spaceID, "CreateStrategy", map[string]any{"strategy": map[string]any{"strategy_id": strategyID, "dsl_yaml": dsl}}, nil)
}

func (c gatewayStartClient) CreateInstance(ctx context.Context, instance setupStartInstance) error {
	return c.strategyCall(ctx, instance.SpaceID, "CreateStrategyInstance", map[string]any{"instance": instance}, nil)
}

func (c gatewayStartClient) EnableInstance(ctx context.Context, spaceID, instanceID string) error {
	return c.strategyCall(ctx, spaceID, "SetStrategyInstanceEnabled", map[string]any{"instance_id": instanceID, "enabled": true}, nil)
}
