package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mooyang-code/moox/modules/cli/internal/rpcclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// fourHostSnapshot 是示例拓扑：控制机、存储机（带 EventBus）、海外机、只能出站的因子机。
func fourHostSnapshot(t *testing.T) *setupSnapshot {
	t.Helper()
	cfg, err := topology.Parse([]byte(`[hosts.control]
address = "203.0.113.10"
ssh = { port = 22, username = "ubuntu" }

[hosts.storage]
address = "203.0.113.11"
ssh = { port = 22, username = "ubuntu" }

[hosts.overseas]
address = "203.0.113.12"
ssh = { port = 22, username = "ubuntu" }

[hosts.factor]
address = "192.0.2.30"
reachability = "outbound"
ssh = { port = 22, username = "builder" }

[deployments]
control = ["admin", "monitor", "collector", "factor-mgr", "archive-mgr", "strategy"]
storage = ["storage-primary", "storage-view", "eventbus"]
overseas = ["trade", "egress-proxy", "collector-stream", "callback-dispatcher"]
factor = ["factor-engine", "archive-worker"]
`))
	require.NoError(t, err)
	secrets, err := topology.ParseSecrets([]byte(`[admin]
username = "admin"
password = "admin-test-password"

[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"
`))
	require.NoError(t, err)
	return &setupSnapshot{Manifest: &cfg, Secrets: &secrets}
}

// installHarness 把部署、初始化、启动和验收都换成记录调用；reports 是只读检测的结果，清空后变成空机。
type installHarness struct {
	t        *testing.T
	snapshot *setupSnapshot
	deps     setupDeps
	mu       sync.Mutex
	calls    []string
	fail     string
	reports  []deployscan.HostReport
	operator string
	source   string
	// seedDirs 是读种子用过的目录，initSources 是完整初始化收到的源目录。
	seedDirs, initSources []string
	// stderr 是最近一次 run 的标准错误（进度行在这里）。
	stderr string
}

func newInstallHarness(t *testing.T) *installHarness {
	t.Helper()
	h := &installHarness{t: t, snapshot: fourHostSnapshot(t), operator: t.TempDir()}
	// setup start 要读源码目录里的内置采集任务与因子种子。
	h.source = startRepository(t)
	seedDir := filepath.Join(h.source, "config", "setup")
	writeLibrary(t, filepath.Join(seedDir, "factors"), "lib.yaml")
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "factor-tasks.yaml"), []byte(testSeed), 0o600))
	for _, host := range h.snapshot.Manifest.Hosts() {
		h.reports = append(h.reports, deployscan.HostReport{Host: host.Name, Root: "/data/moox", Linger: true, Journald: true, DiskFreeKB: 50 << 20, ArchiveKB: -1, SeaweedFSKB: -1})
	}
	record := func(name string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls = append(h.calls, name)
		if name == h.fail {
			return errors.New("boom " + name)
		}
		return nil
	}
	h.deps = setupDeps{
		load: func(string) (*setupSnapshot, error) { return h.snapshot, nil },
		loadInitBundle: func(dir string) (setupInitBundle, error) {
			h.seedDirs = append(h.seedDirs, dir)
			return setupInitBundle{}, nil
		},
		scanDeployment: func(_ context.Context, _ *setupSnapshot, options deployscan.Options) ([]deployscan.HostReport, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			return slices.DeleteFunc(slices.Clone(h.reports), func(report deployscan.HostReport) bool { return options.Host != "" && report.Host != options.Host }), nil
		},
		wipeHost: func(_ context.Context, host topology.Host, root string) (deployscan.HostResult, error) {
			if err := record("wipe:" + host.Name); err != nil {
				return deployscan.HostResult{}, err
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			for i := range h.reports {
				if h.reports[i].Host == host.Name {
					h.reports[i].Entries, h.reports[i].Units, h.reports[i].Active, h.reports[i].Installed, h.reports[i].Version = nil, nil, nil, false, nil
				}
			}
			return deployscan.HostResult{Host: host.Name}, nil
		},
		ensureFirewall: func(context.Context, *setupSnapshot) (setupFirewallSummary, error) {
			return setupFirewallSummary{Status: "ready"}, record("firewall")
		},
		deployHost: func(_ context.Context, options deploy.Options) (deploy.Result, error) {
			name := "deploy:" + options.HostID
			if len(options.Components) != 0 {
				name += ":" + strings.Join(options.Components, ",")
			}
			return deploy.Result{Host: options.HostID}, record(name)
		},
		waitReady: func(_ context.Context, host topology.Host, _ []string, _ time.Duration) (map[string]string, error) {
			return nil, record("ready:" + host.Name)
		},
		initMetadata: func(context.Context, *setupSnapshot, string, setupInitBundle) (setupInitStorageSummary, error) {
			return setupInitStorageSummary{}, record("init-metadata")
		},
		initStorage: func(_ context.Context, _ *setupSnapshot, _, sourceRoot string, _ setupInitBundle) (setupInitSummary, error) {
			h.initSources = append(h.initSources, sourceRoot)
			return setupInitSummary{}, record("init")
		},
		openInitFactor: func(context.Context, *setupSnapshot) (setupInitFactor, error) { return &fakeSetupInitFactor{}, nil },
		verifySpace:    func(context.Context, string, *setupSnapshot, string) error { return record("verify") },
		openStart: func(context.Context, string, *setupSnapshot) (setupStartClient, func(), error) {
			if err := record("start"); err != nil {
				return nil, nil, err
			}
			client := startClient()
			client.tasks = []rpcclient.CollectionTaskSummary{{TaskID: "a"}}
			return client, func() {}, nil
		},
	}
	return h
}

