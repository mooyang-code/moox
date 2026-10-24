package command

import (
	"context"
	"errors"
	"testing"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRegistry struct {
	hosts       map[string]string
	deployments map[string]map[string]string
	calls       []string
	failSet     bool
}

func (f *fakeRegistry) Registry(context.Context) ([]setupclient.HostRegistration, []setupclient.DeploymentRegistration, error) {
	var hosts []setupclient.HostRegistration
	for id, status := range f.hosts {
		hosts = append(hosts, setupclient.HostRegistration{HostID: id, Status: status})
	}
	var deployments []setupclient.DeploymentRegistration
	for host, components := range f.deployments {
		for component, status := range components {
			deployments = append(deployments, setupclient.DeploymentRegistration{HostID: host, ComponentID: component, Status: status})
		}
	}
	return hosts, deployments, nil
}

func (f *fakeRegistry) SetHostStatus(_ context.Context, hostID, status string) error {
	f.calls = append(f.calls, "host:"+hostID+":"+status)
	if f.failSet {
		return errors.New("admin refused")
	}
	f.hosts[hostID] = status
	return nil
}

func (f *fakeRegistry) SetDeploymentStatus(_ context.Context, hostID, component, status string) error {
	f.calls = append(f.calls, "deployment:"+hostID+"/"+component+":"+status)
	if f.failSet {
		return errors.New("admin refused")
	}
	f.deployments[hostID][component] = status
	return nil
}

type fakeServices struct {
	calls    *[]string
	failStop map[string]bool
	failOn   string
}

func (f fakeServices) Stop(_ context.Context, host setupconfig.Host, component string) error {
	*f.calls = append(*f.calls, "stop:"+host.Name+"/"+component)
	if f.failStop[component] {
		return errors.New("service still running")
	}
	return nil
}

func (f fakeServices) Start(_ context.Context, host setupconfig.Host, component, _ string) error {
	*f.calls = append(*f.calls, "start:"+host.Name+"/"+component)
	if f.failOn == component {
		return errors.New("not ready")
	}
	return nil
}

func statusFixture() (statusEnv, *fakeRegistry, *[]string) {
	registry := &fakeRegistry{
		hosts: map[string]string{"control": "enabled", "factor": "enabled"},
		deployments: map[string]map[string]string{
			"control": {"admin": "enabled"},
			"factor":  {"factor-engine": "enabled", "archive-worker": "disabled"},
		},
	}
	var calls []string
	env := statusEnv{
		registry:   registry,
		services:   fakeServices{calls: &calls, failStop: map[string]bool{}},
		deployRoot: "/data/moox",
		external:   func(component string) bool { return component == "factor-engine" || component == "archive-worker" },
		host:       func(id string) (setupconfig.Host, error) { return setupconfig.Host{Name: id}, nil },
	}
	return env, registry, &calls
}

func TestHostDisablePreviewDoesNotChangeAnything(t *testing.T) {
	env, registry, calls := statusFixture()
	report, err := changeHostStatus(t.Context(), env, "factor", false, false)
	require.NoError(t, err)
	assert.Equal(t, "preview", report.Status)
	assert.Equal(t, []statusStep{{Action: "stop-service", Host: "factor", Component: "factor-engine"}, {Action: "set-host-status:disabled", Host: "factor"}}, report.Steps)
	assert.Empty(t, registry.calls)
	assert.Empty(t, *calls)
}

func TestHostDisableStopsEnabledExternalsThenChangesRegistry(t *testing.T) {
	env, registry, calls := statusFixture()
	report, err := changeHostStatus(t.Context(), env, "factor", false, true)
	require.NoError(t, err)
	assert.Equal(t, "applied", report.Status)
	assert.Equal(t, []string{"stop:factor/factor-engine"}, *calls, "单独停用的 archive-worker 不再操作")
	assert.Equal(t, []string{"host:factor:disabled"}, registry.calls)
	assert.Equal(t, "ok", report.Steps[1].Result)
}

func TestHostDisableKeepsRegistryWhenAServiceDoesNotStop(t *testing.T) {
	env, registry, _ := statusFixture()
	env.services = fakeServices{calls: &[]string{}, failStop: map[string]bool{"factor-engine": true}}
	report, err := changeHostStatus(t.Context(), env, "factor", false, true)
	require.ErrorContains(t, err, "service still running")
	assert.Equal(t, "failed", report.Status)
	assert.Empty(t, registry.calls, "有程序没停住时不改登记")
	assert.Equal(t, "skipped", report.Steps[1].Result)
	assert.Equal(t, "enabled", registry.hosts["factor"])
}

func TestHostEnableRestoresRegistryThenStartsOnlyEnabledDeployments(t *testing.T) {
	env, registry, calls := statusFixture()
	registry.hosts["factor"] = "disabled"
	report, err := changeHostStatus(t.Context(), env, "factor", true, true)
	require.NoError(t, err)
	assert.Equal(t, "applied", report.Status)
	assert.Equal(t, []string{"host:factor:enabled"}, registry.calls)
	assert.Equal(t, []string{"start:factor/factor-engine"}, *calls, "恢复主机时保留单独停用的程序")
}

func TestHostEnableReportsEachProgramAndKeepsRegistryEnabled(t *testing.T) {
	env, registry, _ := statusFixture()
	registry.hosts["factor"] = "disabled"
	registry.deployments["factor"]["archive-worker"] = "enabled"
	env.services = fakeServices{calls: &[]string{}, failStop: map[string]bool{}, failOn: "archive-worker"}
	report, err := changeHostStatus(t.Context(), env, "factor", true, true)
	require.ErrorContains(t, err, "not ready")
	assert.Equal(t, "enabled", registry.hosts["factor"])
	require.Len(t, report.Steps, 3)
	// 步骤按组件 ID 排序：archive-worker 先启动并失败，不影响后面的 factor-engine 继续尝试。
	assert.Equal(t, "failed: not ready", report.Steps[1].Result)
	assert.Equal(t, "ok", report.Steps[2].Result)
}

func TestDeploymentDisableExternalStopsServiceBeforeRegistry(t *testing.T) {
	env, registry, calls := statusFixture()
	_, err := changeDeploymentStatus(t.Context(), env, "factor", "factor-engine", false, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"stop:factor/factor-engine"}, *calls)
	assert.Equal(t, []string{"deployment:factor/factor-engine:disabled"}, registry.calls)

	env, registry, _ = statusFixture()
	env.services = fakeServices{calls: &[]string{}, failStop: map[string]bool{"factor-engine": true}}
	_, err = changeDeploymentStatus(t.Context(), env, "factor", "factor-engine", false, true)
	require.Error(t, err)
	assert.Empty(t, registry.calls, "停不住就不改登记")
}

