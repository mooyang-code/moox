package unitruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/servicecatalog"
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
	releaseCatalogFixture(t, plan.ReleaseRoot, servicecatalog.EmbeddedYAML())
	path := filepath.Join(plan.ReleaseRoot, "runtime.json")
	writeFixture(t, path, plan)
	return path, plan
}

// releaseCatalogFixture 写入发布自带的服务目录与记录它哈希的包清单（与 unitpackage 打包的布局相同），返回目录哈希。
func releaseCatalogFixture(t *testing.T, release string, catalog []byte) string {
	t.Helper()
	digest := sha256.Sum256(catalog)
	value := "sha256:" + hex.EncodeToString(digest[:])
	path := filepath.Join(release, releaseCatalogPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, catalog, 0o644))
	require.NoError(t, os.Chmod(path, 0o644))
	manifest := map[string]any{
		"version": 1, "profile": "control", "catalog_sha256": value,
		"files": []map[string]any{{"path": "bin/moox-web-host", "sha256": "sha256:" + strings.Repeat("0", 64)}, {"path": releaseCatalogPath, "sha256": value}},
	}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(release, releaseManifestName), append(raw, '\n'), 0o644))
	return value
}

// olderCatalogFixture 把发布改成“用上一个 runtime 版本打包”：自带的服务目录与当前内嵌目录不同（组件相同），计划、包清单都记录那份目录。
func olderCatalogFixture(t *testing.T, path string, plan Plan) Plan {
	t.Helper()
	older := append(servicecatalog.EmbeddedYAML(), []byte("\n# packaged by an older runtime release\n")...)
	plan.CatalogSHA256 = releaseCatalogFixture(t, plan.ReleaseRoot, older)
	require.NotEqual(t, CatalogSHA256(), plan.CatalogSHA256)
	writeFixture(t, path, plan)
	return plan
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
		func(p *Plan) { p.CatalogSHA256 = "sha256:" + strings.Repeat("a", 64) },
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

// runtime 升级后服务目录变了：已安装的发布按它自带、并经计划与包清单核对过的目录读取，新 runtime 仍能读取和停止它；
// 候选发布必须用本 runtime 的目录。
func TestInstalledReleaseUsesItsOwnCatalogWhileCandidateMustMatchRuntime(t *testing.T) {
	path, plan := planFixture(t, "console-proxy", "web-host")
	proxyBudgetFixture(t, plan.ReleaseRoot, "30s", "10s", "5s", "1m")
	placeholderBinaries(t, plan)
	_, err := LoadCandidatePlan(path)
	require.NoError(t, err, "a release packaged with this runtime's catalog is a valid candidate")
	require.NoError(t, ValidateCandidate(path))

	plan = olderCatalogFixture(t, path, plan)
	loaded, err := LoadPlan(path)
	require.NoError(t, err, "an installed release keeps the catalog it was packaged with")
	require.Equal(t, plan, loaded)
	require.NoError(t, ValidateRelease(path))
	catalog, err := releaseCatalog(loaded)
	require.NoError(t, err)
	_, ok := catalog.Component("web-host")
	require.True(t, ok)

	_, err = LoadCandidatePlan(path)
	require.ErrorContains(t, err, "candidate release must use this runtime's service catalog")
	require.Error(t, ValidateCandidate(path))
}

// placeholderBinaries 为计划里的组件放上发布内的可执行文件（只用于不启动进程的校验）。
func placeholderBinaries(t *testing.T, plan Plan) {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(plan.ReleaseRoot, "bin"), 0o700))
	for _, component := range plan.Components {
		entry, ok := catalog.Component(component.ID)
		require.True(t, ok)
		binary := filepath.Join(plan.ReleaseRoot, "bin", entry.Binary)
		require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o755))
		require.NoError(t, os.Chmod(binary, 0o755))
	}
}

// 发布自带的目录被改动（与计划、包清单记录的哈希不符）、包清单不一致或文件缺失时，已安装发布也拒绝读取。
func TestInstalledReleaseRejectsTamperedOrMissingCatalog(t *testing.T) {
	path, plan := planFixture(t, "web-host")
	plan = olderCatalogFixture(t, path, plan)
	catalogPath := filepath.Join(plan.ReleaseRoot, releaseCatalogPath)
	original, err := os.ReadFile(catalogPath)
	require.NoError(t, err)
	manifestPath := filepath.Join(plan.ReleaseRoot, releaseManifestName)
	manifest, err := os.ReadFile(manifestPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(catalogPath, append(append([]byte(nil), original...), []byte("# tampered\n")...), 0o644))
	_, err = LoadPlan(path)
	require.ErrorContains(t, err, "does not match its runtime plan")

	// 目录与计划一致，但包清单记录的是另一份：同样拒绝。
	require.NoError(t, os.WriteFile(catalogPath, original, 0o644))
	require.NoError(t, os.WriteFile(manifestPath, []byte(strings.ReplaceAll(string(manifest), strings.TrimPrefix(plan.CatalogSHA256, "sha256:"), strings.Repeat("b", 64))), 0o644))
	_, err = LoadPlan(path)
	require.ErrorContains(t, err, "does not match its package manifest")

	require.NoError(t, os.WriteFile(manifestPath, manifest, 0o644))
	_, err = LoadPlan(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(catalogPath, 0o666))
	_, err = LoadPlan(path)
	require.Error(t, err, "a group- or world-writable catalog is not trusted")
	require.NoError(t, os.Remove(catalogPath))
	_, err = LoadPlan(path)
	require.ErrorContains(t, err, "release service catalog cannot be read")
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