// run 执行一条 setup 子命令；install、deploy、wipe 另带上临时的操作员身份目录。
func (h *installHarness) run(args ...string) (string, error) {
	h.t.Helper()
	cmd := newSetupCommand(h.deps)
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	args = append(args, "--file", "moox.toml")
	if slices.Contains([]string{"install", "update", "deploy", "wipe"}, args[0]) {
		args = append(args, "--operator-dir", h.operator)
	}
	if slices.Contains([]string{"install", "update", "deploy"}, args[0]) {
		args = append(args, "--source-dir", h.source)
	}
	cmd.SetArgs(args)
	err := cmd.Execute()
	h.stderr = stderr.String()
	return out.String(), err
}

func (h *installHarness) report(host string) *deployscan.HostReport {
	for i := range h.reports {
		if h.reports[i].Host == host {
			return &h.reports[i]
		}
	}
	h.t.Fatalf("没有主机 %s", host)
	return nil
}

func (h *installHarness) markInstalled() {
	for i := range h.reports {
		h.reports[i].Installed, h.reports[i].Version = true, json.RawMessage(`{"version":"v0.0.1"}`)
		h.reports[i].Entries, h.reports[i].Units = []string{"bin", "version.json"}, []string{"moox-host-gateway.service"}
	}
}

// 真实入口用的依赖不能有空函数：测试里注入假依赖时看不出这个问题，到真机上才空指针崩溃。
func TestDefaultSetupDepsHaveNoNilFunction(t *testing.T) {
	deps := reflect.ValueOf(completeSetupDeps(setupDeps{}))
	for i := range deps.NumField() {
		assert.False(t, deps.Field(i).IsNil(), "setupDeps.%s 没有默认值", deps.Type().Field(i).Name)
	}
}

