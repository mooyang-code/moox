package unitdeploy

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/stretchr/testify/require"
)

type probeClient struct {
	setupssh.Client
	replies *[]string
}

func (c probeClient) Run(context.Context, []string, io.Reader) (setupssh.Result, error) {
	reply := (*c.replies)[0]
	*c.replies = (*c.replies)[1:]
	return setupssh.Result{Stdout: reply + "\n"}, nil
}
func (probeClient) Close() error { return nil }

func TestHelperFailureExplicitFailureDoesNotWait(t *testing.T) {
	err := helperFailure(context.Background(), func() (setupssh.Client, error) {
		t.Fatal("目标机明确失败时不需要重新连接")
		return nil, nil
	}, "部署管理台", errors.New("exit 1"), setupssh.Result{ExitCode: 1})
	require.ErrorContains(t, err, "target_rejected")
	require.NotContains(t, err.Error(), "断开")
}

func TestHelperFailureWaitsForTargetAfterDisconnect(t *testing.T) {
	defer func(wait, poll time.Duration) { disconnectWait, disconnectPoll = wait, poll }(disconnectWait, disconnectPoll)
	disconnectWait, disconnectPoll = time.Minute, time.Millisecond
	replies := []string{"running", "running", "idle"}
	redial := func() (setupssh.Client, error) { return probeClient{replies: &replies}, nil }
	err := helperFailure(context.Background(), redial, "部署管理台", errors.New("eof"), setupssh.Result{})
	require.ErrorContains(t, err, "ssh_disconnected")
	require.ErrorContains(t, err, "已结束")
	require.Empty(t, replies)
}

func TestHelperFailureGivesUpAfterDeadline(t *testing.T) {
	defer func(wait, poll time.Duration) { disconnectWait, disconnectPoll = wait, poll }(disconnectWait, disconnectPoll)
	disconnectWait, disconnectPoll = 0, time.Millisecond
	redial := func() (setupssh.Client, error) { return nil, errors.New("unreachable") }
	err := helperFailure(context.Background(), redial, "部署管理台", errors.New("eof"), setupssh.Result{})
	require.ErrorContains(t, err, "ssh_disconnected")
	require.True(t, strings.Contains(err.Error(), "可能还在运行"))
}

func TestRemoteFailureReportsCancellation(t *testing.T) {
	err := remoteFailure("部署管理台", context.Canceled, setupssh.Result{})
	require.ErrorContains(t, err, "operation_cancelled")
	require.NotContains(t, err.Error(), "ssh_disconnected")
}

func TestReadLocalStateSeesEveryCompletedStep(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(filepath.Join(state, "hosts/storage"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "units/storage/access"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "hosts/storage", unitReadyFile), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(state, "units/storage/access", unitReadyFile), []byte("{}"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "units/storage/storage"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "units/storage/storage/operation.json"), []byte("{}"), 0o600))
	local := ReadLocalState(state, t.TempDir(), Binding{})
	require.True(t, local.Units["storage/host"])
	require.True(t, local.Units["storage/access"])
	require.False(t, local.Units["storage/storage"], "operation.json 在部署前就写了，不能算成功")
}

func TestControlDependenciesSkipEgressWithoutOverseasHost(t *testing.T) {
	snapshot := &setupconfig.Snapshot{Manifest: setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "198.51.100.10"}},
		Deployments: map[string][]string{"control": {"admin", "collector", "storage-primary"}},
	}}
	root, err := os.OpenRoot(t.TempDir())
	require.NoError(t, err)
	defer root.Close()
	err = checkControlDependencyRecords(snapshot, root, []string{"admin"})
	require.NoError(t, err, "只有 Collector 才需要出口代理")
	err = checkControlDependencyRecords(snapshot, root, []string{"collector"})
	require.NotErrorIs(t, err, nil)
	require.NotContains(t, err.Error(), "egress", "没有海外机时只查 Storage")
}
