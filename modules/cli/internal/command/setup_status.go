package command

import (
	"context"
	"fmt"
	"slices"
	"strings"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

// 主机与部署的启停只在 Admin 登记状态上做记录；外部程序（systemd 用户服务）要同时真正停掉或拉起，
// 所以启停必须由 moox-cli 完成，控制台只展示。

type statusRegistry interface {
	Registry(context.Context) ([]setupclient.HostRegistration, []setupclient.DeploymentRegistration, error)
	SetHostStatus(ctx context.Context, hostID, status string) error
	SetDeploymentStatus(ctx context.Context, hostID, componentID, status string) error
}

type externalServices interface {
	Stop(ctx context.Context, host setupconfig.Host, component string) error
	Start(ctx context.Context, host setupconfig.Host, component, deployRoot string) error
}

// statusEnv 是启停需要的外部能力；测试里替换。
type statusEnv struct {
	registry   statusRegistry
	services   externalServices
	host       func(id string) (setupconfig.Host, error)
	deployRoot string
	external   func(component string) bool
}

type statusStep struct {
	Action    string `json:"action"`
	Host      string `json:"host"`
	Component string `json:"component,omitempty"`
	Result    string `json:"result,omitempty"`
}

type statusReport struct {
	Status string       `json:"status"`
	Steps  []statusStep `json:"steps"`
	Error  string       `json:"error,omitempty"`
}

func isExternalComponent(component string) bool {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return false
	}
	c, ok := catalog.Component(component)
	return ok && c.ExternalProgram
}

type sshServices struct {
	open func(context.Context, setupconfig.Host) (setupssh.Client, error)
}