func TestSetupInstallPreviewChangesNothing(t *testing.T) {
	h := newInstallHarness(t)
	out, err := h.run("install", "--space-id", "crypto")
	require.NoError(t, err)
	preview := parsePreview(t, out)
	require.Equal(t, "install", preview["action"])
	require.Equal(t, deployscan.RouteFresh, preview["route"])
	require.NotContains(t, preview, "wipe", "没装过时没有清空范围")
	require.Empty(t, h.calls, "预检不改任何东西")

	// 装过时预检带上清空范围（含归档与内置对象存储的大小）与提醒。
	h.markInstalled()
	h.report("factor").ArchiveKB, h.report("factor").SeaweedFSKB = 2048, 4096
	out, err = h.run("install", "--space-id", "crypto")
	require.NoError(t, err)
	preview = parsePreview(t, out)
	require.Equal(t, deployscan.RouteInstalled, preview["route"])
	wipe := preview["wipe"].(map[string]any)
	require.NotEmpty(t, wipe["digest"])
	require.Equal(t, deployscan.Notice, wipe["notice"])
	require.Empty(t, h.calls)
}

// 顺序：控制机先装 Admin 与主机级组件，再装带 EventBus、Storage 的主机，导入元数据，控制机其余组件，其余主机（因子机最后），
// 统一等一次全部就绪，然后才是完整初始化（告警渠道要等 Monitor 起来）、启动、验收。
func TestSetupInstallRunsStepsInDependencyOrder(t *testing.T) {
	h := newInstallHarness(t)
	out, err := h.run("install", "--space-id", "crypto", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"installed"`)
	ready := slices.DeleteFunc(slices.Clone(h.calls), func(call string) bool { return !strings.HasPrefix(call, "ready:") })
	require.Len(t, ready, 4, "最后对四台主机各等一次就绪")
	sequence := slices.DeleteFunc(slices.Clone(h.calls), func(call string) bool { return strings.HasPrefix(call, "ready:") })
	require.Equal(t, []string{
		"firewall",
		"deploy:control:admin,host-gateway,host-agent",
		"deploy:storage",
		"init-metadata",
		"deploy:control",
		"deploy:overseas",
		"deploy:factor",
		"init",
		"start",
		"verify",
	}, sequence)
	// 全部部署完之后才等就绪，完整初始化在等就绪之后。
	last := func(prefix string) int {
		at := -1
		for i, call := range h.calls {
			if strings.HasPrefix(call, prefix) {
				at = i
			}
		}
		return at
	}
	require.Less(t, last("deploy:"), slices.IndexFunc(h.calls, func(call string) bool { return strings.HasPrefix(call, "ready:") }))
	require.Less(t, last("ready:"), slices.Index(h.calls, "init"))
}

// 单机拓扑：控制机第一次就要带上 EventBus 与 Storage，否则导入元数据时 Storage 还不在。
func TestSetupInstallStepsKeepDependenciesOnControlInFirstPass(t *testing.T) {
	snapshot := testSetupSnapshot(t)
	steps := setupInstallSteps(snapshot.Manifest, "stockcn")
	require.Equal(t, installOperator, steps[0].Kind, "第一步准备这台电脑的 moox-cli 签名私钥，控制机 bootstrap 时登记它的公钥")
	require.Equal(t, installDeploy, steps[2].Kind)
	require.Equal(t, []string{"admin", "host-gateway", "host-agent", "eventbus", "storage-primary"}, steps[2].Components)
	require.Equal(t, installInitMetadata, steps[3].Kind)
}

// 重跑：已经装过时不带 --wipe 就是原地再做一遍，每一步照常执行、不出错，也不清空。
func TestSetupInstallRerunRepeatsStepsWithoutWiping(t *testing.T) {
	h := newInstallHarness(t)
	h.fail = "deploy:overseas"
	_, err := h.run("install", "--space-id", "crypto", "--yes")
	require.ErrorContains(t, err, "install_step_failed")
	require.ErrorContains(t, err, "moox-cli setup deploy --host overseas", "给出单独重跑这一步的命令")
	require.NotContains(t, h.calls, "init", "失败之后的步骤不执行")

	// 第一步生成了这台电脑的 moox-cli 私钥；重跑复用同一把，否则控制机上登记的公钥就对不上了。
	keyFile := filepath.Join(h.operator, identitybundle.OperatorKeyFile)
	firstKey, err := os.ReadFile(keyFile)
	require.NoError(t, err)

	h.fail, h.calls = "", nil
	h.markInstalled()
	_, err = h.run("install", "--space-id", "crypto", "--yes")
	require.NoError(t, err)
	again, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	require.Equal(t, firstKey, again, "重跑不换 moox-cli 的密钥")
	require.Contains(t, h.calls, "deploy:control:admin,host-gateway,host-agent")
	require.Contains(t, h.calls, "verify")
	for _, call := range h.calls {
		require.False(t, strings.HasPrefix(call, "wipe:"), "不带 --wipe 不清空")
	}
}

func TestSetupInstallWipesOnlyWithMatchingDigest(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
	// 上一套部署留下的操作员身份：moox-cli 的私钥、控制机根证书与它的指纹。
	oldKey, _, err := identitybundle.EnsureSigningKeyFile(filepath.Join(h.operator, identitybundle.OperatorKeyFile))
	require.NoError(t, err)
	for _, name := range []string{identitybundle.OperatorPinFile, identitybundle.OperatorCAFile} {
		require.NoError(t, os.WriteFile(filepath.Join(h.operator, name), []byte("old\n"), 0o600))
	}
	_, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", "sha256:other")
	require.ErrorContains(t, err, "wipe_scope_changed")
	require.Empty(t, h.calls)

	out, err := h.run("install", "--space-id", "crypto")
	require.NoError(t, err)
	digest := parsePreview(t, out)["wipe"].(map[string]any)["digest"].(string)
	_, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest)
	require.NoError(t, err)
	wiped := slices.DeleteFunc(slices.Clone(h.calls), func(call string) bool { return !strings.HasPrefix(call, "wipe:") })
	require.Len(t, wiped, 4)
	require.Less(t, slices.IndexFunc(h.calls, func(call string) bool { return strings.HasPrefix(call, "wipe:") }), slices.Index(h.calls, "firewall"))
	// 清空之后删掉这台电脑上的操作员身份；紧接着的安装第一步生成一把新的 moox-cli 私钥。
	require.NoFileExists(t, filepath.Join(h.operator, identitybundle.OperatorPinFile))
	require.NoFileExists(t, filepath.Join(h.operator, identitybundle.OperatorCAFile))
	newKey, err := identitybundle.ReadOperatorKey(h.operator)
	require.NoError(t, err)
	require.NotEqual(t, identitybundle.PublicKey(oldKey), identitybundle.PublicKey(newKey))

	// 没装过时带 --wipe 是多余的，直接拒绝。
	h.calls = nil
	_, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest)
	require.ErrorContains(t, err, "wipe_not_needed")
}

func TestSetupInstallStopsBeforeAnythingWhenLingerIsOff(t *testing.T) {
	h := newInstallHarness(t)
	h.report("overseas").Linger = false
	out, err := h.run("install", "--space-id", "crypto")
	require.NoError(t, err)
	manual := parsePreview(t, out)["manual_steps"].([]any)
	require.Len(t, manual, 1)
	require.Contains(t, manual[0], "sudo loginctl enable-linger ubuntu")
	_, err = h.run("install", "--space-id", "crypto", "--yes")
	require.ErrorContains(t, err, "manual_steps_required")
	require.Empty(t, h.calls)
}

func TestSetupInstallFailsWhenComponentsNeverBecomeReady(t *testing.T) {
	h := newInstallHarness(t)
	h.deps.waitReady = func(_ context.Context, host topology.Host, _ []string, _ time.Duration) (map[string]string, error) {
		if host.Name == "overseas" {
			return map[string]string{"trade": "readyz 503"}, nil
		}
		return nil, nil
	}
	_, err := h.run("install", "--space-id", "crypto", "--yes")
	require.ErrorContains(t, err, "overseas/trade（readyz 503）")
	require.NotContains(t, h.calls, "init")
}

// 覆盖更新：没装过时拒绝；预览按主机列出要更新、新装、删掉的组件，不动任何东西。
func TestSetupUpdatePreviewsPerHostComponents(t *testing.T) {
	h := newInstallHarness(t)
	_, err := h.run("update")
	require.ErrorContains(t, err, "update_not_installed")
	_, err = h.run("update", "--yes")
	require.ErrorContains(t, err, "update_not_installed")
	require.Empty(t, h.calls)

	h.markInstalled()
	// 因子机是新加的主机；控制机上还留着 moox.toml 里已经去掉的组件。
	*h.report("factor") = deployscan.HostReport{Host: "factor", Root: "/data/moox", Linger: true, Journald: true, DiskFreeKB: 50 << 20, ArchiveKB: -1, SeaweedFSKB: -1}
	control := h.report("control")
	control.Version = json.RawMessage(`{"version":"v0.0.1","components":{"admin":{},"host-gateway":{}}}`)
	control.Units = []string{"moox-admin.service", "moox-host-gateway.service", "moox-old.service"}
	// collector 从存储机挪到了控制机。
	h.report("storage").Units = append(h.report("storage").Units, "moox-collector.service")
	out, err := h.run("update")
	require.NoError(t, err)
	require.Empty(t, h.calls, "预览不改任何东西")
	preview := parsePreview(t, out)
	require.Equal(t, "update", preview["action"])
	hosts := map[string]map[string]any{}
	for _, host := range preview["hosts"].([]any) {
		hosts[host.(map[string]any)["host"].(string)] = host.(map[string]any)
	}
	require.Equal(t, "v0.0.1", hosts["control"]["version"])
	require.Contains(t, hosts["control"]["update"], "admin")
	require.Contains(t, hosts["control"]["install"], "monitor")
	require.Equal(t, []any{"old"}, hosts["control"]["remove"])
	require.Empty(t, hosts["factor"]["update"])
	require.Contains(t, hosts["factor"]["install"], "factor-engine")
	require.Contains(t, hosts["storage"]["remove"], "collector")
	require.Equal(t, []any{map[string]any{"component": "collector", "from": "storage", "to": "control"}}, preview["moves"], "换主机的组件单列：数据不迁移")
	notes := preview["notes"].([]any)
	require.Contains(t, notes[len(notes)-1], "主机 factor 还没装过")
	require.Contains(t, preview["note"], "setup init")
	for _, step := range preview["steps"].([]any) {
		kind := step.(map[string]any)["kind"]
		require.Contains(t, updateStepKinds, kind, "覆盖更新不初始化、不启动空间、不验收")
	}
}

// 覆盖更新的执行顺序与安装一致，但没有初始化、启动空间与验收，也不清空。
func TestSetupUpdateDeploysInInstallOrderWithoutInitOrStart(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
	out, err := h.run("update", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"updated"`)
	progress := slices.DeleteFunc(strings.Split(strings.TrimSpace(h.stderr), "\n"), func(line string) bool { return !strings.HasPrefix(line, "[") })
	require.Len(t, progress, 8)
	for i, line := range progress {
		require.True(t, strings.HasPrefix(line, fmt.Sprintf("[%d/8] ", i+1)), line)
	}
	ready := slices.DeleteFunc(slices.Clone(h.calls), func(call string) bool { return !strings.HasPrefix(call, "ready:") })
	require.Len(t, ready, 4, "最后对四台主机各等一次就绪")
	sequence := slices.DeleteFunc(slices.Clone(h.calls), func(call string) bool { return strings.HasPrefix(call, "ready:") })
	require.Equal(t, []string{
		"firewall",
		"deploy:control:admin,host-gateway,host-agent",
		"deploy:storage",
		"deploy:control",
		"deploy:overseas",
		"deploy:factor",
	}, sequence)

	// 一步失败时停下，报错带错误码和单独重跑这一步的命令。
	h.calls, h.fail = nil, "deploy:overseas"
	_, err = h.run("update", "--yes")
	require.ErrorContains(t, err, "update_step_failed")
	require.ErrorContains(t, err, "moox-cli setup deploy --host overseas")
	require.NotContains(t, h.calls, "deploy:factor")

	// 存储机没装过：是安装没做完，不覆盖更新。
	h.calls, h.fail = nil, ""
	storage := *h.report("storage")
	*h.report("storage") = deployscan.HostReport{Host: "storage", Root: "/data/moox", Linger: true, Journald: true, DiskFreeKB: 50 << 20}
	_, err = h.run("update", "--yes")
	require.ErrorContains(t, err, "update_not_installed")
	require.ErrorContains(t, err, "setup install")
	require.Empty(t, h.calls)
	*h.report("storage") = storage

	// 没开 linger 时什么都不做。
	h.calls, h.fail = nil, ""
	h.report("storage").Linger = false
	_, err = h.run("update", "--yes")
	require.ErrorContains(t, err, "manual_steps_required")
	require.Empty(t, h.calls)
}

