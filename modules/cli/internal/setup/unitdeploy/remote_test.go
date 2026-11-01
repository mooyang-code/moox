package unitdeploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
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
	}, nil, "部署管理台", errors.New("exit 1"), setupssh.Result{ExitCode: 1})
	require.ErrorContains(t, err, "target_rejected")
	require.NotContains(t, err.Error(), "断开")
}

func TestHelperFailureWaitsForTargetAfterDisconnect(t *testing.T) {
	defer func(wait, poll time.Duration) { disconnectWait, disconnectPoll = wait, poll }(disconnectWait, disconnectPoll)
	disconnectWait, disconnectPoll = time.Minute, time.Millisecond
	replies := []string{"running", "running", "idle"}
	redial := func() (setupssh.Client, error) { return probeClient{replies: &replies}, nil }
	err := helperFailure(context.Background(), redial, nil, "部署管理台", errors.New("eof"), setupssh.Result{})
	require.ErrorContains(t, err, "ssh_disconnected")
	require.ErrorContains(t, err, "已结束")
	require.Empty(t, replies)
}

func TestHelperFailureGivesUpAfterDeadline(t *testing.T) {
	defer func(wait, poll time.Duration) { disconnectWait, disconnectPoll = wait, poll }(disconnectWait, disconnectPoll)
	disconnectWait, disconnectPoll = 0, time.Millisecond
	redial := func() (setupssh.Client, error) { return nil, errors.New("unreachable") }
	err := helperFailure(context.Background(), redial, nil, "部署管理台", errors.New("eof"), setupssh.Result{})
	require.ErrorContains(t, err, "ssh_disconnected")
	require.True(t, strings.Contains(err.Error(), "可能还在运行"))
}

func TestReadLocalStateSeesEveryCompletedStep(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(filepath.Join(state, "hosts/storage"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "units/storage/access"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "hosts/storage", unitReadyFile), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(state, "units/storage/access", unitReadyFile), []byte("{}"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "units/storage/storage"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "units/storage/storage/operation.json"), []byte("{}"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(state, "units/factor/factor-engine/releases/r1"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(state, "units/factor/factor-engine/releases/r1", unitReadyFile), []byte("{}"), 0o600))
	local := ReadLocalState(state, t.TempDir(), Binding{})
	require.True(t, local.Units["factor/factor-engine"], "带 --release 部署的也算完成")
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

// runLocalClient 把“目标机”命令放到本机运行，用真实的 python3 跑目标机脚本。
type runLocalClient struct{ setupssh.Client }

func (runLocalClient) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return setupssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

func TestUploadPackVerifiesAndWritesPrivateFiles(t *testing.T) {
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(directory, "moox")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bootstrap-input"), 0o700))
	files := []packFile{
		{filepath.Join(root, "bootstrap-input/host-deploy/e1/identity/key.pem"), []byte("key")},
		{filepath.Join(root, "bootstrap-input/host-deploy/e1/request.json"), []byte("{}")},
	}
	require.NoError(t, uploadPack(t.Context(), runLocalClient{}, root, files))
	raw, err := os.ReadFile(files[0].remote)
	require.NoError(t, err)
	require.Equal(t, "key", string(raw))
	info, err := os.Stat(files[0].remote)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	require.NoError(t, uploadPack(t.Context(), runLocalClient{}, root, files), "重试覆盖同样的内容")
	require.Error(t, uploadPack(t.Context(), runLocalClient{}, root, []packFile{{"/etc/passwd", []byte("x")}}), "只能写到 bootstrap-input")
}

func TestEnsureHelperExtractsFromHostPackage(t *testing.T) {
	if _, err := exec.LookPath("sha256sum"); err != nil {
		t.Skip("需要 sha256sum")
	}
	directory, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(directory, "moox")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bootstrap-input"), 0o700))
	source := filepath.Join(directory, "source/bin")
	require.NoError(t, os.MkdirAll(source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(source, "moox-runtime"), []byte("helper"), 0o700))
	archive := filepath.Join(directory, "host.tar.gz")
	require.NoError(t, exec.Command("tar", "-czf", archive, "-C", filepath.Dir(source), "bin/moox-runtime").Run())
	digest := sha([]byte("helper"))
	remote, err := ensureHelper(t.Context(), runLocalClient{}, root, "", archive, digest)
	require.NoError(t, err)
	info, err := os.Stat(remote)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	_, err = ensureHelper(t.Context(), runLocalClient{}, root, "", archive, sha([]byte("other")))
	require.Error(t, err, "摘要不对不能放到位")
}
