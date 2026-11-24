package deployscan

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/stretchr/testify/require"
)

// fakeSSH 只实现 Run：按主机返回预置的输出，并记下收到的命令行。
type fakeSSH struct {
	host   string
	stdout map[string]string
	err    error
	mu     *sync.Mutex
	argv   map[string][]string
}

func (f fakeSSH) Check(context.Context) error { return nil }
func (f fakeSSH) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("not used")
}
func (f fakeSSH) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error { return nil }
func (f fakeSSH) Download(context.Context, string, io.Writer) (int64, error)          { return 0, nil }
func (f fakeSSH) Close() error                                                        { return nil }
func (f fakeSSH) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	f.mu.Lock()
	f.argv[f.host] = argv
	f.mu.Unlock()
	if f.err != nil {
		return setupssh.Result{Stderr: "Traceback\nValueError: boom", ExitCode: 1}, f.err
	}
	return setupssh.Result{Stdout: f.stdout[f.host]}, nil
}

func testConfig(t *testing.T) *topology.Config {
	t.Helper()
	cfg, err := topology.Parse([]byte(`
[paths]
deploy_root = "/data/moox"

[hosts.control]
address = "203.0.113.10"
ssh = { username = "ubuntu" }

[hosts.storage]
address = "203.0.113.11"
ssh = { username = "ubuntu" }

[deployments]
control = ["admin", "monitor"]
storage = ["storage-primary", "storage-view", "eventbus"]
`))
	require.NoError(t, err)
	return &cfg
}

func fakeDial(stdout map[string]string, err error) (Dial, map[string][]string) {
	argv := map[string][]string{}
	mu := &sync.Mutex{}
	return func(_ context.Context, host topology.Host) (setupssh.Client, error) {
		return fakeSSH{host: host.Name, stdout: stdout, err: err, mu: mu, argv: argv}, nil
	}, argv
}

func TestScanSendsRootAsJSONAndKeepsHostOrderAndVersionBytes(t *testing.T) {
	version := `{"version":"v0.0.2",  "catalog_sha256":"sha256:abc"}`
	encode := func(installed bool, text string, units ...string) string {
		raw, _ := json.Marshal(map[string]any{
			"root": "/data/moox", "root_exists": true, "entries": []string{"bin", "version.json"}, "installed": installed,
			"version_text": text, "units": units, "active": units, "processes": 2, "archive_kb": -1, "seaweedfs_kb": -1,
			"disk_free_kb": 1024, "linger": true, "journald": false,
		})
		return string(raw)
	}
	dial, argv := fakeDial(map[string]string{
		"control": encode(true, version, "moox-admin.service"),
		"storage": encode(true, "{broken", "moox-eventbus.service"),
	}, nil)
	reports, err := Scan(context.Background(), testConfig(t), dial, Options{Sizes: true})
	require.NoError(t, err)
	require.Equal(t, []string{"control", "storage"}, []string{reports[0].Host, reports[1].Host})
	require.Equal(t, "203.0.113.10", reports[0].Address)
	require.Equal(t, version, string(reports[0].Version), "version.json 原文逐字节保留")
	require.Nil(t, reports[1].Version, "不是合法 JSON 的 version.json 不带回")
	require.True(t, reports[1].Installed)
	require.Equal(t, []string{"moox-admin.service"}, reports[0].Units)
	require.True(t, reports[0].Linger)
	require.Equal(t, RouteInstalled, Route(reports))

	call := argv["control"]
	require.Equal(t, []string{"python3", "-c"}, call[:2])
	require.Equal(t, inspectScript, call[2])
	var request map[string]any
	require.NoError(t, json.Unmarshal([]byte(call[3]), &request))
	require.Equal(t, map[string]any{"root": "/data/moox", "sizes": true}, request)

	// 只查一台。
	dial, argv = fakeDial(map[string]string{"storage": encode(false, "")}, nil)
	reports, err = Scan(context.Background(), testConfig(t), dial, Options{Host: "storage"})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Len(t, argv, 1)
	require.Equal(t, RouteFresh, Route(reports))
	_, err = Scan(context.Background(), testConfig(t), dial, Options{Host: "nobody"})
	require.Error(t, err)
}

func TestScanRejectsUnreadableOutput(t *testing.T) {
	dial, _ := fakeDial(map[string]string{"control": `{"root":"/data/moox","unknown":1}`, "storage": `{}`}, nil)
	_, err := Scan(context.Background(), testConfig(t), dial, Options{})
	require.ErrorContains(t, err, "无法解析")
	dial, _ = fakeDial(nil, errors.New("exit 1"))
	_, err = Scan(context.Background(), testConfig(t), dial, Options{})
	require.ErrorContains(t, err, "ValueError: boom")
}