func TestSetupWipePreviewsThenClearsEveryHostAndOperatorIdentity(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
	h.report("factor").ArchiveKB, h.report("factor").SeaweedFSKB = 2048, 4096
	operatorFiles := []string{identitybundle.OperatorKeyFile, identitybundle.OperatorPinFile, identitybundle.OperatorCAFile}
	for _, name := range append(operatorFiles, "unrelated.txt") {
		require.NoError(t, os.WriteFile(filepath.Join(h.operator, name), []byte("keep\n"), 0o600))
	}

	out, err := h.run("wipe")
	require.NoError(t, err)
	preview := parsePreview(t, out)
	plan := preview["plan"].(map[string]any)
	require.Equal(t, deployscan.Notice, plan["notice"])
	hosts := plan["hosts"].([]any)
	require.Len(t, hosts, 4)
	at := slices.IndexFunc(hosts, func(host any) bool { return host.(map[string]any)["host"] == "factor" })
	require.GreaterOrEqual(t, at, 0)
	factor := hosts[at].(map[string]any)
	require.EqualValues(t, 2048, factor["archive_kb"], "预览里单独列出归档目录的大小")
	require.EqualValues(t, 4096, factor["seaweedfs_kb"], "预览里单独列出内置对象存储数据的大小")
	require.Empty(t, h.calls, "预览不清空")
	require.Contains(t, preview["note"], "主机 overseas 上有 Trade：清空会删除 Trade 库，先确认没有持仓和挂单")
	require.Contains(t, preview["left_behind"], "云上的采集计划函数（moox-fetch-plan- 开头；要删加 --clean-functions）", "预览列出卸载不会动的东西")
	require.Contains(t, preview["left_behind"], "COS 桶里的云函数代码包（带 --clean-functions 也不删）")
	require.FileExists(t, filepath.Join(h.operator, identitybundle.OperatorKeyFile))

	_, err = h.run("wipe", "--yes")
	require.ErrorContains(t, err, "--expect")
	_, err = h.run("wipe", "--yes", "--expect", "sha256:other")
	require.ErrorContains(t, err, "wipe_scope_changed")
	require.Empty(t, h.calls)

	out, err = h.run("wipe", "--yes", "--expect", plan["digest"].(string))
	require.NoError(t, err)
	require.Contains(t, out, `"status":"wiped"`)
	require.ElementsMatch(t, []string{"wipe:control", "wipe:storage", "wipe:overseas", "wipe:factor"}, h.calls)
	// moox-cli 的私钥、控制机根证书与它的指纹都删掉：重装的控制机是新的 CA，操作身份也重新生成。
	for _, name := range operatorFiles {
		require.NoFileExists(t, filepath.Join(h.operator, name))
	}
	require.FileExists(t, filepath.Join(h.operator, "unrelated.txt"), "只删操作员身份")
}