func TestDeploymentEnableExternalRefusedOnDisabledHost(t *testing.T) {
	env, registry, calls := statusFixture()
	registry.hosts["factor"] = "disabled"
	_, err := changeDeploymentStatus(t.Context(), env, "factor", "archive-worker", true, true)
	require.ErrorContains(t, err, "已停用")
	assert.Empty(t, registry.calls)
	assert.Empty(t, *calls)
}

func TestDeploymentEnableExternalChangesRegistryThenStarts(t *testing.T) {
	env, registry, calls := statusFixture()
	report, err := changeDeploymentStatus(t.Context(), env, "factor", "archive-worker", true, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployment:factor/archive-worker:enabled"}, registry.calls)
	assert.Equal(t, []string{"start:factor/archive-worker"}, *calls)
	assert.Equal(t, "applied", report.Status)
}

func TestDeploymentChangeForOrdinaryComponentOnlyTouchesRegistry(t *testing.T) {
	env, registry, calls := statusFixture()
	report, err := changeDeploymentStatus(t.Context(), env, "control", "admin", false, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"deployment:control/admin:disabled"}, registry.calls)
	assert.Empty(t, *calls)
	assert.Len(t, report.Steps, 1)

	preview, err := changeDeploymentStatus(t.Context(), env, "control", "admin", true, false)
	require.NoError(t, err)
	assert.Equal(t, "preview", preview.Status)
	assert.Len(t, registry.calls, 1, "预览不改登记")

	_, err = changeDeploymentStatus(t.Context(), env, "control", "missing", true, true)
	require.ErrorContains(t, err, "没有部署")
	_, err = changeDeploymentStatus(t.Context(), env, "nowhere", "admin", true, true)
	require.ErrorContains(t, err, "没有主机")
}
