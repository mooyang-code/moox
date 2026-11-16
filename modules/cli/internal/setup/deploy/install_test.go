package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// localClient 在本机执行“远端”命令：用来在 Linux 上（CI 的容器里）直接跑内嵌的安装脚本。
// HOME 指向临时目录，PATH 最前面是假的 systemctl，它把调用记到文件里，并按状态文件回答 is-active、is-enabled。
type localClient struct {
	t    *testing.T
	home string
	path string
}

func (c *localClient) Check(context.Context) error { return nil }
func (c *localClient) Close() error                { return nil }
func (c *localClient) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("unsupported")
}
func (c *localClient) Upload(_ context.Context, src io.Reader, _ int64, dst string, mode fs.FileMode) error {
	raw, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, raw, mode)
}
func (c *localClient) Download(_ context.Context, src string, dst io.Writer) (int64, error) {
	raw, err := os.ReadFile(src)
	if err != nil {
		return 0, err
	}
	n, err := dst.Write(raw)
	return int64(n), err
}
func (c *localClient) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = []string{"HOME=" + c.home, "PATH=" + c.path}
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	result := setupssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
	}
	return result, err
}

// fakeSystemctl 记下每次调用；is-active 看 <state>/active/<单元> 在不在，is-enabled 看 <state>/disabled/<单元> 在不在；
// restart、enable 之后单元算在运行。
const fakeSystemctl = `#!/bin/sh
state="%s"
echo "$@" >> "$state/calls.log"
shift
case "$1" in
  is-active) shift; [ "$1" = --quiet ] && shift; if [ -e "$state/active/$1" ]; then echo active; exit 0; else echo inactive; exit 3; fi ;;
  is-enabled) if [ -e "$state/disabled/$2" ]; then echo disabled; exit 1; else echo enabled; exit 0; fi ;;
  restart) touch "$state/active/$2" ;;
esac
exit 0
`

func newInstallEnv(t *testing.T) (*localClient, Layout, string) {
	t.Helper()
	for _, tool := range []string{"bash", "flock", "tar", "install"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("本机没有 %s：安装脚本的测试在 Linux 上跑", tool)
		}
	}
	base := t.TempDir()
	state, bin, home := filepath.Join(base, "state"), filepath.Join(base, "fakebin"), filepath.Join(base, "home")
	for _, dir := range []string{filepath.Join(state, "active"), filepath.Join(state, "disabled"), bin, home} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	script := strings.Replace(fakeSystemctl, "%s", state, 1)
	require.NoError(t, os.WriteFile(filepath.Join(bin, "systemctl"), []byte(script), 0o755))
	client := &localClient{t: t, home: home, path: bin + ":" + os.Getenv("PATH")}
	return client, Layout{Root: filepath.Join(base, "moox")}, state
}

func applyEntries(list string, keep ...string) map[string]File {
	entries := map[string]File{
		"components.list":                 {Data: []byte(list), Mode: 0o600},
		"version.json":                    {Data: []byte(`{"version":"dev"}` + "\n"), Mode: 0o600},
		"config/host-gateway/app.yaml":    {Data: []byte("host:\n  id: control\n"), Mode: 0o600},
		"config/host-agent/app.yaml":      {Data: []byte("version: 1\n"), Mode: 0o600},
		"secrets/host.key":                {Data: []byte("secret\n"), Mode: 0o600},
		"secrets/env/host-agent.env":      {Data: []byte("MOOX_NODE_ID=\"control\"\n"), Mode: 0o600},
		"secrets/eventbus/ca.pem":         {Data: []byte("ca\n"), Mode: 0o600},
		"units/moox-host-gateway.service": {Data: []byte("[Unit]\nDescription=MooX host-gateway\n"), Mode: 0o644},
		"units/moox-host-agent.service":   {Data: []byte("[Unit]\nDescription=MooX host-agent\n"), Mode: 0o644},
	}
	if len(keep) != 0 {
		entries["units.keep"] = File{Data: []byte(strings.Join(keep, "\n") + "\n"), Mode: 0o600}
	}
	return entries
}