// 一台清空失败时报错并保留操作员身份，重跑接着清。
func TestSetupWipeReportsFailedHostAndKeepsOperatorIdentity(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
	require.NoError(t, os.WriteFile(filepath.Join(h.operator, identitybundle.OperatorKeyFile), []byte("keep\n"), 0o600))
	out, err := h.run("wipe")
	require.NoError(t, err)
	digest := parsePreview(t, out)["plan"].(map[string]any)["digest"].(string)
	h.fail = "wipe:storage"
	_, err = h.run("wipe", "--yes", "--expect", digest)
	require.ErrorContains(t, err, "boom wipe:storage")
	require.FileExists(t, filepath.Join(h.operator, identitybundle.OperatorKeyFile))
}

func TestSetupDeployPassesHostComponentsAndSources(t *testing.T) {
	h := newInstallHarness(t)
	var got deploy.Options
	h.deps.deployHost = func(_ context.Context, options deploy.Options) (deploy.Result, error) {
		got = options
		return deploy.Result{Host: options.HostID, Components: []deploy.ComponentResult{{ID: "monitor", Action: "restarted", Ready: true}}}, nil
	}
	binaries := t.TempDir()
	out, err := h.run("deploy", "--host", "control", "--component", "monitor,collector", "--binary-dir", binaries)
	require.NoError(t, err)
	require.Contains(t, out, `"action":"restarted"`)
	require.Equal(t, "control", got.HostID)
	require.Equal(t, []string{"monitor", "collector"}, got.Components)
	require.Equal(t, binaries, got.BinaryDirectory)
	require.Equal(t, h.operator, got.OperatorDirectory)
	require.NotNil(t, got.SyncHost)
	require.NotNil(t, got.Dial)
	require.Nil(t, got.Bundle, "源码目录里没有产物包清单")

	// 部署失败时已有的结果照样输出，退出码非 0。
	h.deps.deployHost = func(_ context.Context, options deploy.Options) (deploy.Result, error) {
		return deploy.Result{Host: options.HostID, Components: []deploy.ComponentResult{{ID: "monitor", Action: "started", Detail: "readyz 503"}}}, errors.New("not_ready: monitor")
	}
	out, err = h.run("deploy", "--host", "control")
	require.ErrorContains(t, err, "not_ready")
	require.Contains(t, out, "readyz 503")
}

