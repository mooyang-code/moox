package command

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/bundle"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/stretchr/testify/require"
)

// installHarness 在 wipeHarness 上把部署步骤都换成记录调用。
type installHarness struct {
	*wipeHarness
	mu          sync.Mutex
	calls       []string
	fail        string
	unreachable bool
}

func newInstallHarness(t *testing.T) *installHarness {
	t.Helper()
	h := &installHarness{wipeHarness: newWipeHarness(t)}
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, bundle.ManifestName), []byte(`{"version":1,"release":"v0.0.0","commit":"0123456789abcdef0123456789abcdef01234567","units":{"host":{"file":"host.tar.gz"}},"files":["a"]}`), 0o644))
	t.Chdir(root)
	deployments := h.snapshot.Manifest.Deployments
	deployments["compute"] = append(slices.Clone(deployments["compute"]), "collector-stream", "egress-proxy")
	record := func(name string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls = append(h.calls, name)
		if name == h.fail {
			return errors.New("boom " + name)
		}
		return nil
	}
	h.deps.checkDownload = func(context.Context, setupconfig.Manifest, unitdeploy.DeployOptions, string) (bool, error) {
		return !h.unreachable, nil
	}
	h.deps.prefetch = func(context.Context, setupconfig.Manifest, unitdeploy.DeployOptions, string) error { return nil }
	h.deps.localReachable = func(context.Context, unitdeploy.DeployOptions) bool { return false }
	h.deps.ensureFirewall = func(context.Context, *setupconfig.Snapshot) (setupFirewallSummary, error) {
		return setupFirewallSummary{Status: "ready"}, record("firewall")
	}
	h.deps.ensureRelayFirewall = h.deps.ensureFirewall
	h.deps.bootstrapAdmin = func(context.Context, *setupconfig.Snapshot, unitdeploy.DeployOptions) (unitdeploy.BootstrapResult, error) {
		return unitdeploy.BootstrapResult{}, record("admin")
	}
	h.deps.bootstrapControl = func(context.Context, *setupconfig.Snapshot, unitdeploy.ControlOptions) (unitdeploy.BootstrapResult, error) {
		return unitdeploy.BootstrapResult{}, record("control")
	}
	h.deps.deployHost = func(_ context.Context, _ *setupconfig.Snapshot, options unitdeploy.HostOptions) (unitdeploy.HostResult, error) {
		return unitdeploy.HostResult{}, record("host:" + options.HostID)
	}
	h.deps.deployUnit = func(_ context.Context, _ *setupconfig.Snapshot, options unitdeploy.UnitOptions) (unitdeploy.UnitResult, error) {
		return unitdeploy.UnitResult{}, record("unit:" + options.Profile)
	}
	h.deps.installWatchdog = func(context.Context, *setupconfig.Snapshot, setupconfig.Host, bool) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	}
	h.deps.loadInitBundle = func(string) (setupInitBundle, error) { return setupInitBundle{}, nil }
	h.deps.initMetadata = func(context.Context, *setupconfig.Snapshot, string, setupInitBundle) (setupInitStorageSummary, error) {
		return setupInitStorageSummary{}, record("init-storage")
	}
	h.deps.initStorage = func(context.Context, *setupconfig.Snapshot, string, string, string, setupInitBundle) (setupInitSummary, error) {
		return setupInitSummary{}, record("init")
	}
	h.deps.backupConfig = func(context.Context, string) error { return record("backup") }
	h.deps.verifySpace = func(context.Context, string, *setupconfig.Snapshot, unitdeploy.DeployOptions, string) error {
		return record("verify")
	}
	h.deps.openStart = func(context.Context, string, *setupconfig.Snapshot) (setupStartClient, func(), error) {
		if err := record("start"); err != nil {
			return nil, nil, err
		}
		client := startClient()
		client.tasks = []adminclient.CollectionTaskSummary{{TaskID: "a"}}
		return client, func() {}, nil
	}
	return h
}

func (h *installHarness) position(t *testing.T, name string) int {
	t.Helper()
	for i, call := range h.calls {
		if call == name {
			return i
		}
	}
	t.Fatalf("没有执行 %s：%v", name, h.calls)
	return -1
}

func freshReports(h *installHarness) {
	for i := range h.reports {
		h.reports[i].Entries, h.reports[i].Units, h.reports[i].RuntimeSHA256 = nil, nil, ""
	}
}

func TestSetupInstallPreviewChangesNothing(t *testing.T) {
	h := newInstallHarness(t)
	freshReports(h)
	out, err := h.run(t, newSetupInstallCommand, "--space-id", "crypto")
	require.NoError(t, err)
	require.Contains(t, out, `"preview": true`)
	require.Empty(t, h.calls, "预检不改任何东西")
}

func TestSetupInstallFreshRunsStepsInDependencyOrder(t *testing.T) {
	h := newInstallHarness(t)
	freshReports(h)
	out, err := h.run(t, newSetupInstallCommand, "--space-id", "crypto", "--yes")
	require.NoError(t, err)
	require.Contains(t, out, `"status":"installed"`)
	require.Less(t, h.position(t, "firewall"), h.position(t, "admin"))
	require.Less(t, h.position(t, "admin"), h.position(t, "unit:storage"))
	require.Less(t, h.position(t, "init-storage"), h.position(t, "control"))
	require.Less(t, h.position(t, "control"), h.position(t, "init"))
	require.Less(t, h.position(t, "start"), h.position(t, "verify"), "启动之后才验收")
}

func TestSetupInstallExistingRequiresConfirmedWipe(t *testing.T) {
	h := newInstallHarness(t)
	_, err := h.run(t, newSetupInstallCommand, "--space-id", "crypto", "--yes", "--wipe", "wrong")
	require.ErrorContains(t, err, "wipe_scope_changed")
	require.Empty(t, h.wiped)
	require.Empty(t, h.calls)
	require.Nil(t, deployscan.ReadPending(h.operator))
}

func TestSetupInstallWipesThenDeploys(t *testing.T) {
	h := newInstallHarness(t)
	digest := h.digest(t)
	_, err := h.run(t, newSetupInstallCommand, "--space-id", "crypto", "--yes", "--wipe", digest)
	require.NoError(t, err)
	require.Len(t, h.wiped, len(h.reports))
	require.Nil(t, deployscan.ReadPending(h.operator), "清除确认后删掉标记")
	h.position(t, "admin")
}

func TestSetupInstallStopsOnFailedStepAndReportsRerun(t *testing.T) {
	h := newInstallHarness(t)
	freshReports(h)
	h.fail = "control"
	_, err := h.run(t, newSetupInstallCommand, "--space-id", "crypto", "--yes")
	require.ErrorContains(t, err, "重跑同一条命令")
	require.Equal(t, -1, func() int {
		for i, call := range h.calls {
			if call == "init" {
				return i
			}
		}
		return -1
	}(), "失败后不进入下一组")
}

func TestSetupInstallStopsBeforeWipeWhenNothingCanDownload(t *testing.T) {
	h := newInstallHarness(t)
	h.unreachable = true
	digest := h.digest(t)
	_, err := h.run(t, newSetupInstallCommand, "--space-id", "crypto", "--yes", "--wipe", digest)
	require.ErrorContains(t, err, "download_failed")
	require.Empty(t, h.wiped, "下载不了就不能动旧部署")
}

func TestSetupInstallRefusesSpaceWithoutPrerequisites(t *testing.T) {
	h := newInstallHarness(t)
	_, err := h.run(t, newSetupInstallCommand, "--space-id", "stockcn")
	require.ErrorContains(t, err, "start_prerequisite_missing")
}
