package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
)

type wipeHarness struct {
	snapshot *setupconfig.Snapshot
	deps     setupDeps
	reports  []deployscan.HostReport
	wiped    []string
	wipeErr  error
	trialErr error
	trials   int
	operator string
	state    string
}

func newWipeHarness(t *testing.T) *wipeHarness {
	t.Helper()
	h := &wipeHarness{snapshot: setupSnapshot(t)}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	h.operator, h.state = filepath.Join(base, "operator"), filepath.Join(base, "state")
	for _, host := range h.snapshot.Manifest.Hosts() {
		h.reports = append(h.reports, deployscan.HostReport{Host: host.Name, Address: host.Address, Home: "/home/ubuntu", OwnerOK: true, ModeOK: true, ParentWritable: true, Entries: []string{"host"}, Markers: true, Units: map[string]string{"host": "r1"}, Trade: deployscan.TradeNone, DiskFreeKB: 100 << 20})
	}
	h.deps = setupDeps{
		load: func(string) (*setupconfig.Snapshot, error) { return h.snapshot, nil },
		validate: func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error) {
			return setupvalidate.Result{Checks: []setupvalidate.Check{{Name: "config", Status: "valid"}}}, nil
		},
		scanDeployment: func(context.Context, *setupconfig.Snapshot, deployscan.Options) ([]deployscan.HostReport, error) {
			return h.reports, nil
		},
		wipeHost: func(_ context.Context, host setupconfig.Host, _ string, _ []string) (deployscan.HostResult, error) {
			if h.wipeErr != nil {
				return deployscan.HostResult{}, h.wipeErr
			}
			h.wiped = append(h.wiped, host.Name)
			if len(h.wiped) == len(h.reports) {
				for i := range h.reports {
					h.reports[i].Entries, h.reports[i].Units = nil, nil
				}
			}
			return deployscan.HostResult{Host: host.Name}, nil
		},
		trialPackage: func(context.Context, unitdeploy.DeployOptions) error { h.trials++; return h.trialErr },
	}
	return h
}

func (h *wipeHarness) run(t *testing.T, make func(setupDeps) *cobra.Command, args ...string) (string, error) {
	t.Helper()
	cmd := make(h.deps)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetArgs(append([]string{"--state-dir", h.state, "--operator-dir", h.operator}, args...))
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), err
}

func (h *wipeHarness) digest(t *testing.T) string {
	t.Helper()
	out, err := h.run(t, newSetupWipeCommand)
	require.NoError(t, err)
	var preview struct {
		Preview bool            `json:"preview"`
		Plan    deployscan.Plan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &preview))
	require.True(t, preview.Preview)
	require.NotEmpty(t, preview.Plan.Digest)
	return preview.Plan.Digest
}

func TestSetupWipePreviewChangesNothing(t *testing.T) {
	h := newWipeHarness(t)
	h.digest(t)
	require.Empty(t, h.wiped)
	require.Zero(t, h.trials)
	require.Nil(t, deployscan.ReadPending(h.operator))
}

func TestSetupWipeRequiresMatchingDigest(t *testing.T) {
	h := newWipeHarness(t)
	_, err := h.run(t, newSetupWipeCommand, "--yes")
	require.ErrorContains(t, err, "--expect")
	_, err = h.run(t, newSetupWipeCommand, "--yes", "--expect", "stale")
	require.ErrorContains(t, err, "wipe_scope_changed")
	require.Empty(t, h.wiped)
}

func TestSetupWipeClearsAllHostsAndArchivesLocalState(t *testing.T) {
	h := newWipeHarness(t)
	require.NoError(t, os.MkdirAll(h.operator, 0o700))
	for _, name := range []string{"config.key", "known_hosts", "gateway-client.yaml"} {
		require.NoError(t, os.WriteFile(filepath.Join(h.operator, name), []byte("x"), 0o600))
	}
	require.NoError(t, os.MkdirAll(h.state, 0o700))
	out, err := h.run(t, newSetupWipeCommand, "--yes", "--expect", h.digest(t))
	require.NoError(t, err)
	require.Contains(t, out, `"status":"wiped"`)
	require.Len(t, h.wiped, len(h.reports))
	require.Equal(t, 1, h.trials)
	require.FileExists(t, filepath.Join(h.operator, "config.key"))
	require.FileExists(t, filepath.Join(h.operator, "known_hosts"))
	require.NoFileExists(t, filepath.Join(h.operator, "gateway-client.yaml"))
	require.NoDirExists(t, h.state)
	require.Nil(t, deployscan.ReadPending(h.operator))
}

func TestSetupWipeLeavesMarkerWhenInterrupted(t *testing.T) {
	h := newWipeHarness(t)
	digest := h.digest(t)
	h.wipeErr = errors.New("wipe_interrupted")
	_, err := h.run(t, newSetupWipeCommand, "--yes", "--expect", digest)
	require.ErrorContains(t, err, "wipe_interrupted")
	require.NotNil(t, deployscan.ReadPending(h.operator))
	local := unitdeploy.ReadLocalState(h.state, h.operator, unitdeploy.Binding{})
	require.Equal(t, deployscan.RouteExisting, deployscan.Decide(nil, "", local).Route, "主机都空了也要继续清")
}