func TestSetupOperateCommandsPreviewAndValidateComponent(t *testing.T) {
	h := newInstallHarness(t)
	var operations []string
	h.deps.operate = func(_ context.Context, host topology.Host, component, operation string) (string, error) {
		operations = append(operations, host.Name+"/"+component+"/"+operation)
		return "active", nil
	}
	for _, command := range []string{"restart", "stop", "start-component"} {
		before := len(operations)
		out, err := h.run(command, "--host", "factor", "--component", "seaweedfs")
		require.NoError(t, err)
		require.Equal(t, "moox-seaweedfs.service", parsePreview(t, out)["unit"], "内置对象存储也是这台主机上的一个组件")
		require.Len(t, operations, before, "没有 --yes 只预览")
		_, err = h.run(command, "--host", "factor", "--component", "seaweedfs", "--yes")
		require.NoError(t, err)
	}
	require.Equal(t, []string{"factor/seaweedfs/restart", "factor/seaweedfs/stop", "factor/seaweedfs/start"}, operations)
	_, err := h.run("restart", "--host", "storage", "--component", "monitor", "--yes")
	require.ErrorContains(t, err, "主机 storage 上没有组件 monitor")
	_, err = h.run("restart", "--host", "nowhere", "--component", "monitor", "--yes")
	require.ErrorContains(t, err, "没有主机 nowhere")
}

