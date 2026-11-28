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
	setupbackup "github.com/mooyang-code/moox/modules/cli/internal/setup/backup"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	setupdataformat "github.com/mooyang-code/moox/modules/cli/internal/setup/dataformat"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	pkgdataformat "github.com/mooyang-code/moox/packages/dataformat"
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
overseas = ["trade", "egress-proxy", "collector-stream"]
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

// installHarness 把部署、初始化、启动、验收、备份与完成标记都换成记录调用；reports 是只读检测的结果，清空后变成空机。
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
	// file 是临时目录里的 moox.toml 路径，保证测试写 moox.backup 不污染仓库工作区。
	file string
	// previewSecrets/previewChannels/previewBackupErr 控制清空前备份预览行为。
	previewSecrets   int
	previewChannels  int
	previewBackupErr error
	// backupBeforeWipeErr 控制清空前自动备份失败。
	backupBeforeWipeErr error
	// restoreBackupErr 控制安装自动导入备份失败。
	restoreBackupErr error
	// seedDirs 是读种子用过的目录，initSources 是完整初始化收到的源目录。
	seedDirs, initSources []string
	// stderr 是最近一次 run 的标准错误（进度行在这里）。
	stderr string
}

func newInstallHarness(t *testing.T) *installHarness {
	t.Helper()
	restoreKeys := configcrypt.SetDefaultForTest(&configcrypt.MemoryStore{})
	t.Cleanup(restoreKeys)

	workDir := t.TempDir()
	h := &installHarness{
		t:               t,
		snapshot:        fourHostSnapshot(t),
		operator:        t.TempDir(),
		file:            filepath.Join(workDir, "moox.toml"),
		previewSecrets:  3,
		previewChannels: 2,
	}
	// setup start 要读源码目录里的内置采集任务与因子种子。
	h.source = startRepository(t)
	seedDir := filepath.Join(h.source, "config", "setup")
	writeLibrary(t, filepath.Join(seedDir, "factors"), "lib.yaml")
	require.NoError(t, os.WriteFile(filepath.Join(seedDir, "factor-tasks.yaml"), []byte(testSeed), 0o600))
	for _, host := range h.snapshot.Manifest.Hosts() {
		h.reports = append(h.reports, deployscan.HostReport{Host: host.Name, Root: "/data/moox", Linger: true, Journald: true, DiskFreeKB: 50 << 20, ArchiveKB: -1})
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
			// 与 deployscan.Scan 一致：点名只看那一台；不点名时停用的主机只在清空时检测。
			return slices.DeleteFunc(slices.Clone(h.reports), func(report deployscan.HostReport) bool {
				if options.Host != "" {
					return report.Host != options.Host
				}
				return !h.snapshot.Manifest.HostByID(report.Host).Enabled && !options.Wipe
			}), nil
		},
		wipeHost: func(_ context.Context, host topology.Host, root string) (deployscan.HostResult, error) {
			if err := record("wipe:" + host.Name); err != nil {
				return deployscan.HostResult{}, err
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			for i := range h.reports {
				if h.reports[i].Host == host.Name {
					h.reports[i].Entries, h.reports[i].Units, h.reports[i].Active = nil, nil, nil
					h.reports[i].Installed, h.reports[i].InstallComplete, h.reports[i].Version = false, false, nil
					h.reports[i].DataFormats = nil
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
		initStorage: func(_ context.Context, snapshot *setupSnapshot, _, sourceRoot string, _ setupInitBundle) (setupInitSummary, error) {
			h.initSources = append(h.initSources, sourceRoot)
			console := describeSetupConsoleLinks(snapshot)
			return setupInitSummary{
				Notification: &setupNotificationSummary{
					Status:  notificationEmpty,
					Warning: fmt.Sprintf("尚未配置告警通知渠道，系统异常时不会推送通知；请打开管理台「资源与运维 › 监控告警」添加：%s", console.MonitorURL),
				},
				Console: console,
			}, record("init")
		},
		openInitFactor: func(context.Context, *setupSnapshot) (setupInitFactor, error) { return &fakeSetupInitFactor{}, nil },
		ensureArchiveTasks: func(context.Context, *setupSnapshot, string) (*setupArchiveTasksSummary, error) {
			return &setupArchiveTasksSummary{Enabled: true}, nil
		},
		verifySpace: func(context.Context, string, *setupSnapshot, string) error { return record("verify") },
		markInstallComplete: func(_ context.Context, _ *setupSnapshot, _ string) error {
			if err := record("mark-complete"); err != nil {
				return err
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			for i := range h.reports {
				if h.reports[i].Host == "control" {
					h.reports[i].InstallComplete = true
				}
			}
			return nil
		},
		previewBackup: func(context.Context, string, *setupSnapshot) (int, int, error) {
			if h.previewBackupErr != nil {
				return 0, 0, h.previewBackupErr
			}
			return h.previewSecrets, h.previewChannels, nil
		},
		backupBeforeWipe: func(_ context.Context, _ string, _ *setupSnapshot, backupPath string) (setupBackupOutput, error) {
			if err := record("backup-before-wipe"); err != nil {
				return setupBackupOutput{}, err
			}
			if h.backupBeforeWipeErr != nil {
				return setupBackupOutput{}, h.backupBeforeWipeErr
			}
			creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), true)
			if err != nil {
				return setupBackupOutput{}, err
			}
			defer creds.Clear()
			channels := make([]setupbackup.Channel, h.previewChannels)
			for i := range channels {
				channels[i] = setupbackup.Channel{ChannelType: "wecom", WebhookURL: fmt.Sprintf("https://example.com/hook/%d", i)}
			}
			sealed, err := setupbackup.SealPending(`{"format":"moox.secrets/v1","ciphertext":"auto"}`, h.previewSecrets, channels, creds, time.Now())
			if err != nil {
				return setupBackupOutput{}, err
			}
			if err := setupbackup.WriteGuarded(backupPath, sealed); err != nil {
				return setupBackupOutput{}, err
			}
			return setupBackupOutput{Path: backupPath, SecretCount: h.previewSecrets, ChannelCount: h.previewChannels}, nil
		},
		restoreBackup: func(_ context.Context, file string, snapshot *setupSnapshot) (*setupRestoreBackupSummary, error) {
			if err := record("restore-backup"); err != nil {
				return nil, err
			}
			if h.restoreBackupErr != nil {
				return nil, h.restoreBackupErr
			}
			backupPath := setupBackupPath(file, snapshot.Manifest)
			meta, exists, err := setupbackup.ReadMetadata(backupPath)
			if err != nil {
				return nil, err
			}
			if !exists || !meta.PendingImport {
				return &setupRestoreBackupSummary{Status: "no_backup", Path: backupPath, Note: "无待导入备份，请到管理台录入"}, nil
			}
			if err := setupbackup.MarkImported(backupPath, time.Now()); err != nil {
				return nil, err
			}
			return &setupRestoreBackupSummary{
				Status:          "imported",
				Path:            backupPath,
				SecretsCreated:  int32(meta.Secrets.Count),
				ChannelsCreated: int32(meta.NotificationChannels.Count),
				Note:            fmt.Sprintf("已导入 %d 把密钥、%d 个告警渠道", meta.Secrets.Count, meta.NotificationChannels.Count),
			}, nil
		},
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

func (h *installHarness) backupPath() string {
	return topology.BackupPathFor(h.file)
}

// run 执行一条 setup 子命令；install、deploy、wipe 另带上临时的操作员身份目录。
func (h *installHarness) run(args ...string) (string, error) {
	h.t.Helper()
	cmd := newSetupCommand(h.deps)
	var out, stderr bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&stderr)
	args = append(args, "--file", h.file)
	if slices.Contains([]string{"install", "deploy", "wipe"}, args[0]) {
		args = append(args, "--operator-dir", h.operator)
	}
	if slices.Contains([]string{"install", "deploy"}, args[0]) {
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

// markIncomplete 把各主机标记为有 version.json 但控制机无完成标记（没装完状态）。
func (h *installHarness) markIncomplete() {
	for i := range h.reports {
		h.reports[i].Installed = true
		h.reports[i].InstallComplete = false
		h.reports[i].Version = json.RawMessage(`{"version":"v0.0.1"}`)
		h.reports[i].Entries, h.reports[i].Units = []string{"bin", "version.json"}, []string{"moox-host-gateway.service"}
	}
}

// markInstalled 把各主机标记为已装好（控制机带有 install-complete.json 完成标记）。
func (h *installHarness) markInstalled() {
	h.markIncomplete()
	h.report("control").InstallComplete = true
	h.report("control").Entries = append(h.report("control").Entries, deploy.InstallCompleteFile)
}

// 真实入口用的依赖不能有空函数：测试里注入假依赖时看不出这个问题，到真机上才空指针崩溃。
func TestDefaultSetupDepsHaveNoNilFunction(t *testing.T) {
	deps := reflect.ValueOf(completeSetupDeps(setupDeps{}))
	for i := range deps.NumField() {
		assert.False(t, deps.Field(i).IsNil(), "setupDeps.%s 没有默认值", deps.Type().Field(i).Name)
	}
}

// 三种状态（fresh、incomplete、installed）各自的预检内容。
func TestSetupInstallPreviewCoversThreeStatesAndFormatComparison(t *testing.T) {
	h := newInstallHarness(t)

	// 1. fresh（没装过）：直接展示新安装步骤，无 wipe 选项，不动任何主机。
	out, err := h.run("install", "--space-id", "crypto")
	require.NoError(t, err)
	preview := parsePreview(t, out)
	require.Equal(t, "install", preview["action"])
	require.Equal(t, deployscan.RouteFresh, preview["route"])
	require.NotContains(t, preview, "wipe", "没装过时没有清空范围")
	require.NotContains(t, preview, "options")
	require.Empty(t, h.calls, "预检不改任何东西")

	// 2. incomplete（没装完）：并排列出 keep（resume）与 wipe，带数据格式比对与备份条数。
	h.markIncomplete()
	h.report("factor").ArchiveKB = 2048
	out, err = h.run("install", "--space-id", "crypto")
	require.NoError(t, err)
	preview = parsePreview(t, out)
	require.Equal(t, deployscan.RouteIncomplete, preview["route"])
	opts := preview["options"].(map[string]any)
	keepOpt := opts["keep"].(map[string]any)
	wipeOpt := opts["wipe"].(map[string]any)
	require.Equal(t, true, keepOpt["available"])
	require.Equal(t, "resume", keepOpt["mode"])
	require.Equal(t, true, wipeOpt["available"])
	wipeBackup := wipeOpt["backup"].(map[string]any)
	require.Equal(t, "will_backup", wipeBackup["status"])
	require.EqualValues(t, 3, wipeBackup["secret_count"])
	require.EqualValues(t, 2, wipeBackup["channel_count"])
	require.Contains(t, preview["note"], "归档文件")
	require.Contains(t, preview["note"], "唯一可能补不回来")
	require.Empty(t, h.calls)

	// 3. installed（已装好）：不带 --space-id 也能预检，keep 走 update 视图（含 hosts/moves/update 步骤），wipe 带备份与清空摘要。
	h.markInstalled()
	*h.report("factor") = deployscan.HostReport{Host: "factor", Root: "/data/moox", Linger: true, Journald: true, DiskFreeKB: 50 << 20, ArchiveKB: -1}
	control := h.report("control")
	control.Version = json.RawMessage(`{"version":"v0.0.1","components":{"admin":{},"host-gateway":{}}}`)
	control.Units = []string{"moox-admin.service", "moox-host-gateway.service", "moox-old.service"}
	h.report("storage").Units = append(h.report("storage").Units, "moox-collector.service")

	out, err = h.run("install")
	require.NoError(t, err)
	preview = parsePreview(t, out)
	require.Equal(t, deployscan.RouteInstalled, preview["route"])
	opts = preview["options"].(map[string]any)
	keepOpt = opts["keep"].(map[string]any)
	require.Equal(t, true, keepOpt["available"])
	require.Equal(t, "update", keepOpt["mode"])
	hosts := map[string]map[string]any{}
	for _, host := range keepOpt["hosts"].([]any) {
		hosts[host.(map[string]any)["host"].(string)] = host.(map[string]any)
	}
	require.Equal(t, "v0.0.1", hosts["control"]["version"])
	require.Contains(t, hosts["control"]["update"], "admin")
	require.Contains(t, hosts["control"]["install"], "monitor")
	require.Equal(t, []any{"old"}, hosts["control"]["remove"])
	require.Equal(t, []any{map[string]any{"component": "collector", "from": "storage", "to": "control"}}, keepOpt["moves"])
	for _, step := range keepOpt["steps"].([]any) {
		require.Contains(t, updateStepKinds, step.(map[string]any)["kind"], "已装好保留更新不初始化、不导入备份、不启动空间、不验收")
	}
	require.Empty(t, h.calls)
}

// 装过的目标机带 --yes 但未选 --keep 或 --wipe 时直接拒绝，且不动任何主机；同时传两者也拒绝。
func TestSetupInstallRejectsAmbiguousChoiceWhenInstalledOrIncomplete(t *testing.T) {
	for _, state := range []string{"incomplete", "installed"} {
		t.Run(state, func(t *testing.T) {
			h := newInstallHarness(t)
			if state == "incomplete" {
				h.markIncomplete()
			} else {
				h.markInstalled()
			}
			_, err := h.run("install", "--space-id", "crypto", "--yes")
			require.ErrorContains(t, err, "install_choice_required")
			require.Empty(t, h.calls, "未作选择时不得触碰任何主机")

			_, err = h.run("install", "--space-id", "crypto", "--yes", "--keep", "--wipe", "sha256:abc")
			require.ErrorContains(t, err, "install_choice_conflict")
			require.Empty(t, h.calls)
		})
	}
}

// 数据格式有变化或未知时：预检标出不兼容组件且 keep.available=false，--keep --yes 被拒绝且不动任何主机。
func TestSetupInstallKeepRejectedOnDataFormatMismatch(t *testing.T) {
	staleRec, err := pkgdataformat.Build("admin", map[string]string{pkgdataformat.PartSQLiteSchema: "sha256:old"})
	require.NoError(t, err)
	staleRaw, err := json.Marshal(staleRec)
	require.NoError(t, err)

	for name, formats := range map[string]map[string]deployscan.ComponentDataFormat{
		"changed": {"admin": {HasData: true, FormatText: string(staleRaw)}},
		"unknown": {"monitor": {HasData: true, FormatText: ""}},
	} {
		t.Run(name, func(t *testing.T) {
			h := newInstallHarness(t)
			h.markInstalled()
			h.report("control").DataFormats = formats

			out, err := h.run("install")
			require.NoError(t, err)
			preview := parsePreview(t, out)
			df := preview["data_format"].(map[string]any)
			require.Equal(t, false, df["can_keep"])
			require.NotEmpty(t, df["incompatible"])
			keepOpt := preview["options"].(map[string]any)["keep"].(map[string]any)
			require.Equal(t, false, keepOpt["available"])
			require.Contains(t, keepOpt["reject_reason"], "data_format_incompatible")

			_, err = h.run("install", "--keep", "--yes")
			require.ErrorContains(t, err, "data_format_incompatible")
			if name == "changed" {
				require.ErrorContains(t, err, "control/admin")
			} else {
				require.ErrorContains(t, err, "control/monitor")
			}
			require.Empty(t, h.calls, "格式不兼容拒绝保留时不得触碰任何主机")
		})
	}
}

// 顺序：控制机先装 Admin 与主机级组件，再装带 EventBus、Storage 的主机，导入元数据，控制机其余组件，其余主机（因子机最后），
// 统一等一次全部就绪，自动导入待恢复备份（在完整初始化之前！），然后是完整初始化、启动、验收，最后在控制机写完成标记。
func TestSetupInstallFreshRunsStepsInDependencyOrderAndMarksComplete(t *testing.T) {
	h := newInstallHarness(t)
	out, err := h.run("install", "--space-id", "crypto", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"installed"`)
	require.Contains(t, out, `"mode":"fresh"`)
	require.Contains(t, out, `"url":"https://203.0.113.10:9527"`)
	require.Contains(t, out, `"secrets_url":"https://203.0.113.10:9527/#/settings/secrets"`)
	require.Contains(t, out, `"monitor_url":"https://203.0.113.10:9527/#/ops/monitor"`)
	require.Contains(t, h.stderr, "https://203.0.113.10:9527/#/settings/secrets")
	require.Contains(t, h.stderr, "https://203.0.113.10:9527/#/ops/monitor")
	require.Contains(t, h.stderr, "尚未配置告警通知渠道")
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
		"restore-backup",
		"init",
		"start",
		"verify",
		"mark-complete",
	}, sequence)
	// 全部部署完之后才等就绪，自动导入备份在等就绪之后、完整初始化之前。
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
	require.Less(t, last("ready:"), slices.Index(h.calls, "restore-backup"))
	require.Less(t, slices.Index(h.calls, "restore-backup"), slices.Index(h.calls, "init"))
	require.True(t, h.report("control").InstallComplete, "验收通过后应在控制机写出完成标记")
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

// 没装完（incomplete）时选 --keep：接着跑完安装流程并写出完成标记，数据保留不清空，复用操作员密钥。
func TestSetupInstallIncompleteKeepResumesAndMarksComplete(t *testing.T) {
	h := newInstallHarness(t)
	h.fail = "deploy:overseas"
	_, err := h.run("install", "--space-id", "crypto", "--yes")
	require.ErrorContains(t, err, "install_step_failed")
	require.ErrorContains(t, err, "moox-cli setup deploy --host overseas", "给出单独重跑这一步的命令")
	require.NotContains(t, h.calls, "init", "失败之后的步骤不执行")
	require.NotContains(t, h.calls, "mark-complete", "失败时不能写完成标记")

	keyFile := filepath.Join(h.operator, identitybundle.OperatorKeyFile)
	firstKey, err := os.ReadFile(keyFile)
	require.NoError(t, err)

	// 模拟首次部署在 control 完成后中断，Admin 已有匹配格式的数据。
	expected, err := setupdataformat.Expected()
	require.NoError(t, err)
	adminFmt, err := json.Marshal(expected["admin"])
	require.NoError(t, err)
	h.fail, h.calls = "", nil
	h.markIncomplete()
	h.report("control").DataFormats = map[string]deployscan.ComponentDataFormat{
		"admin": {HasData: true, FormatText: string(adminFmt)},
	}

	out, err := h.run("install", "--space-id", "crypto", "--keep", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"installed"`)
	require.Contains(t, out, `"mode":"resume"`)
	again, err := os.ReadFile(keyFile)
	require.NoError(t, err)
	require.Equal(t, firstKey, again, "续装不换 moox-cli 的密钥")
	require.Contains(t, h.calls, "deploy:control:admin,host-gateway,host-agent")
	require.Contains(t, h.calls, "restore-backup")
	require.Contains(t, h.calls, "verify")
	require.Contains(t, h.calls, "mark-complete")
	for _, call := range h.calls {
		require.False(t, strings.HasPrefix(call, "wipe:"), "选 --keep 不清空")
	}
}

// 已装好（installed）时选 --keep：不需要 --space-id，只换二进制与配置（不初始化、不导入备份、不启动空间、不验收、不重写完成标记）。
func TestSetupInstallInstalledKeepUpdatesWithoutSpaceIDOrInit(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()

	out, err := h.run("install", "--keep", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"updated"`)
	require.Contains(t, out, `"mode":"keep"`)
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
	require.NotContains(t, h.calls, "init-metadata")
	require.NotContains(t, h.calls, "restore-backup")
	require.NotContains(t, h.calls, "init")
	require.NotContains(t, h.calls, "start")
	require.NotContains(t, h.calls, "verify")
	require.NotContains(t, h.calls, "mark-complete")

	// 一步失败时停下，报错带错误码和单独重跑这一步的命令。
	h.calls, h.fail = nil, "deploy:overseas"
	_, err = h.run("install", "--keep", "--yes")
	require.ErrorContains(t, err, "install_step_failed")
	require.ErrorContains(t, err, "moox-cli setup deploy --host overseas")
	require.NotContains(t, h.calls, "deploy:factor")

	// 没开 linger 时什么都不做。
	h.calls, h.fail = nil, ""
	h.report("storage").Linger = false
	_, err = h.run("install", "--keep", "--yes")
	require.ErrorContains(t, err, "manual_steps_required")
	require.Empty(t, h.calls)
}

// 全部覆盖（--wipe）：校验摘要、先自动备份并打待导入标记、清空全部主机与操作员身份、全新安装、在就绪后且完整初始化前自动导入并清标记。
func TestSetupInstallWipesBacksUpAndAutoRestores(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
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

	// 备份失败时停下不清任何主机。
	h.backupBeforeWipeErr = errors.New("admin down")
	_, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest)
	require.ErrorContains(t, err, "backup_failed")
	require.Equal(t, []string{"backup-before-wipe"}, h.calls, "备份失败后不得清空任何主机")
	h.backupBeforeWipeErr, h.calls = nil, nil

	// 正常 --wipe：自动备份 → 清空 → 全新安装 → 自动导入 → 写完成标记。
	out, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest)
	require.NoError(t, err)
	require.Contains(t, out, `"status":"installed"`)
	require.Contains(t, out, `"mode":"wipe"`)
	require.Contains(t, out, `"status":"imported"`)
	wiped := slices.DeleteFunc(slices.Clone(h.calls), func(call string) bool { return !strings.HasPrefix(call, "wipe:") })
	require.Len(t, wiped, 4)
	require.Less(t, slices.Index(h.calls, "backup-before-wipe"), slices.IndexFunc(h.calls, func(call string) bool { return strings.HasPrefix(call, "wipe:") }))
	require.Less(t, slices.IndexFunc(h.calls, func(call string) bool { return strings.HasPrefix(call, "wipe:") }), slices.Index(h.calls, "firewall"))
	require.Less(t, slices.Index(h.calls, "restore-backup"), slices.Index(h.calls, "init"))
	require.Contains(t, h.calls, "mark-complete")

	// 备份已清掉待导入标记并记下 imported_at。
	meta, exists, err := setupbackup.ReadMetadata(h.backupPath())
	require.NoError(t, err)
	require.True(t, exists)
	require.False(t, meta.PendingImport)
	require.NotEmpty(t, meta.ImportedAt)

	// 清空之后删掉旧操作员身份并生成新的 moox-cli 私钥。
	require.NoFileExists(t, filepath.Join(h.operator, identitybundle.OperatorPinFile))
	require.NoFileExists(t, filepath.Join(h.operator, identitybundle.OperatorCAFile))
	newKey, err := identitybundle.ReadOperatorKey(h.operator)
	require.NoError(t, err)
	require.NotEqual(t, identitybundle.PublicKey(oldKey), identitybundle.PublicKey(newKey))

	// 没装过时带 --wipe 与 --keep 都拒绝。
	h.calls = nil
	for i := range h.reports {
		h.reports[i].Installed, h.reports[i].InstallComplete, h.reports[i].Version = false, false, nil
		h.reports[i].Entries, h.reports[i].Units = nil, nil
	}
	_, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest)
	require.ErrorContains(t, err, "wipe_not_needed")
	_, err = h.run("install", "--space-id", "crypto", "--yes", "--keep")
	require.ErrorContains(t, err, "keep_not_needed")
}

// 2C-6 防误覆盖：卸载 → 安装中途失败 → 再卸载，不得用半成品覆盖已有待导入备份；
// 且 --no-backup 卸载时会给本机已有手动备份打上待导入标记，重装后自动导入。
func TestSetupWipePendingBackupProtectionAndNoBackupMarksExisting(t *testing.T) {
	t.Run("mid_install_failure_then_rewipe_preserves_good_backup", func(t *testing.T) {
		h := newInstallHarness(t)
		h.markInstalled()
		h.previewSecrets, h.previewChannels = 5, 2

		out, err := h.run("install", "--space-id", "crypto")
		require.NoError(t, err)
		digest := parsePreview(t, out)["wipe"].(map[string]any)["digest"].(string)

		// 第一次全部覆盖：备份成功写下 5 把密钥、2 个渠道，清空完成，安装中途在 overseas 失败。
		h.fail = "deploy:overseas"
		_, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest)
		require.ErrorContains(t, err, "install_step_failed")

		// 备份文件仍带有待导入标记，且内容是完好的 5 把密钥、2 个渠道。
		meta, exists, err := setupbackup.ReadMetadata(h.backupPath())
		require.NoError(t, err)
		require.True(t, exists)
		require.True(t, meta.PendingImport)
		require.Equal(t, 5, meta.Secrets.Count)

		// 模拟安装中途已经把 control 装了一半（Admin 里只剩 1 把腾讯云密钥），再次 --wipe。
		h.fail, h.calls = "", nil
		h.markIncomplete()
		h.previewSecrets, h.previewChannels = 1, 0

		out, err = h.run("install", "--space-id", "crypto")
		require.NoError(t, err)
		preview := parsePreview(t, out)
		backupPreview := preview["backup"].(map[string]any)
		require.Equal(t, "reuse_pending", backupPreview["status"])
		require.EqualValues(t, 5, backupPreview["secret_count"], "预览应显示将沿用原 5 把密钥的待导入备份")
		digest2 := preview["wipe"].(map[string]any)["digest"].(string)

		out, err = h.run("install", "--space-id", "crypto", "--yes", "--wipe", digest2)
		require.NoError(t, err)
		require.NotContains(t, h.calls, "backup-before-wipe", "已有待导入备份时再卸载绝不能重新导出覆盖")
		require.Contains(t, out, `"secrets_created":5`)
		require.Contains(t, out, `"channels_created":2`)

		// 导入成功后标记清除。
		meta, exists, err = setupbackup.ReadMetadata(h.backupPath())
		require.NoError(t, err)
		require.True(t, exists)
		require.False(t, meta.PendingImport)
	})

	t.Run("no_backup_wipe_marks_existing_manual_backup_for_auto_import", func(t *testing.T) {
		h := newInstallHarness(t)
		h.markInstalled()

		// 先写一份没有待导入标记的手动备份。
		creds, err := setupbackup.DeriveCredentials(configcrypt.Default(), true)
		require.NoError(t, err)
		defer creds.Clear()
		manualRaw, err := setupbackup.Seal(`{"format":"moox.secrets/v1","ciphertext":"manual"}`, 4, []setupbackup.Channel{
			{ChannelType: "feishu", WebhookURL: "https://open.feishu.cn/hook/manual", Secret: "sec"},
		}, creds, time.Now())
		require.NoError(t, err)
		require.NoError(t, setupbackup.WriteAtomic(h.backupPath(), manualRaw))

		// Admin 起不来时预览依然成功（给出 digest 和 unavailable 说明），再带 --no-backup 卸载。
		h.previewBackupErr = errors.New("admin unreachable")
		out, err := h.run("wipe")
		require.NoError(t, err)
		preview := parsePreview(t, out)
		require.Equal(t, "unavailable", preview["backup"].(map[string]any)["status"])
		digest := preview["plan"].(map[string]any)["digest"].(string)

		out, err = h.run("wipe", "--yes", "--expect", digest, "--no-backup")
		require.NoError(t, err)
		require.Contains(t, out, `"status":"wiped"`)
		require.Contains(t, out, `"status":"marked_existing"`)
		require.NotContains(t, h.calls, "backup-before-wipe")

		// 本机已有备份被打上了待导入标记。
		meta, exists, err := setupbackup.ReadMetadata(h.backupPath())
		require.NoError(t, err)
		require.True(t, exists)
		require.True(t, meta.PendingImport)
		require.Equal(t, 4, meta.Secrets.Count)

		// 随后全新安装会自动导入这份已有备份并清标记。
		h.calls = nil
		out, err = h.run("install", "--space-id", "crypto", "--yes")
		require.NoError(t, err)
		require.Contains(t, out, `"secrets_created":4`)
		require.Contains(t, out, `"channels_created":1`)
		meta, exists, err = setupbackup.ReadMetadata(h.backupPath())
		require.NoError(t, err)
		require.True(t, exists)
		require.False(t, meta.PendingImport)
	})
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
	require.NotContains(t, h.calls, "restore-backup")
	require.NotContains(t, h.calls, "init")
}

func TestSetupWipePreviewsBackupAndClearsEveryHostAndOperatorIdentity(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
	h.report("factor").ArchiveKB = 2048
	operatorFiles := []string{identitybundle.OperatorKeyFile, identitybundle.OperatorPinFile, identitybundle.OperatorCAFile}
	for _, name := range append(operatorFiles, "unrelated.txt") {
		require.NoError(t, os.WriteFile(filepath.Join(h.operator, name), []byte("keep\n"), 0o600))
	}

	out, err := h.run("wipe")
	require.NoError(t, err)
	preview := parsePreview(t, out)
	plan := preview["plan"].(map[string]any)
	require.Equal(t, deployscan.Notice, plan["notice"])
	backupPreview := preview["backup"].(map[string]any)
	require.Equal(t, "will_backup", backupPreview["status"])
	require.EqualValues(t, 3, backupPreview["secret_count"])
	require.EqualValues(t, 2, backupPreview["channel_count"])
	hosts := plan["hosts"].([]any)
	require.Len(t, hosts, 4)
	at := slices.IndexFunc(hosts, func(host any) bool { return host.(map[string]any)["host"] == "factor" })
	require.GreaterOrEqual(t, at, 0)
	factor := hosts[at].(map[string]any)
	require.EqualValues(t, 2048, factor["archive_kb"], "预览里单独列出归档目录的大小")
	require.Empty(t, h.calls, "预览不清空")
	require.Contains(t, preview["note"], "主机 overseas 上有 Trade：清空会删除 Trade 库")
	require.Contains(t, preview["left_behind"], "云上的采集计划函数（moox-fetch-plan- 开头；要删加 --clean-functions）", "预览列出卸载不会动的东西")
	require.Contains(t, preview["left_behind"], "COS 桶里的云函数代码包（带 --clean-functions 也不删）")
	require.FileExists(t, filepath.Join(h.operator, identitybundle.OperatorKeyFile))

	_, err = h.run("wipe", "--yes")
	require.ErrorContains(t, err, "--expect")
	_, err = h.run("wipe", "--yes", "--expect", "sha256:other")
	require.ErrorContains(t, err, "wipe_scope_changed")
	require.Empty(t, h.calls)

	// 备份失败时停下不清任何主机。
	h.backupBeforeWipeErr = errors.New("gateway refused")
	_, err = h.run("wipe", "--yes", "--expect", plan["digest"].(string))
	require.ErrorContains(t, err, "backup_failed")
	require.Equal(t, []string{"backup-before-wipe"}, h.calls)
	h.backupBeforeWipeErr, h.calls = nil, nil

	out, err = h.run("wipe", "--yes", "--expect", plan["digest"].(string))
	require.NoError(t, err)
	require.Contains(t, out, `"status":"wiped"`)
	require.Contains(t, out, `"status":"backed_up"`)
	require.Equal(t, "backup-before-wipe", h.calls[0], "清空前必须先备份")
	require.ElementsMatch(t, []string{"backup-before-wipe", "wipe:control", "wipe:storage", "wipe:overseas", "wipe:factor"}, h.calls)
	for _, name := range operatorFiles {
		require.NoFileExists(t, filepath.Join(h.operator, name))
	}
	require.FileExists(t, filepath.Join(h.operator, "unrelated.txt"), "只删操作员身份")
	meta, exists, err := setupbackup.ReadMetadata(h.backupPath())
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, meta.PendingImport, "卸载写入的备份应带待导入标记")
}

// 一台清空失败时报错并保留操作员身份，重跑接着清且不覆盖已写好的待导入备份。
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
	meta, exists, err := setupbackup.ReadMetadata(h.backupPath())
	require.NoError(t, err)
	require.True(t, exists && meta.PendingImport)
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
	require.NotNil(t, got.SyncTopology)
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
		out, err := h.run(command, "--host", "factor", "--component", "archive-worker")
		require.NoError(t, err)
		require.Equal(t, "moox-archive-worker.service", parsePreview(t, out)["unit"])
		require.Len(t, operations, before, "没有 --yes 只预览")
		_, err = h.run(command, "--host", "factor", "--component", "archive-worker", "--yes")
		require.NoError(t, err)
	}
	require.Equal(t, []string{"factor/archive-worker/restart", "factor/archive-worker/stop", "factor/archive-worker/start"}, operations)
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

// 统一等就绪按 moox.toml 的声明排除组件：[deployments_disabled] 里的不等，停用的主机整台不等；其余的（含崩溃后正在自动重启的）都要等。
func TestSetupInstallWaitsForEveryComponentThatIsNotDisabled(t *testing.T) {
	h := newInstallHarness(t)
	h.snapshot.Manifest.DeploymentsDisabled = map[string][]string{"overseas": {"collector-stream"}}
	factor := h.snapshot.Manifest.HostCatalog["factor"]
	disabled := false
	factor.Enabled = &disabled
	h.snapshot.Manifest.HostCatalog["factor"] = factor
	waited := map[string][]string{}
	h.deps.waitReady = func(_ context.Context, host topology.Host, components []string, _ time.Duration) (map[string]string, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		waited[host.Name] = components
		return nil, nil
	}
	require.NoError(t, waitSetupAllReady(context.Background(), h.deps, h.snapshot))
	require.Contains(t, waited["overseas"], "trade")
	require.Contains(t, waited["overseas"], "egress-proxy")
	require.NotContains(t, waited["overseas"], "collector-stream", "声明停用的组件不等")
	require.NotContains(t, waited, "factor", "停用的主机整台不等")
}

// 停用的主机上仍留着 host-gateway 等部署：清空照样清它；保留更新的预检与执行跳过它（即使停用主机连不上）。
func TestSetupWipeIncludesDisabledHostButKeepUpdateSkipsIt(t *testing.T) {
	h := newInstallHarness(t)
	h.markInstalled()
	disabled := false
	factor := h.snapshot.Manifest.HostCatalog["factor"]
	factor.Enabled = &disabled
	h.snapshot.Manifest.HostCatalog["factor"] = factor
	require.False(t, h.snapshot.Manifest.HostByID("factor").Enabled)

	// 停用主机连不上时：setup install 预览仍成功，keep 可用、wipe 标为不可用；--keep --yes 照常执行。
	origScan := h.deps.scanDeployment
	h.deps.scanDeployment = func(ctx context.Context, snapshot *setupSnapshot, options deployscan.Options) ([]deployscan.HostReport, error) {
		if options.Wipe {
			return nil, errors.New("ssh unreachable factor")
		}
		return origScan(ctx, snapshot, options)
	}
	out, err := h.run("install")
	require.NoError(t, err)
	opts := parsePreview(t, out)["options"].(map[string]any)
	keepOpt := opts["keep"].(map[string]any)
	wipeOpt := opts["wipe"].(map[string]any)
	require.Equal(t, true, keepOpt["available"])
	require.Equal(t, false, wipeOpt["available"])
	for _, host := range keepOpt["hosts"].([]any) {
		require.NotEqual(t, "factor", host.(map[string]any)["host"], "保留更新的预检不检测停用的主机")
	}
	out, err = h.run("install", "--keep", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"updated"`)

	// 停用主机恢复可达时，setup wipe 照常包含并清空它。
	h.deps.scanDeployment = origScan
	h.calls = nil
	out, err = h.run("wipe")
	require.NoError(t, err)
	plan := parsePreview(t, out)["plan"].(map[string]any)
	require.True(t, slices.ContainsFunc(plan["hosts"].([]any), func(host any) bool { return host.(map[string]any)["host"] == "factor" }), "清空范围包含停用的主机")
	_, err = h.run("wipe", "--yes", "--expect", plan["digest"].(string))
	require.NoError(t, err)
	require.Contains(t, h.calls, "wipe:factor")
}