func TestBuildPlanListsOnlyHostsWithContentAndHasStableDigest(t *testing.T) {
	reports := []HostReport{
		{Host: "control", Root: "/data/moox", Entries: []string{"bin", "data"}, Units: []string{"moox-admin.service"}, ArchiveKB: -1, SeaweedFSKB: -1},
		{Host: "storage", Root: "/data/moox", Entries: []string{}, Units: []string{}},
		{Host: "factor", Root: "/data/moox", Entries: []string{"archive"}, Processes: 1, ArchiveKB: 4096, SeaweedFSKB: 128},
	}
	plan := BuildPlan(reports)
	require.Equal(t, Notice, plan.Notice)
	require.Equal(t, "管理台里改过的密钥只在线上有，清空前先在管理台导出", plan.Notice)
	require.Len(t, plan.Hosts, 2)
	require.Equal(t, []string{"control", "factor"}, []string{plan.Hosts[0].Host, plan.Hosts[1].Host})
	require.Equal(t, int64(4096), plan.Hosts[1].ArchiveKB)
	require.True(t, strings.HasPrefix(plan.Digest, "sha256:"))
	require.Equal(t, plan.Digest, BuildPlan(reports).Digest)

	// 大小与磁盘空间变化不改摘要；条目、单元、进程变化改摘要。
	resized := slices.Clone(reports)
	resized[2].ArchiveKB, resized[2].DiskFreeKB = 8192, 1
	require.Equal(t, plan.Digest, BuildPlan(resized).Digest)
	grown := slices.Clone(reports)
	grown[1].Entries = []string{"bin"}
	require.NotEqual(t, plan.Digest, BuildPlan(grown).Digest)
	running := slices.Clone(reports)
	running[0].Processes = 3
	require.NotEqual(t, plan.Digest, BuildPlan(running).Digest)

	empty := BuildPlan([]HostReport{{Host: "control", Root: "/data/moox"}})
	require.Empty(t, empty.Hosts)
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"seaweedfs_kb":128`)
}

func TestWipeParsesResult(t *testing.T) {
	dial, argv := fakeDial(map[string]string{"control": `{"units":["moox-admin.service"],"killed":2,"removed":["bin","data"],"systemd_error":""}`}, nil)
	host := testConfig(t).HostByID("control")
	result, err := Wipe(context.Background(), dial, host, "/data/moox")
	require.NoError(t, err)
	require.Equal(t, HostResult{Host: "control", Units: []string{"moox-admin.service"}, Killed: 2, Removed: []string{"bin", "data"}}, result)
	require.Equal(t, wipeScript, argv["control"][2])
	require.JSONEq(t, `{"root":"/data/moox"}`, argv["control"][3])

	dial, _ = fakeDial(nil, errors.New("exit 2"))
	_, err = Wipe(context.Background(), dial, host, "/data/moox")
	require.ErrorContains(t, err, "清空失败")
}

// 在本机用 python3 跑一遍目标机脚本：临时部署根加假的 HOME；PATH 里只有假的 systemctl、loginctl（或者都没有）。
type scriptEnv struct {
	python, root, home, bin, log string
}

func newScriptEnv(t *testing.T, withSystemd bool) scriptEnv {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("本机没有 python3")
	}
	base := t.TempDir()
	env := scriptEnv{python: python, root: filepath.Join(base, "moox"), home: filepath.Join(base, "home"), bin: filepath.Join(base, "bin"), log: filepath.Join(base, "systemctl.log")}
	for _, dir := range []string{env.root, filepath.Join(env.home, ".config", "systemd", "user", "default.target.wants"), env.bin} {
		require.NoError(t, os.MkdirAll(dir, 0o700))
	}
	if withSystemd {
		write := func(name, body string) {
			require.NoError(t, os.WriteFile(filepath.Join(env.bin, name), []byte("#!/bin/sh\n"+body), 0o755))
		}
		write("systemctl", `echo "$@" >> "`+env.log+`"
if [ "$2" = is-active ]; then shift 2; for u in "$@"; do if [ "$u" = moox-admin.service ]; then echo active; else echo inactive; fi; done; fi
if [ "$2" = is-enabled ]; then shift 2; for u in "$@"; do if [ "$u" = moox-admin.service ]; then echo enabled; else echo disabled; fi; done; fi
exit 0
`)
		write("loginctl", "echo yes\n")
	}
	return env
}

func (e scriptEnv) run(t *testing.T, script string, request any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(request)
	require.NoError(t, err)
	command := exec.Command(e.python, "-I", "-c", script, string(raw))
	command.Env = []string{"HOME=" + e.home, "PATH=" + e.bin}
	command.Dir = e.home
	out, err := command.Output()
	return string(out), err
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var entries []string
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		entries = append(entries, path+" "+info.Mode().String())
		return nil
	}))
	return entries
}

func seedDeployment(t *testing.T, env scriptEnv) string {
	t.Helper()
	for _, dir := range []string{"bin", "config/admin", "data/seaweedfs/volume", "archive/2026", "secrets"} {
		require.NoError(t, os.MkdirAll(filepath.Join(env.root, dir), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(env.root, "archive/2026/part.csv"), make([]byte, 8192), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(env.root, "data/seaweedfs/volume/1.dat"), make([]byte, 4096), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(env.root, "version.json"), []byte(`{"version":"dev"}`), 0o600))
	units := filepath.Join(env.home, ".config", "systemd", "user")
	for _, unit := range []string{"moox-admin.service", "moox-monitor.service"} {
		require.NoError(t, os.WriteFile(filepath.Join(units, unit), []byte("[Unit]\n"), 0o644))
		require.NoError(t, os.Symlink(filepath.Join(units, unit), filepath.Join(units, "default.target.wants", unit)))
	}
	require.NoError(t, os.WriteFile(filepath.Join(units, "other.service"), []byte("[Unit]\n"), 0o644))
	// 部署根里指向外面的符号链接：清空只删链接，不跟随。
	outside := filepath.Join(filepath.Dir(env.root), "outside")
	require.NoError(t, os.MkdirAll(outside, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("keep"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(env.root, "import")))
	return outside
}

func TestInspectScriptIsReadOnlyAndReportsLayout(t *testing.T) {
	for _, withSystemd := range []bool{true, false} {
		env := newScriptEnv(t, withSystemd)
		seedDeployment(t, env)
		// 只看部署根和 HOME：假的 systemctl 自己会在旁边写一份调用记录。
		snapshot := func() []string { return append(snapshotTree(t, env.root), snapshotTree(t, env.home)...) }
		before := snapshot()
		out, err := env.run(t, inspectScript, map[string]any{"root": env.root, "sizes": true})
		require.NoError(t, err)
		require.Equal(t, before, snapshot(), "检测只读")
		var result inspectResult
		require.NoError(t, decode(out, &result))
		require.True(t, result.RootExists)
		require.Equal(t, []string{"archive", "bin", "config", "data", "import", "secrets", "version.json"}, result.Entries)
		require.True(t, result.Installed)
		require.Equal(t, `{"version":"dev"}`, result.VersionText)
		require.Equal(t, []string{"moox-admin.service", "moox-monitor.service"}, result.Units)
		require.GreaterOrEqual(t, result.ArchiveKB, int64(8))
		require.GreaterOrEqual(t, result.SeaweedFSKB, int64(4))
		require.Positive(t, result.DiskFreeKB)
		if withSystemd {
			require.Equal(t, []string{"moox-admin.service"}, result.Active)
			require.Equal(t, []string{"moox-monitor.service"}, result.Disabled)
			require.True(t, result.Linger)
		} else {
			require.Empty(t, result.Active)
			require.False(t, result.Linger)
		}
		// 不统计大小时是 -1；部署根不存在时没有条目、没装过，可用空间取上级目录。
		out, err = env.run(t, inspectScript, map[string]any{"root": env.root + "-missing"})
		require.NoError(t, err)
		require.NoError(t, decode(out, &result))
		require.False(t, result.RootExists)
		require.False(t, result.Installed)
		require.Equal(t, int64(-1), result.ArchiveKB)
		require.Positive(t, result.DiskFreeKB)
	}
}

func TestWipeScriptClearsRootKeepsItAndRefusesUnsafeRoots(t *testing.T) {
	for _, withSystemd := range []bool{true, false} {
		env := newScriptEnv(t, withSystemd)
		outside := seedDeployment(t, env)
		out, err := env.run(t, wipeScript, map[string]any{"root": env.root})
		require.NoError(t, err)
		var result HostResult
		require.NoError(t, decode(out, &result))
		require.Equal(t, []string{"moox-admin.service", "moox-monitor.service"}, result.Units)
		require.Equal(t, []string{"archive", "bin", "config", "data", "import", "secrets", "version.json"}, result.Removed)
		entries, err := os.ReadDir(env.root)
		require.NoError(t, err)
		require.Empty(t, entries, "部署根清空")
		info, err := os.Stat(env.root)
		require.NoError(t, err)
		require.True(t, info.IsDir(), "部署根本身保留")
		require.FileExists(t, filepath.Join(outside, "keep.txt"), "不跟随符号链接删到部署根外面")
		units := filepath.Join(env.home, ".config", "systemd", "user")
		left, err := os.ReadDir(units)
		require.NoError(t, err)
		names := []string{}
		for _, entry := range left {
			names = append(names, entry.Name())
		}
		require.Equal(t, []string{"default.target.wants", "other.service"}, names, "只删 moox-* 单元")
		wants, err := os.ReadDir(filepath.Join(units, "default.target.wants"))
		require.NoError(t, err)
		require.Empty(t, wants)
		if withSystemd {
			require.Empty(t, result.SystemdError)
			logged, err := os.ReadFile(env.log)
			require.NoError(t, err)
			require.Contains(t, string(logged), "--user disable --now moox-admin.service")
			require.Contains(t, string(logged), "--user daemon-reload")
		} else {
			require.NotEmpty(t, result.SystemdError, "没有 systemctl 时如实报告")
		}
	}
	env := newScriptEnv(t, false)
	for _, root := range []string{"/", "relative/moox", "/data/../moox", ""} {
		_, err := env.run(t, wipeScript, map[string]any{"root": root})
		require.Error(t, err, root)
		_, err = env.run(t, inspectScript, map[string]any{"root": root})
		require.Error(t, err, root)
	}
	link := filepath.Join(filepath.Dir(env.root), "link")
	require.NoError(t, os.Symlink(env.root, link))
	_, err := env.run(t, wipeScript, map[string]any{"root": link})
	require.Error(t, err, "部署根是符号链接时拒绝")
}

// moox.toml 里停用的主机只写登记、可能连不上：全量检测跳过它（连不上也不让预检失败），点名检测时照常检测。
func TestScanSkipsDisabledHostsUnlessNamed(t *testing.T) {
	cfg, err := topology.Parse([]byte(`
[paths]
deploy_root = "/data/moox"

[hosts.control]
address = "203.0.113.10"
ssh = { username = "ubuntu" }

[hosts.factor]
address = "203.0.113.12"
enabled = false
ssh = { username = "ubuntu" }

[deployments]
control = ["admin", "monitor", "storage-primary", "storage-view", "eventbus"]
factor = ["factor-engine"]
`))
	require.NoError(t, err)
	encoded, _ := json.Marshal(map[string]any{"root": "/data/moox", "installed": true, "archive_kb": -1, "seaweedfs_kb": -1, "disk_free_kb": -1})
	var dialed []string
	dial := func(_ context.Context, host topology.Host) (setupssh.Client, error) {
		dialed = append(dialed, host.Name)
		if host.Name == "factor" {
			return nil, errors.New("connection refused")
		}
		return fakeSSH{host: host.Name, stdout: map[string]string{host.Name: string(encoded)}, mu: &sync.Mutex{}, argv: map[string][]string{}}, nil
	}
	reports, err := Scan(context.Background(), &cfg, dial, Options{})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	require.Equal(t, "control", reports[0].Host)
	require.Equal(t, []string{"control"}, dialed)

	_, err = Scan(context.Background(), &cfg, dial, Options{Host: "factor"})
	require.ErrorContains(t, err, "connection refused", "点名的停用主机照常检测")
}

// 清空针对 moox.toml 所列的全部主机：停用的主机也检测，连不上照常报错并提示先让它可达或从 moox.toml 删掉。
func TestScanForWipeIncludesDisabledHosts(t *testing.T) {
	cfg, err := topology.Parse([]byte(`
[hosts.control]
address = "203.0.113.10"
ssh = { username = "ubuntu" }

[hosts.factor]
address = "203.0.113.12"
enabled = false
ssh = { username = "ubuntu" }

[deployments]
control = ["admin", "monitor", "storage-primary", "storage-view", "eventbus"]
factor = ["factor-engine"]
`))
	require.NoError(t, err)
	encoded, _ := json.Marshal(map[string]any{"root": cfg.Paths.DeployRoot, "installed": true, "archive_kb": -1, "seaweedfs_kb": -1, "disk_free_kb": -1})
	reachable := true
	dial := func(_ context.Context, host topology.Host) (setupssh.Client, error) {
		if host.Name == "factor" && !reachable {
			return nil, errors.New("connection refused")
		}
		return fakeSSH{host: host.Name, stdout: map[string]string{host.Name: string(encoded)}, mu: &sync.Mutex{}, argv: map[string][]string{}}, nil
	}
	reports, err := Scan(context.Background(), &cfg, dial, Options{Wipe: true})
	require.NoError(t, err)
	require.Equal(t, []string{"control", "factor"}, []string{reports[0].Host, reports[1].Host})

	reachable = false
	_, err = Scan(context.Background(), &cfg, dial, Options{Wipe: true})
	require.ErrorContains(t, err, "connection refused")
	require.ErrorContains(t, err, "从 moox.toml 删掉这台主机")
}