func TestSetupLogsReadsOneComponentOrWholeHost(t *testing.T) {
	h := newInstallHarness(t)
	var got []string
	var gotJournal bool
	h.deps.readLogs = func(_ context.Context, host topology.Host, root string, components []string, pattern string, lines int, journal bool) ([]deploy.LogSection, error) {
		require.Equal(t, "/data/moox", root)
		got, gotJournal = components, journal
		return []deploy.LogSection{{Component: components[0], Source: "/data/moox/logs/" + components[0] + "/trpc.log", Text: "line\n"}}, nil
	}
	out, err := h.run("logs", "--host", "storage", "--component", "eventbus", "--journal")
	require.NoError(t, err)
	require.Equal(t, []string{"eventbus"}, got)
	require.True(t, gotJournal)
	require.Contains(t, out, "trpc.log")
	_, err = h.run("logs", "--host", "storage")
	require.NoError(t, err)
	require.Contains(t, got, "host-gateway")
	require.Contains(t, got, "storage-view")
	_, err = h.run("logs", "--host", "storage", "--component", "trade")
	require.ErrorContains(t, err, "主机 storage 上没有组件 trade")
}

// 一次安装里的种子、云函数代码包都取自 --source-dir，不看运行命令时的当前目录；预检给出的启动命令不带 setup start 没有的 --yes。
func TestSetupInstallUsesOneSourceDirectory(t *testing.T) {
	h := newInstallHarness(t)
	_, err := h.run("install", "--space-id", "crypto", "--yes")
	require.NoError(t, err)
	root, err := filepath.Abs(h.source)
	require.NoError(t, err)
	require.Equal(t, []string{root}, h.initSources)
	require.Equal(t, []string{filepath.Join(root, "config", "setup"), filepath.Join(root, "config", "setup")}, h.seedDirs)

	for _, step := range setupInstallSteps(h.snapshot.Manifest, "crypto") {
		if step.Kind == installStart {
			require.Equal(t, "moox-cli setup start --space-id crypto", step.Command)
		}
	}
}

