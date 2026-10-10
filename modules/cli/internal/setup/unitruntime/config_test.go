package unitruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func writeFixture(t *testing.T, path string, value any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(raw, '\n'), 0o600))
}

func planFixture(t *testing.T, ids ...string) (string, Plan) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	plan := Plan{Version: 1, HostID: "fixture-control", DeploymentRoot: filepath.Join(root, "deployment"), ReleaseRoot: filepath.Join(root, "unit", "releases", "first"), CatalogSHA256: CatalogSHA256()}
	require.NoError(t, os.MkdirAll(plan.DeploymentRoot, 0o700))
	require.NoError(t, os.MkdirAll(plan.ReleaseRoot, 0o700))
	for _, id := range ids {
		component := Component{ID: id, EnvironmentFile: filepath.Join(plan.ReleaseRoot, "secrets", "runtime-"+id+".json")}
		plan.Components = append(plan.Components, component)
		require.NoError(t, os.MkdirAll(filepath.Join(plan.ReleaseRoot, id, "config"), 0o700))
		writeFixture(t, component.EnvironmentFile, fixtureEnvironment())
	}
	path := filepath.Join(plan.ReleaseRoot, "runtime.json")
	writeFixture(t, path, plan)
	return path, plan
}

func fixtureEnvironment() map[string]string {
	return map[string]string{
		"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": "fixture-monitor",
		"MOOX_HEALTH_AUTH_SECRET_KEY": "fixture-only-health-signing-key-at-least-32-bytes",
		"MOOX_RUNTIME_TEST_CHILD":     "1",
	}
}

func proxyBudgetFixture(t *testing.T, release, drain, engine, cleanup, startup string) {
	t.Helper()
	path := filepath.Join(release, "console-proxy", "config", "app.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	raw := "public:\n  host: localhost\nlifecycle:\n  drain_timeout: " + drain + "\n  engine_stop_timeout: " + engine + "\n  cleanup_margin: " + cleanup + "\n  startup_timeout: " + startup + "\n"
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
}

func TestRuntimePlanValidatesCatalogPhysicalReleaseAndPrivateEnvironment(t *testing.T) {
	path, plan := planFixture(t, "console-proxy", "web-host")
	loaded, err := LoadPlan(path)
	require.NoError(t, err)
	require.Equal(t, plan, loaded)
	selected, err := plan.selectComponents([]string{"web-host", "console-proxy"})
	require.NoError(t, err)
	require.Equal(t, "web-host", selected[0].ID)
	for _, ids := range [][]string{{"web-host", "web-host"}, {"admin"}, {"../admin"}} {
		_, err := plan.selectComponents(ids)
		require.Error(t, err)
	}
	view := filepath.Join(filepath.Dir(plan.ReleaseRoot), "current")
	require.NoError(t, os.Symlink(plan.ReleaseRoot, view))
	_, err = LoadPlan(filepath.Join(view, "runtime.json"))
	require.NoError(t, err, "a directory view may point at the physical release")
	fileLink := filepath.Join(plan.ReleaseRoot, "linked.json")
	require.NoError(t, os.Symlink(path, fileLink))
	_, err = LoadPlan(fileLink)
	require.Error(t, err, "the private plan file itself cannot be a symlink")
	for _, mutate := range []func(*Plan){
		func(p *Plan) { p.Version = 2 },
		func(p *Plan) { p.HostID = "Control" },
		func(p *Plan) { p.CatalogSHA256 = "wrong" },
		func(p *Plan) { p.ReleaseRoot = view },
		func(p *Plan) { p.DeploymentRoot = "/" },
		func(p *Plan) { p.Components = append(p.Components, p.Components[0]) },
		func(p *Plan) { p.Components[0].ID = "unknown" },
		func(p *Plan) { p.Components[0].EnvironmentFile = "../../secret.json" },
	} {
		candidate := plan
		candidate.Components = append([]Component(nil), plan.Components...)
		mutate(&candidate)
		writeFixture(t, path, candidate)
		_, err := LoadPlan(path)
		require.Error(t, err)
	}
	writeFixture(t, path, plan)
	require.NoError(t, os.Chmod(path, 0o644))
	_, err = LoadPlan(path)
	require.Error(t, err)
}

func TestRuntimeEnvironmentRejectsExecutionOverridesUnsafeFilesAndOversizeInput(t *testing.T) {
	_, plan := planFixture(t, "web-host")
	component := plan.Components[0]
	for _, values := range []any{
		map[string]string{"PATH": "/fixture"},
		map[string]string{"LD_PRELOAD": "fixture"},
		map[string]string{"MOOX_BAD=KEY": "fixture"},
		map[string]string{"MOOX_KEY": "bad\x00value"},
		map[string]string{},
		map[string]any{"MOOX_KEY": 123},
	} {
		writeFixture(t, component.EnvironmentFile, values)
		_, err := loadEnvironment(component)
		require.Error(t, err)
	}
	writeFixture(t, component.EnvironmentFile, fixtureEnvironment())
	_, err := loadEnvironment(component)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(component.EnvironmentFile, 0o644))
	_, err = loadEnvironment(component)
	require.Error(t, err)
	require.NoError(t, os.Chmod(component.EnvironmentFile, 0o600))
	require.NoError(t, os.WriteFile(component.EnvironmentFile, []byte(strings.Repeat("x", maxPrivateBytes+1)), 0o600))
	_, err = loadEnvironment(component)
	require.Error(t, err)
}

func TestRuntimeProxyBudgetUsesAllConfiguredPhasesAndRejectsFallbacks(t *testing.T) {
	_, plan := planFixture(t, "console-proxy")
	proxyBudgetFixture(t, plan.ReleaseRoot, "30s", "10s", "5s", "3m")
	stop, startup, err := lifecycleBudgets(plan.ReleaseRoot, "console-proxy")
	require.NoError(t, err)
	require.Equal(t, 45*time.Second, stop)
	require.Equal(t, 3*time.Minute, startup)
	for _, duration := range []string{"0s", "-1s", "11m", "bad", ""} {
		proxyBudgetFixture(t, plan.ReleaseRoot, duration, "10s", "5s", "3m")
		_, _, err := lifecycleBudgets(plan.ReleaseRoot, "console-proxy")
		require.Error(t, err)
	}
	stop, _, err = lifecycleBudgets(plan.ReleaseRoot, "web-host")
	require.NoError(t, err)
	require.Equal(t, DefaultStopTimeout, stop)
}