func (s sshServices) Stop(ctx context.Context, host setupconfig.Host, component string) error {
	client, err := s.open(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()
	return unitdeploy.StopExternalService(ctx, client, component)
}

func (s sshServices) Start(ctx context.Context, host setupconfig.Host, component, deployRoot string) error {
	client, err := s.open(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()
	return unitdeploy.StartExternalService(ctx, client, component, deployRoot)
}

type registrySnapshot struct {
	hosts       map[string]string
	deployments map[string]map[string]string // host → component → status
}

func readRegistry(ctx context.Context, registry statusRegistry) (registrySnapshot, error) {
	hosts, deployments, err := registry.Registry(ctx)
	if err != nil {
		return registrySnapshot{}, err
	}
	snapshot := registrySnapshot{hosts: map[string]string{}, deployments: map[string]map[string]string{}}
	for _, h := range hosts {
		snapshot.hosts[h.HostID] = h.Status
	}
	for _, d := range deployments {
		if snapshot.deployments[d.HostID] == nil {
			snapshot.deployments[d.HostID] = map[string]string{}
		}
		snapshot.deployments[d.HostID][d.ComponentID] = d.Status
	}
	return snapshot, nil
}

// enabledExternals 返回主机上登记为启用的外部程序部署，按组件 ID 排序。
func (r registrySnapshot) enabledExternals(env statusEnv, hostID string) []string {
	var out []string
	for component, status := range r.deployments[hostID] {
		if status == servicecatalog.Enabled && env.external(component) {
			out = append(out, component)
		}
	}
	slices.Sort(out)
	return out
}

// changeHostStatus 停用或启用一台主机。execute 为 false 时只返回计划。
//   - 停用：先逐个停掉启用部署的外部程序，全部成功后才改登记，任何一个失败都不改登记；
//   - 启用：先改登记，再只启动启用部署的外部程序（单独停用的程序保持停用）。
func changeHostStatus(ctx context.Context, env statusEnv, hostID string, enable, execute bool) (statusReport, error) {
	snapshot, err := readRegistry(ctx, env.registry)
	if err != nil {
		return statusReport{}, err
	}
	if _, ok := snapshot.hosts[hostID]; !ok {
		return statusReport{}, fmt.Errorf("Admin 里没有主机 %s", hostID)
	}
	externals := snapshot.enabledExternals(env, hostID)
	var steps []statusStep
	target := servicecatalog.Disabled
	if enable {
		target = servicecatalog.Enabled
		steps = append(steps, statusStep{Action: "set-host-status:enabled", Host: hostID})
		for _, component := range externals {
			steps = append(steps, statusStep{Action: "start-service", Host: hostID, Component: component})
		}
	} else {
		for _, component := range externals {
			steps = append(steps, statusStep{Action: "stop-service", Host: hostID, Component: component})
		}
		steps = append(steps, statusStep{Action: "set-host-status:disabled", Host: hostID})
	}
	report := statusReport{Status: "preview", Steps: steps}
	if !execute {
		return report, nil
	}
	report.Status = "applied"
	host, err := env.host(hostID)
	if len(externals) > 0 && err != nil {
		return report, err
	}
	failed := false
	for i := range report.Steps {
		step := &report.Steps[i]
		if failed {
			step.Result = "skipped"
			continue
		}
		var stepErr error
		switch {
		case step.Action == "stop-service":
			stepErr = env.services.Stop(ctx, host, step.Component)
		case step.Action == "start-service":
			stepErr = env.services.Start(ctx, host, step.Component, env.deployRoot)
		default:
			stepErr = env.registry.SetHostStatus(ctx, hostID, target)
		}
		if stepErr != nil {
			step.Result = "failed: " + stepErr.Error()
			report.Status, report.Error = "failed", stepErr.Error()
			// 停用时任何一个程序没停住就不改登记；启用时登记已改，其余程序继续尝试启动。
			failed = !enable
			continue
		}
		step.Result = "ok"
	}
	if report.Status == "failed" {
		return report, fmt.Errorf("%s", report.Error)
	}
	return report, nil
}

// changeDeploymentStatus 停用或启用一个组件部署。
//   - 外部程序停用：先停服务并核对已停止，再改登记；
//   - 外部程序启用：主机停用时拒绝；先改登记，再启动服务；
//   - 普通组件：只改登记（网关路由随之撤销或恢复）。
func changeDeploymentStatus(ctx context.Context, env statusEnv, hostID, component string, enable, execute bool) (statusReport, error) {
	snapshot, err := readRegistry(ctx, env.registry)
	if err != nil {
		return statusReport{}, err
	}
	hostStatus, ok := snapshot.hosts[hostID]
	if !ok {
		return statusReport{}, fmt.Errorf("Admin 里没有主机 %s", hostID)
	}
	if _, ok := snapshot.deployments[hostID][component]; !ok {
		return statusReport{}, fmt.Errorf("主机 %s 没有部署 %s", hostID, component)
	}
	external := env.external(component)
	target := servicecatalog.Disabled
	if enable {
		target = servicecatalog.Enabled
		if external && hostStatus != servicecatalog.Enabled {
			return statusReport{}, fmt.Errorf("主机 %s 已停用，先用 setup host enable 启用主机，再启用 %s", hostID, component)
		}
	}
	var steps []statusStep
	switch {
	case external && !enable:
		steps = []statusStep{{Action: "stop-service", Host: hostID, Component: component}, {Action: "set-deployment-status:disabled", Host: hostID, Component: component}}
	case external && enable:
		steps = []statusStep{{Action: "set-deployment-status:enabled", Host: hostID, Component: component}, {Action: "start-service", Host: hostID, Component: component}}
	default:
		steps = []statusStep{{Action: "set-deployment-status:" + target, Host: hostID, Component: component}}
	}
	report := statusReport{Status: "preview", Steps: steps}
	if !execute {
		return report, nil
	}
	report.Status = "applied"
	var host setupconfig.Host
	if external {
		if host, err = env.host(hostID); err != nil {
			return report, err
		}
	}
	for i := range report.Steps {
		step := &report.Steps[i]
		var stepErr error
		switch {
		case step.Action == "stop-service":
			stepErr = env.services.Stop(ctx, host, component)
		case step.Action == "start-service":
			stepErr = env.services.Start(ctx, host, component, env.deployRoot)
		default:
			stepErr = env.registry.SetDeploymentStatus(ctx, hostID, component, target)
		}
		if stepErr != nil {
			step.Result = "failed: " + stepErr.Error()
			for j := i + 1; j < len(report.Steps); j++ {
				report.Steps[j].Result = "skipped"
			}
			report.Status, report.Error = "failed", stepErr.Error()
			return report, fmt.Errorf("%s", report.Error)
		}
		step.Result = "ok"
	}
	return report, nil
}

// defaultStatusEnv 用操作员网关与已核验的 SSH 装配启停环境；返回的 close 释放网关。
func defaultStatusEnv(ctx context.Context, file string, snapshot *setupconfig.Snapshot) (statusEnv, func(), error) {
	gateway, err := openCommandGateway(ctx, file, snapshot)
	if err != nil {
		return statusEnv{}, nil, err
	}
	env := statusEnv{
		registry: setupclient.New(gateway),
		services: sshServices{open: func(ctx context.Context, host setupconfig.Host) (setupssh.Client, error) {
			return dialSetupHost(ctx, host)
		}},
		deployRoot: snapshot.Manifest.Paths.Resolved().DeployRoot,
		external:   isExternalComponent,
		host: func(id string) (setupconfig.Host, error) {
			host := snapshot.Manifest.HostByID(id)
			if host.Name == "" {
				return setupconfig.Host{}, fmt.Errorf("moox.toml 里没有主机 %s", id)
			}
			return host, nil
		},
	}
	return env, func() { _ = gateway.Close() }, nil
}

func newSetupHostStatusCommand(deps setupDeps, newEnv func(context.Context, string, *setupconfig.Snapshot) (statusEnv, func(), error)) *cobra.Command {
	host := &cobra.Command{Use: "host", Short: "启用或停用一台已登记的主机（默认只预览）"}
	for _, enable := range []bool{true, false} {
		var file, hostID string
		var yes bool
		verb, short := "disable", "停用主机：先停掉它上面启用的外部程序，全部成功后才改登记（网关路由随之撤销）"
		if enable {
			verb, short = "enable", "启用主机：先改登记，再只启动启用部署的外部程序（单独停用的程序保持停用）"
		}
		command := &cobra.Command{Use: verb, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			hostID = strings.TrimSpace(hostID)
			if hostID == "" {
				return fmt.Errorf("--host 必填")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			if !enable && hostID == snapshot.Manifest.ControlHost().Name {
				return fmt.Errorf("不能停用控制机 %s", hostID)
			}
			env, closeEnv, err := newEnv(cmd.Context(), file, snapshot)
			if err != nil {
				return err
			}
			defer closeEnv()
			report, err := changeHostStatus(cmd.Context(), env, hostID, enable, yes)
			if report.Steps != nil {
				if writeErr := writeSetupJSON(cmd, report); writeErr != nil {
					return writeErr
				}
			}
			return err
		}}
		command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
		command.Flags().StringVar(&hostID, "host", "", "主机 ID")
		command.Flags().BoolVar(&yes, "yes", false, "确认执行；不加只预览")
		host.AddCommand(command)
	}
	return host
}

func newSetupDeploymentStatusCommand(deps setupDeps, newEnv func(context.Context, string, *setupconfig.Snapshot) (statusEnv, func(), error)) *cobra.Command {
	deployment := &cobra.Command{Use: "deployment", Short: "启用或停用一个组件部署（默认只预览）"}
	for _, enable := range []bool{true, false} {
		var file, hostID, component string
		var yes bool
		verb, short := "disable", "停用部署：外部程序先停服务并核对已停止，再改登记；普通组件只改登记"
		if enable {
			verb, short = "enable", "启用部署：外部程序要求主机已启用，先改登记再启动服务；普通组件只改登记"
		}
		command := &cobra.Command{Use: verb, Short: short, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			hostID, component = strings.TrimSpace(hostID), strings.TrimSpace(component)
			if hostID == "" || component == "" {
				return fmt.Errorf("--host 与 --component 必填")
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			env, closeEnv, err := newEnv(cmd.Context(), file, snapshot)
			if err != nil {
				return err
			}
			defer closeEnv()
			report, err := changeDeploymentStatus(cmd.Context(), env, hostID, component, enable, yes)
			if report.Steps != nil {
				if writeErr := writeSetupJSON(cmd, report); writeErr != nil {
					return writeErr
				}
			}
			return err
		}}
		command.Flags().StringVar(&file, "file", defaultSetupFile, "原仓库中的私有 moox.toml")
		command.Flags().StringVar(&hostID, "host", "", "主机 ID")
		command.Flags().StringVar(&component, "component", "", "组件 ID")
		command.Flags().BoolVar(&yes, "yes", false, "确认执行；不加只预览")
		deployment.AddCommand(command)
	}
	return deployment
}