// setup init 的 --source-dir 传到完整初始化；写了 --config-dir 时种子按它读。
func TestSetupInitPassesSourceDirectory(t *testing.T) {
	h := newInstallHarness(t)
	source := t.TempDir()
	_, err := h.run("init", "--source-dir", source)
	require.NoError(t, err)
	require.Equal(t, []string{source}, h.initSources)
	require.Equal(t, []string{filepath.Join(source, "config", "setup")}, h.seedDirs)

	_, err = h.run("init", "--source-dir", source, "--config-dir", "seeds")
	require.NoError(t, err)
	require.Equal(t, "seeds", h.seedDirs[len(h.seedDirs)-1])
}

// 统一等就绪按是否停用排除组件：崩溃后正在自动重启的（单元在、没在运行、没被停用）也要等；被停用的不等。
func TestSetupInstallWaitsForEveryComponentThatIsNotDisabled(t *testing.T) {
	h := newInstallHarness(t)
	overseas := h.report("overseas")
	overseas.Units = []string{"moox-trade.service", "moox-egress-proxy.service", "moox-callback-dispatcher.service"}
	overseas.Active = []string{"moox-egress-proxy.service"}
	overseas.Disabled = []string{"moox-callback-dispatcher.service"}
	waited := map[string][]string{}
	h.deps.waitReady = func(_ context.Context, host topology.Host, components []string, _ time.Duration) (map[string]string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		waited[host.Name] = components
		return nil, nil
	}
	require.NoError(t, waitSetupAllReady(context.Background(), h.deps, h.snapshot))
	require.Contains(t, waited["overseas"], "trade", "没在运行但没被停用的组件要等")
	require.Contains(t, waited["overseas"], "egress-proxy")
	require.NotContains(t, waited["overseas"], "callback-dispatcher", "被停用的组件不等")
}