func TestSetupWipeStopsOnTradeRisk(t *testing.T) {
	h := newWipeHarness(t)
	h.reports[0].Trade = deployscan.TradeLive
	digest := h.digest(t)
	_, err := h.run(t, newSetupWipeCommand, "--yes", "--expect", digest)
	require.ErrorContains(t, err, "wipe_trade_risk")
	require.Empty(t, h.wiped)
	_, err = h.run(t, newSetupWipeCommand, "--yes", "--expect", digest, "--accept-trade-risk")
	require.NoError(t, err)
	require.NotEmpty(t, h.wiped)
}

func TestSetupWipeTouchesNothingWhenBundleBroken(t *testing.T) {
	h := newWipeHarness(t)
	digest := h.digest(t)
	h.trialErr = errors.New("bundle_incomplete")
	_, err := h.run(t, newSetupWipeCommand, "--yes", "--expect", digest)
	require.ErrorContains(t, err, "bundle_incomplete")
	require.Empty(t, h.wiped)
	require.Nil(t, deployscan.ReadPending(h.operator))
}

func TestSetupWipeRefusesBlockedHosts(t *testing.T) {
	h := newWipeHarness(t)
	h.reports[0].OwnerOK = false
	out, err := h.run(t, newSetupWipeCommand)
	require.ErrorIs(t, err, errDeploymentBlocked)
	require.Contains(t, out, `"route":"blocked"`)
}

func TestSetupValidateReportsRoute(t *testing.T) {
	h := newWipeHarness(t)
	out, err := h.run(t, newSetupValidateCommand)
	require.NoError(t, err)
	require.Contains(t, out, `"route":"existing"`)
	require.Contains(t, out, `"checks"`)
	h.reports[0].PortsOccupied = []int{9527}
	out, err = h.run(t, newSetupValidateCommand)
	require.ErrorIs(t, err, errDeploymentBlocked)
	require.Contains(t, out, `"route":"blocked"`)
}

func TestCheckAdminRoute(t *testing.T) {
	h := newWipeHarness(t)
	options := unitdeploy.DeployOptions{StateDirectory: h.state, OperatorDirectory: h.operator, Log: &bytes.Buffer{}}
	require.ErrorContains(t, checkAdminRoute(context.Background(), h.deps, h.snapshot, options), "deployment_not_fresh")
	for i := range h.reports {
		h.reports[i].Entries, h.reports[i].Units = nil, nil
	}
	require.NoError(t, os.MkdirAll(h.state, 0o700))
	require.NoError(t, os.MkdirAll(h.operator, 0o700))
	pin := filepath.Join(h.operator, "gateway-bootstrap.json")
	require.NoError(t, os.WriteFile(pin, []byte("{}"), 0o600))
	leftover := filepath.Join(h.state, "runtime-identity.json")
	require.NoError(t, os.WriteFile(leftover, []byte("{}"), 0o600))
	require.NoError(t, checkAdminRoute(context.Background(), h.deps, h.snapshot, options))
	require.NoFileExists(t, leftover, "空机时要归档本机遗留状态")
	require.NoFileExists(t, pin, "旧操作员身份会和新 CA 冲突，也要归档")
	binding := unitdeploy.ReadLocalState(h.state, h.operator, setupBinding(h.snapshot))
	require.False(t, binding.StaleBinding, "首次安装开始时要记下配置和版本")
	require.True(t, unitdeploy.ReadLocalState(h.state, h.operator, unitdeploy.Binding{ConfigSHA256: "other"}).StaleBinding)
	require.NoError(t, os.RemoveAll(h.state))
	h.deps.scanDeployment = nil
	require.NoError(t, checkAdminRoute(context.Background(), h.deps, h.snapshot, options))
}

func TestSetupValidateListsNextSteps(t *testing.T) {
	h := newWipeHarness(t)
	for i := range h.reports {
		h.reports[i].Entries, h.reports[i].Units, h.reports[i].Markers = nil, nil, false
		h.reports[i].DiskFreeKB = 1 << 20
	}
	out, err := h.run(t, newSetupValidateCommand)
	require.NoError(t, err)
	require.Contains(t, out, `"route":"fresh"`)
	require.Contains(t, out, "setup bootstrap --stage admin")
	require.Contains(t, out, "disk_low", "磁盘不够要提醒，但不阻塞：清除正好能腾空间")
}

func TestSetupLogsChecksHostAndComponent(t *testing.T) {
	h := newWipeHarness(t)
	h.deps.readLogs = func(_ context.Context, host setupconfig.Host, roots []string, component, unit string, lines int) ([]deployscan.LogSection, error) {
		return []deployscan.LogSection{{Path: roots[0] + "/run/logs/" + component + ".log", Text: host.Name + " tail"}}, nil
	}
	cmd := func(args ...string) (string, error) {
		c := newSetupLogsCommand(h.deps)
		var stdout bytes.Buffer
		c.SetOut(&stdout)
		c.SetErr(&bytes.Buffer{})
		c.SilenceUsage, c.SilenceErrors = true, true
		c.SetArgs(args)
		err := c.Execute()
		return stdout.String(), err
	}
	control := h.snapshot.Manifest.ControlHost().Name
	out, err := cmd("--host", control, "--component", "admin")
	require.NoError(t, err)
	require.Contains(t, out, "tail")
	_, err = cmd("--host", "nobody", "--component", "admin")
	require.ErrorContains(t, err, "没有主机")
	_, err = cmd("--host", control, "--component", "../../secret")
	require.ErrorContains(t, err, "没有组件")
	_, err = cmd("--host", control, "--component", "trade")
	require.ErrorContains(t, err, "没有部署在")
	_, err = cmd("--host", control, "--component", "admin", "--lines", "99999")
	require.ErrorContains(t, err, "--lines")
}