// 在本机（Linux）用假的 systemctl 跑通“装一个 host-gateway 加 host-agent”：目录布局、权限、version.json、启动顺序、重跑、停用与清理多余单元。
func TestInstallScriptInstallsHostGatewayAndHostAgent(t *testing.T) {
	client, layout, state := newInstallEnv(t)
	ctx := context.Background()
	components := []string{"host-gateway", "host-agent"}

	output, err := client.Run(ctx, append([]string{"bash", "-s", "--", "prepare", layout.Root}, components...), strings.NewReader(installScript))
	require.NoError(t, err, output.Stderr)
	for _, dir := range []string{"bin", "config", "secrets/env", "data/host-gateway", "data/host-agent", "logs/host-agent"} {
		require.DirExists(t, filepath.Join(layout.Root, dir))
	}
	info, err := os.Stat(layout.Secrets())
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())

	// 二进制先放到位。
	local := filepath.Join(t.TempDir(), "moox-host-gateway")
	require.NoError(t, os.WriteFile(local, []byte("\x7fELF fake"), 0o600))
	output, err = runInstall(ctx, client, layout, "binaries", nil, map[string]string{"bin/moox-host-gateway": local})
	require.NoError(t, err, output.Stderr)
	info, err = os.Stat(layout.Bin("moox-host-gateway"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	// 目标机自己生成的秘密、上一次留下的配置文件、不再属于这台主机的单元。
	require.NoError(t, os.WriteFile(layout.Secret("admin-master.key"), []byte("master\n"), 0o600))
	require.NoError(t, os.MkdirAll(layout.Config("host-agent"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(layout.Config("host-agent"), "stale.yaml"), []byte("old\n"), 0o600))
	units := filepath.Join(client.home, ".config", "systemd", "user")
	require.NoError(t, os.WriteFile(filepath.Join(units, "moox-old.service"), []byte("[Unit]\n"), 0o644))

	output, err = runInstall(ctx, client, layout, "apply", applyEntries("host-gateway 1 0\nhost-agent 1 0\n", "moox-host-gateway.service", "moox-host-agent.service"), nil)
	require.NoError(t, err, output.Stderr)
	require.Equal(t, "removed moox-old\ncomponent host-gateway started\ncomponent host-agent started\n", output.Stdout)
	require.NoFileExists(t, filepath.Join(units, "moox-old.service"))
	require.FileExists(t, filepath.Join(units, "moox-host-agent.service"))
	require.NoFileExists(t, filepath.Join(layout.Config("host-agent"), "stale.yaml"), "配置按组件整目录替换")
	require.FileExists(t, filepath.Join(layout.Config("host-agent"), "app.yaml"))
	require.FileExists(t, layout.Secret("admin-master.key"), "目标机自己生成的秘密不碰")
	for _, name := range []string{layout.Secret("host.key"), layout.Env("host-agent"), layout.Version()} {
		info, err := os.Stat(name)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), name)
	}
	calls, err := os.ReadFile(filepath.Join(state, "calls.log"))
	require.NoError(t, err)
	log := string(calls)
	require.Contains(t, log, "--user daemon-reload")
	require.Less(t, strings.Index(log, "restart moox-host-gateway.service"), strings.Index(log, "restart moox-host-agent.service"), "按启动顺序重启")
	leftovers, err := filepath.Glob(filepath.Join(layout.Root, ".deploy-*"))
	require.NoError(t, err)
	require.Empty(t, leftovers, "上传的包和解包目录用完就删")

	// 重跑：没有变化且在运行的不重启；上次装好之后被停用的保持停用。
	require.NoError(t, os.WriteFile(filepath.Join(state, "calls.log"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(state, "disabled", "moox-host-agent.service"), nil, 0o600))
	output, err = runInstall(ctx, client, layout, "apply", applyEntries("host-gateway 0 1\nhost-agent 1 1\n"), nil)
	require.NoError(t, err, output.Stderr)
	require.Equal(t, "component host-gateway unchanged\ncomponent host-agent paused\n", output.Stdout)
	calls, err = os.ReadFile(filepath.Join(state, "calls.log"))
	require.NoError(t, err)
	require.NotContains(t, string(calls), "restart")

	// 上次没装完（部署记录里没有它）时，即使单元是 disabled 也照常启用：重跑能接着装完。
	output, err = runInstall(ctx, client, layout, "apply", applyEntries("host-gateway 0 1\nhost-agent 1 0\n"), nil)
	require.NoError(t, err, output.Stderr)
	require.Contains(t, output.Stdout, "component host-agent restarted")

	// 没有运行的组件即使没变化也会被拉起。
	require.NoError(t, os.Remove(filepath.Join(state, "active", "moox-host-gateway.service")))
	require.NoError(t, os.Remove(filepath.Join(state, "disabled", "moox-host-agent.service")))
	output, err = runInstall(ctx, client, layout, "apply", applyEntries("host-gateway 0 1\nhost-agent 0 1\n"), nil)
	require.NoError(t, err, output.Stderr)
	require.Equal(t, "component host-gateway started\ncomponent host-agent unchanged\n", output.Stdout)
}

func TestInstallScriptRejectsUnsafeRootAndUnknownStep(t *testing.T) {
	client, layout, _ := newInstallEnv(t)
	for _, argv := range [][]string{{"prepare", "/"}, {"prepare", "relative"}, {"prepare", layout.Root + " x"}, {"other", layout.Root, layout.Root + "/.deploy-x.tar.gz"}, {"apply", layout.Root, "/tmp/elsewhere.tar.gz"}} {
		_, err := client.Run(context.Background(), append([]string{"bash", "-s", "--"}, argv...), strings.NewReader(installScript))
		require.Error(t, err, argv)
	}
}
