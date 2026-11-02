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

const (
	// 默认示例：合约 1m 双均线观察实例。用固定 ID，重复运行能识别。
	defaultStrategyID     = "default_dual_ma_swap_1m"
	defaultInstanceID     = "default_dual_ma_swap_1m"
	defaultStrategyTask   = "ftask_swap_1m"
	defaultStrategySpace  = "crypto"
	defaultStrategyDSL    = "examples/strategies/dual_ma_swap_1m.yaml"
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
	StreamOnline(ctx context.Context, spaceID string) (bool, error)
	Trigger(ctx context.Context, spaceID string) error
	PlanState(ctx context.Context, spaceID string) (bool, string, error)
	Instances(ctx context.Context, spaceID string) ([]setupStartInstance, error)
	EnsureStrategy(ctx context.Context, strategyID, dsl string) error
	CreateInstance(ctx context.Context, instance setupStartInstance) error
	EnableInstance(ctx context.Context, instanceID string) error
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

// setupStartPrerequisites 只查这个空间需要的东西：collector-stream 的空间写死为 crypto，其他空间只能走云函数。
func setupStartPrerequisites(manifest setupconfig.Manifest, spaceID string) error {
	if spaceID == defaultStrategySpace {
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
	if spaceID == defaultStrategySpace {
		// 触发时 collector-stream 不在线，这一轮只会记下原因，要等到下一分钟。
		if err := setupStartWaitStream(ctx, client, spaceID, poll); err != nil {
			return summary, err
		}
	}
	for _, task := range tasks {
		if task.Enabled {
			continue
		}
		if err := setupStartEnableTask(ctx, client, spaceID, task.TaskID, poll); err != nil {
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
	if spaceID != defaultStrategySpace || !slices.ContainsFunc(manifest.Factors.Tasks, func(task setupconfig.FactorSetupTask) bool { return task.ID == defaultStrategyTask }) {
		return summary, nil
	}
	summary.Strategy, err = setupStartStrategy(ctx, client, repository, &summary)
	return summary, err
}

// setupStartEnableTask 启用任务。标的刚同步完时标签还可能解析不出成员，控制面会拒绝启用；
// 这是暂时现象，等一会儿再试，其他错误原样返回。
func setupStartEnableTask(ctx context.Context, client setupStartClient, spaceID, taskID string, poll time.Duration) error {
	deadline := time.Now().Add(setupStartSubjectWait)
	for {
		err := client.EnableTask(ctx, spaceID, taskID)
		if err == nil || !strings.Contains(err.Error(), "没有有效标的") || !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
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

// setupStartStrategy 创建默认示例并启用。用户已经把这个因子任务绑给别的实例时跳过：一个因子任务只能有一个实例。
func setupStartStrategy(ctx context.Context, client setupStartClient, repository string, summary *setupStartSummary) (string, error) {
	instances, err := client.Instances(ctx, defaultStrategySpace)
	if err != nil {
		return "", err
	}
	var existing *setupStartInstance
	for i, instance := range instances {
		switch {
		case instance.InstanceID == defaultInstanceID:
			existing = &instances[i]
		case instance.FactorTaskID == defaultStrategyTask:
			summary.Notes = append(summary.Notes, fmt.Sprintf("因子任务 %s 已绑定实例 %s，跳过默认示例", defaultStrategyTask, instance.InstanceID))
			return "skipped", nil
		}
	}
	if existing != nil {
		if existing.Enabled {
			return "unchanged", nil
		}
		return "enabled", client.EnableInstance(ctx, defaultInstanceID)
	}
	dsl, err := os.ReadFile(filepath.Join(repository, filepath.FromSlash(defaultStrategyDSL)))
	if err != nil {
		return "", fmt.Errorf("读取默认策略: %w", err)
	}
	if err := client.EnsureStrategy(ctx, defaultStrategyID, string(dsl)); err != nil {
		return "", err
	}
	return "created", client.CreateInstance(ctx, setupStartInstance{InstanceID: defaultInstanceID, StrategyID: defaultStrategyID, SpaceID: defaultStrategySpace, FactorTaskID: defaultStrategyTask, Enabled: true})
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

// strategyCall 调 StrategyMgr 并检查返回码。
func (c gatewayStartClient) strategyCall(ctx context.Context, method string, request, response any) error {
	var raw json.RawMessage
	if err := c.admin(defaultStrategySpace).CallGatewayJSON(ctx, strategyMgr, method, request, &raw); err != nil {
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
	err := c.strategyCall(ctx, "ListStrategyInstances", map[string]any{"space_id": spaceID, "page": map[string]any{"page": 1, "page_size": 100}}, &parsed)
	return parsed.Instances, err
}

func (c gatewayStartClient) EnsureStrategy(ctx context.Context, strategyID, dsl string) error {
	if c.strategyCall(ctx, "GetStrategy", map[string]any{"strategy_id": strategyID}, nil) == nil {
		return nil
	}
	return c.strategyCall(ctx, "CreateStrategy", map[string]any{"strategy": map[string]any{"strategy_id": strategyID, "dsl_yaml": dsl}}, nil)
}

func (c gatewayStartClient) CreateInstance(ctx context.Context, instance setupStartInstance) error {
	return c.strategyCall(ctx, "CreateStrategyInstance", map[string]any{"instance": instance}, nil)
}

func (c gatewayStartClient) EnableInstance(ctx context.Context, instanceID string) error {
	return c.strategyCall(ctx, "SetStrategyInstanceEnabled", map[string]any{"instance_id": instanceID, "enabled": true}, nil)
}
