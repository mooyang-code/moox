package deployscan

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	_ "github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
)

// localClient 在本机执行命令代替 SSH，用临时的 HOME 和 PATH 隔离 crontab、systemctl。
type localClient struct{ env []string }

func (c localClient) Check(context.Context) error { return nil }
func (c localClient) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("unused")
}
func (c localClient) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return errors.New("unused")
}
func (c localClient) Download(context.Context, string, io.Writer) (int64, error) {
	return 0, errors.New("unused")
}
func (c localClient) Close() error { return nil }
func (c localClient) Run(ctx context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Env = c.env
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	return setupssh.Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

type sandbox struct {
	home, root string
	client     localClient
	dial       Dial
}

func script(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
}

func newSandbox(t *testing.T) *sandbox {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("需要 python3")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	home := filepath.Join(base, "home")
	bin := filepath.Join(base, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o755))
	require.NoError(t, os.MkdirAll(home, 0o755))
	cron := filepath.Join(base, "crontab.txt")
	script(t, filepath.Join(bin, "crontab"), `f='`+cron+`'
case "$1" in
-l) [ -f "$f" ] && cat "$f" || exit 1 ;;
-r) rm -f "$f" ;;
-) cat > "$f" ;;
esac`)
	script(t, filepath.Join(bin, "systemctl"), `echo "$@" >> '`+filepath.Join(base, "systemctl.log")+`'
[ "$2" = is-active ] && exit 3
exit 0`)
	python, err := exec.LookPath("python3")
	require.NoError(t, err)
	require.NoError(t, os.Symlink(python, filepath.Join(bin, "python3")))
	env := []string{"HOME=" + home, "PATH=" + bin + ":/usr/bin:/bin"}
	client := localClient{env: env}
	return &sandbox{home: home, root: filepath.Join(base, "moox"), client: client, dial: func(context.Context, setupconfig.Host) (setupssh.Client, error) { return client, nil }}
}

func (s *sandbox) cron(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(s.home), "crontab.txt"), []byte(content), 0o644))
}

func (s *sandbox) cronContent() string {
	raw, _ := os.ReadFile(filepath.Join(filepath.Dir(s.home), "crontab.txt"))
	return string(raw)
}

func (s *sandbox) inspect(t *testing.T, request map[string]any) HostReport {
	t.Helper()
	report, err := inspect(context.Background(), s.dial, setupconfig.Host{Name: "control", Address: "198.51.100.10"}, request)
	require.NoError(t, err)
	return report
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func deployment(t *testing.T, s *sandbox) {
	t.Helper()
	require.NoError(t, os.MkdirAll(s.root, 0o755))
	release := filepath.Join(s.root, "prod/releases/r1")
	write(t, filepath.Join(release, "stop.sh"), "#!/bin/sh\necho stopped >> '"+filepath.Join(filepath.Dir(s.home), "stop.log")+"'\n")
	require.NoError(t, os.Chmod(filepath.Join(release, "stop.sh"), 0o755))
	require.NoError(t, os.Symlink("releases/r1", filepath.Join(s.root, "prod/current")))
	write(t, filepath.Join(s.root, "identity/runtime.json"), `{"version":1,"host_id":"control","sha256":"sha256:abc"}`)
	write(t, filepath.Join(s.root, "import/history.bin"), "data")
	write(t, filepath.Join(s.root, "run/host.json"), "{}")
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "run/x"), 0o755))
	require.NoError(t, os.Chmod(release, 0o555))
	t.Cleanup(func() { _ = os.Chmod(release, 0o755) })
}

func snapshotTree(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	require.NoError(t, filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		names = append(names, name)
		return nil
	}))
	return names
}

func TestInspectEmptyMachine(t *testing.T) {
	s := newSandbox(t)
	report := s.inspect(t, map[string]any{"deploy_root": s.root, "archive_root": filepath.Join(s.root, "archive")})
	require.False(t, report.RootExists)
	require.True(t, report.Empty())
	require.True(t, report.ParentWritable)
	require.Equal(t, TradeNone, report.Trade)
	require.Nil(t, report.RegisteredHosts)
}

func TestInspectReadsDeploymentWithoutWriting(t *testing.T) {
	s := newSandbox(t)
	deployment(t, s)
	s.cron(t, "* * * * * run # moox-watchdog\n")
	external := filepath.Join(s.home, "moox/factor-engine")
	write(t, filepath.Join(external, "bin/moox-factor-engine"), "x")
	write(t, filepath.Join(s.home, ".config/systemd/user/moox-factor-engine.service"), "[Service]\nWorkingDirectory="+external+"\nExecStart="+external+"/bin/moox-factor-engine serve\n")
	archive := filepath.Join(filepath.Dir(s.home), "archive-data")
	write(t, filepath.Join(archive, ".state/staging.db"), "x")
	write(t, filepath.Join(external, "config/app.yaml"), "worker:\n  root_dir: "+archive+"\n")
	before := snapshotTree(t, filepath.Dir(s.home))
	report := s.inspect(t, map[string]any{"deploy_root": s.root, "archive_root": filepath.Join(s.root, "archive"), "sizes": true})
	require.Equal(t, before, snapshotTree(t, filepath.Dir(s.home)), "检测不能产生文件")
	require.False(t, report.Empty())
	require.True(t, report.Markers)
	require.True(t, report.Cron)
	require.Equal(t, "sha256:abc", report.RuntimeSHA256)
	require.Equal(t, map[string]string{"prod": "r1"}, report.Units)
	require.Equal(t, external, report.ExternalDirs[0].Path)
	require.True(t, report.ExternalDirs[0].Marked)
	require.Contains(t, report.ArchiveDirs, DirReport{Path: archive, Marked: true})
	require.Contains(t, report.SizesKB, s.root)
}

func tradeDB(t *testing.T, path, mode string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE t_trading_accounts (c_trading_account_id TEXT, c_execution_mode TEXT);
		CREATE TABLE t_trading_positions (c_trading_account_id TEXT, c_signed_quantity TEXT);
		CREATE TABLE t_trade_orders (c_trading_account_id TEXT, c_state TEXT);
		INSERT INTO t_trading_accounts VALUES ('a', '` + mode + `');
		INSERT INTO t_trading_positions VALUES ('a', '1.5');
		INSERT INTO t_trade_orders VALUES ('a', 'OPEN');`)
	require.NoError(t, err)
}

func tradeExec(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(statement)
	require.NoError(t, err)
}

func TestInspectTradeAndRegistry(t *testing.T) {
	s := newSandbox(t)
	deployment(t, s)
	request := map[string]any{"deploy_root": s.root, "archive_root": filepath.Join(s.root, "archive")}
	database := filepath.Join(s.root, "trade/current/trade/data/moox_trade.db")
	tradeDB(t, database, "PAPER")
	require.Equal(t, TradeClear, s.inspect(t, request).Trade, "纸面账户的持仓和订单不算")
	tradeExec(t, database, `INSERT INTO t_trading_positions VALUES ('gone', '2')`)
	require.Equal(t, TradeLive, s.inspect(t, request).Trade, "账户记录没了但还有持仓")
	tradeExec(t, database, `DELETE FROM t_trading_positions WHERE c_trading_account_id = 'gone'; INSERT INTO t_trade_orders VALUES ('gone', 'SUBMIT_UNKNOWN')`)
	require.Equal(t, TradeLive, s.inspect(t, request).Trade, "账户记录没了但还有未完成订单")
	tradeExec(t, database, `DELETE FROM t_trade_orders WHERE c_trading_account_id = 'gone'; INSERT INTO t_trade_orders VALUES ('gone', 'FILLED'); INSERT INTO t_trading_positions VALUES ('gone', '0')`)
	require.Equal(t, TradeClear, s.inspect(t, request).Trade, "已结束的订单和零持仓不算")
	require.NoError(t, os.Remove(database))
	tradeDB(t, database, "LIVE")
	require.Equal(t, TradeLive, s.inspect(t, request).Trade)
	require.NoError(t, os.WriteFile(database, []byte("broken"), 0o644))
	require.Equal(t, TradeUnreadable, s.inspect(t, request).Trade)

	admin := filepath.Join(s.root, "prod/admin.db")
	require.NoError(t, os.Chmod(filepath.Join(s.root, "prod"), 0o755))
	db, err := sql.Open("sqlite", admin)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE t_hosts (c_host_id TEXT, c_address TEXT); INSERT INTO t_hosts VALUES ('old', '203.0.113.9')`)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	request["admin_db"] = admin
	require.Equal(t, []RegisteredHost{{ID: "old", Address: "203.0.113.9"}}, s.inspect(t, request).RegisteredHosts)
}

func TestInspectFlagsUnsafeRootMode(t *testing.T) {
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(s.root, 0o755))
	require.NoError(t, os.Chmod(s.root, 0o777))
	report := s.inspect(t, map[string]any{"deploy_root": s.root})
	require.False(t, report.ModeOK)
	require.Equal(t, RouteBlocked, Decide([]HostReport{report}, "control", LocalState{}).Route)
}

func wipe(t *testing.T, s *sandbox, dirs ...string) {
	t.Helper()
	result, err := WipeHost(context.Background(), s.dial, setupconfig.Host{Name: "control"}, s.root, dirs)
	require.NoError(t, err)
	require.Empty(t, result.ProcessesOutside)
}

func TestWipeClearsEverythingAndIsRepeatable(t *testing.T) {
	s := newSandbox(t)
	deployment(t, s)
	s.cron(t, "1 2 3 4 5 mine\n* * * * * run # moox-watchdog\n")
	external := filepath.Join(s.home, "moox/factor-engine")
	write(t, filepath.Join(external, "bin/moox-factor-engine"), "x")
	service := filepath.Join(s.home, ".config/systemd/user/moox-factor-engine.service")
	write(t, service, "[Service]\nWorkingDirectory="+external+"\n")
	keep := filepath.Join(s.home, "moox-web/app.js")
	write(t, keep, "用户自己的文件")
	outsideTarget := filepath.Join(s.home, "linked.txt")
	write(t, outsideTarget, "链接目标")
	require.NoError(t, os.Symlink(outsideTarget, filepath.Join(s.root, "run/link")))
	require.NoError(t, os.Symlink(s.home, filepath.Join(s.root, "run/homelink")))

	wipe(t, s, external)

	entries, err := os.ReadDir(s.root)
	require.NoError(t, err)
	require.Empty(t, entries, "部署根要清空，包括 import")
	require.DirExists(t, s.root, "部署根本身要保留")
	require.Equal(t, "1 2 3 4 5 mine\n", s.cronContent(), "只删看护这一行")
	require.NoFileExists(t, service)
	require.FileExists(t, keep)
	require.FileExists(t, outsideTarget, "不能跟随符号链接删到目录外")
	remaining, err := os.ReadDir(external)
	require.NoError(t, err)
	require.Empty(t, remaining)
	stopped, err := os.ReadFile(filepath.Join(filepath.Dir(s.home), "stop.log"))
	require.NoError(t, err)
	require.Contains(t, string(stopped), "stopped")
	log, err := os.ReadFile(filepath.Join(filepath.Dir(s.home), "systemctl.log"))
	require.NoError(t, err)
	for _, want := range []string{"stop moox-factor-engine.service", "disable moox-factor-engine.service", "daemon-reload"} {
		require.Contains(t, string(log), want)
	}

	wipe(t, s, external) // 重跑：stop.sh、moox-runtime 都没了也不能卡住
	require.True(t, s.inspect(t, map[string]any{"deploy_root": s.root}).Empty())
}

func TestWipeRemovesLastCronLine(t *testing.T) {
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(s.root, 0o755))
	s.cron(t, "* * * * * run # moox-watchdog\n")
	wipe(t, s)
	require.NoFileExists(t, filepath.Join(filepath.Dir(s.home), "crontab.txt"))
}

func TestWipeSkipsSystemdWithoutServices(t *testing.T) {
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(s.root, 0o755))
	wipe(t, s)
	require.NoFileExists(t, filepath.Join(filepath.Dir(s.home), "systemctl.log"))
}

func TestWipeStopsWhenSystemdUnreachable(t *testing.T) {
	s := newSandbox(t)
	deployment(t, s)
	write(t, filepath.Join(s.home, ".config/systemd/user/moox-archive-worker.service"), "[Service]\n")
	bin := strings.TrimPrefix(strings.Split(s.client.env[1], ":")[0], "PATH=")
	script(t, filepath.Join(bin, "systemctl"), "exit 1")
	_, err := WipeHost(context.Background(), s.dial, setupconfig.Host{Name: "control"}, s.root, nil)
	require.ErrorContains(t, err, "systemd_unreachable")
	require.FileExists(t, filepath.Join(s.root, "import/history.bin"), "连不上 systemd 时不能往下删")
}

func TestWipeKillsProcessesInsideRoot(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("需要 /proc")
	}
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "run"), 0o755))
	sleep, err := exec.LookPath("sleep")
	require.NoError(t, err)
	raw, err := os.ReadFile(sleep)
	require.NoError(t, err)
	binary := filepath.Join(s.root, "moox-fake")
	require.NoError(t, os.WriteFile(binary, raw, 0o755))
	command := exec.Command(binary, "60")
	require.NoError(t, command.Start())
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	report := s.inspect(t, map[string]any{"deploy_root": s.root})
	require.Equal(t, 1, report.ProcessesInside)
	require.Empty(t, report.ProcessesOutside)
	wipe(t, s)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("部署根内的进程没有被终止")
	}
}

func TestDecideRoutes(t *testing.T) {
	clean := HostReport{Host: "control", OwnerOK: true, ModeOK: true, ParentWritable: true}
	installed := clean
	installed.Entries, installed.Units, installed.RuntimeSHA256 = []string{"prod"}, map[string]string{"prod": "r1"}, "sha256:a"
	uploaded := clean
	uploaded.Entries = []string{"bootstrap-input"}
	halfWiped := clean
	halfWiped.Entries = []string{"storage"}
	cronOnly := clean
	cronOnly.Cron = true
	archive := clean
	archive.ArchiveDirs = []DirReport{{Path: "/data/x", Marked: true}}
	running := clean
	running.ProcessesInside = 3
	userFiles := clean
	userFiles.ArchiveDirs = []DirReport{{Path: "/data/x"}}
	foreignArchive := clean
	foreignArchive.ArchiveDirs = []DirReport{{Path: "/data/x", Marked: true, Foreign: true}}
	foreign := installed
	foreign.OwnerOK = false
	outside := clean
	outside.ProcessesOutside = []string{"/opt/moox-admin"}
	ports := clean
	ports.PortsOccupied = []int{9527}
	matched := LocalState{RuntimeSHA256: "sha256:a"}
	full := LocalState{RuntimeSHA256: "sha256:a", CoreReady: true, ControlReady: true, OperatorPin: true, UnitRecords: true}
	noRecords := full
	noRecords.UnitRecords = false
	other := LocalState{RuntimeSHA256: "sha256:b", ControlReady: true}
	tests := []struct {
		name    string
		reports []HostReport
		local   LocalState
		route   string
	}{
		{"全空机", []HostReport{clean}, LocalState{}, RouteFresh},
		{"空机+本机遗留状态", []HostReport{clean}, full, RouteFresh},
		{"新电脑+已部署", []HostReport{installed}, LocalState{}, RouteExisting},
		{"部分已装", []HostReport{clean, {Host: "storage", OwnerOK: true, ModeOK: true, ParentWritable: true, Entries: []string{"storage"}}}, LocalState{}, RouteExisting},
		{"半清的机器不算空机", []HostReport{halfWiped}, LocalState{}, RouteExisting},
		{"只剩看护也不算空机", []HostReport{cronOnly}, LocalState{}, RouteExisting},
		{"归档有内容也不算空机", []HostReport{archive}, LocalState{}, RouteExisting},
		{"没有标记的归档目录是用户自己的", []HostReport{userFiles}, LocalState{}, RouteFresh},
		{"归档里有删不掉的文件", []HostReport{foreignArchive}, LocalState{}, RouteBlocked},
		{"还有进程在跑不算空机", []HostReport{running}, LocalState{}, RouteExisting},
		{"首装做到一半", []HostReport{installed}, matched, RouteResume},
		{"首装写绑定前失败", []HostReport{uploaded}, matched, RouteResume},
		{"写绑定前失败但别处已部署", []HostReport{uploaded, {Host: "x", OwnerOK: true, ModeOK: true, ParentWritable: true, Entries: []string{"host"}, Units: map[string]string{"host": "r"}}}, matched, RouteExisting},
		{"原电脑", []HostReport{installed}, full, RouteUpgrade},
		{"本机缺部署记录", []HostReport{installed}, noRecords, RouteExisting},
		{"哈希不一致", []HostReport{installed}, other, RouteExisting},
		{"清除没做完", []HostReport{clean}, LocalState{WipePending: true}, RouteExisting},
		{"属主不同", []HostReport{foreign}, LocalState{}, RouteBlocked},
		{"目录外有进程", []HostReport{outside}, LocalState{}, RouteBlocked},
		{"端口被占", []HostReport{ports}, LocalState{}, RouteBlocked},
		{"阻塞优先于未完成清除", []HostReport{ports}, LocalState{WipePending: true}, RouteBlocked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := Decide(test.reports, "control", test.local)
			require.Equal(t, test.route, decision.Route)
			if test.route == RouteBlocked {
				require.NotEmpty(t, decision.Reasons)
			}
		})
	}
}

func TestCheckDeleteTarget(t *testing.T) {
	for _, target := range []string{"/", "/data", "/home", "/root", "/etc", "/usr", "/var", "/opt", "/home/ubuntu", "/home/other", "/home/ubuntu/", "relative", "/data/../etc", "/usr/local", "/var/lib", "/etc/systemd", "/home/ubuntu/.ssh", "/home/ubuntu/.local/share"} {
		require.Error(t, CheckDeleteTarget(target, "/home/ubuntu"), target)
	}
	require.Error(t, CheckDeleteTarget("/srv/users", "/srv/users/ubuntu"), "家目录的上级")
	for _, target := range []string{"/data/moox", "/home/ubuntu/moox", "/mnt/disk/moox"} {
		require.NoError(t, CheckDeleteTarget(target, "/home/ubuntu"), target)
	}
}

func planManifest() setupconfig.Manifest {
	return setupconfig.Manifest{Paths: setupconfig.Paths{DeployRoot: "/data/moox"}}
}

func TestBuildPlan(t *testing.T) {
	report := HostReport{Host: "h", Address: "198.51.100.1", Home: "/home/ubuntu", Entries: []string{"host", "prod", "my-notes"}, Markers: true, Trade: TradeClear,
		ExternalDirs:    []DirReport{{Path: "/home/ubuntu/moox/factor-engine", Marked: true}, {Path: "/home/ubuntu/projects"}},
		ArchiveDirs:     []DirReport{{Path: "/mnt/archive", Marked: true}, {Path: "/mnt/empty", Marked: true, Empty: true}, {Path: "/mnt/photos"}},
		RegisteredHosts: []RegisteredHost{{ID: "h"}, {ID: "old", Address: "203.0.113.9"}}, SizesKB: map[string]int64{"/data/moox": 1}}
	manifest := planManifest()
	plan, err := BuildPlan(manifest, []HostReport{report}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"my-notes"}, plan.Hosts[0].Unknown)
	require.Equal(t, []string{"/home/ubuntu/moox/factor-engine", "/mnt/archive"}, plan.Hosts[0].Dirs, "空目录不列")
	require.Equal(t, []string{"/home/ubuntu/projects", "/mnt/photos"}, plan.Hosts[0].Skipped, "没有 MooX 标记的目录绝不清除")
	require.Equal(t, []RegisteredHost{{ID: "old", Address: "203.0.113.9"}}, plan.OtherHosts)
	require.False(t, plan.RegistryUnread)
	require.Equal(t, TradeClear, plan.Trade)
	require.NotEmpty(t, plan.Digest)

	report.SizesKB = map[string]int64{"/data/moox": 2}
	same, err := BuildPlan(manifest, []HostReport{report}, nil)
	require.NoError(t, err)
	require.Equal(t, plan.Digest, same.Digest, "目录占用变化不算范围变化")
	report.Entries = append(report.Entries, "new")
	changed, err := BuildPlan(manifest, []HostReport{report}, nil)
	require.NoError(t, err)
	require.NotEqual(t, plan.Digest, changed.Digest)
}

func TestBuildPlanRejectsUnsafeRoot(t *testing.T) {
	manifest := planManifest()
	manifest.Paths.DeployRoot = "/home/ubuntu"
	_, err := BuildPlan(manifest, []HostReport{{Host: "h", Home: "/home/ubuntu", Markers: true}}, nil)
	require.ErrorContains(t, err, "delete_target_rejected")
}

func TestBuildPlanRejectsDangerousDirs(t *testing.T) {
	base := HostReport{Host: "h", Home: "/home/ubuntu", Markers: true, Entries: []string{"host"}}
	for _, dir := range []DirReport{
		{Path: "/data", Marked: true},
		{Path: "/usr/local", Marked: true},
		{Path: "/home/ubuntu/.local", Marked: true},
		{Path: "/mnt/link", Marked: true, Symlink: true},
	} {
		report := base
		report.ExternalDirs = []DirReport{dir}
		_, err := BuildPlan(planManifest(), []HostReport{report}, nil)
		require.ErrorContains(t, err, "delete_target_rejected", dir.Path)
	}
	manifest := planManifest()
	manifest.Paths.DeployRoot = "/data/x/moox"
	report := base
	report.ExternalDirs = []DirReport{{Path: "/data/x", Marked: true}}
	_, err := BuildPlan(manifest, []HostReport{report}, nil)
	require.ErrorContains(t, err, "上级")
}

func TestBuildPlanRequiresMarker(t *testing.T) {
	report := HostReport{Host: "h", Address: "198.51.100.1", Home: "/home/ubuntu", Entries: []string{"storage"}}
	_, err := BuildPlan(planManifest(), []HostReport{report}, nil)
	require.ErrorContains(t, err, "MooX 标记")
	for name, pending := range map[string]*PendingRecord{
		"标记读不出":   {},
		"换了主机":    {Hosts: map[string]PendingHost{"h": {Address: "203.0.113.1", Root: "/data/moox"}}},
		"换了部署目录":  {Hosts: map[string]PendingHost{"h": {Address: "198.51.100.1", Root: "/data/other"}}},
		"别的主机的标记": {Hosts: map[string]PendingHost{"x": {Address: "198.51.100.1", Root: "/data/moox"}}},
	} {
		_, err := BuildPlan(planManifest(), []HostReport{report}, pending)
		require.ErrorContains(t, err, "MooX 标记", name)
	}
	plan, err := BuildPlan(planManifest(), []HostReport{report}, &PendingRecord{Hosts: map[string]PendingHost{"h": {Address: "198.51.100.1", Root: "/data/moox", Dirs: []string{"/home/ubuntu/moox/old"}}}})
	require.NoError(t, err)
	require.Equal(t, []string{"/home/ubuntu/moox/old"}, plan.Hosts[0].Dirs, "重跑要沿用上次记录的目录")
}

func TestInterruptedUploadStillHasMarker(t *testing.T) {
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(filepath.Join(s.root, "bootstrap-input"), 0o700))
	report := s.inspect(t, map[string]any{"deploy_root": s.root})
	report.Home = "/home/ubuntu"
	_, err := BuildPlan(planManifest(), []HostReport{report}, nil)
	require.NoError(t, err, "上传阶段中断的机器也要能清")
}

func TestWipeWithoutCrontabCommand(t *testing.T) {
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(s.root, 0o755))
	bin := strings.TrimPrefix(strings.Split(s.client.env[1], ":")[0], "PATH=")
	require.NoError(t, os.Remove(filepath.Join(bin, "crontab")))
	s.client.env[1] = "PATH=" + bin
	wipe(t, s)
}

func TestMountedRootIsNotForeign(t *testing.T) {
	s := newSandbox(t)
	lost := filepath.Join(s.root, "lost+found")
	require.NoError(t, os.MkdirAll(lost, 0o755))
	require.NoError(t, os.Chmod(lost, 0))
	t.Cleanup(func() { _ = os.Chmod(lost, 0o755) })
	mountinfo := filepath.Join(s.home, "mountinfo")
	write(t, mountinfo, "36 35 98:0 / "+s.root+" rw - ext4 /dev/vdb rw\n")
	report := s.inspect(t, map[string]any{"deploy_root": s.root, "mountinfo": mountinfo})
	require.False(t, report.ForeignFiles, "挂载点的 lost+found 不算别人的文件")
	require.True(t, report.Empty())
	require.Equal(t, RouteFresh, Decide([]HostReport{report}, "control", LocalState{}).Route)
}

func TestWipeKeepsNestedMountPoint(t *testing.T) {
	s := newSandbox(t)
	deployment(t, s)
	mountinfo := filepath.Join(s.home, "mountinfo")
	mounted := filepath.Join(s.root, "import")
	write(t, mountinfo, "36 35 98:0 / "+mounted+" rw - ext4 /dev/vdc rw\n")
	raw, err := json.Marshal(map[string]any{"deploy_root": s.root, "mountinfo": mountinfo})
	require.NoError(t, err)
	output, err := s.client.Run(context.Background(), []string{"python3", "-c", wipeScript, string(raw)}, nil)
	require.NoError(t, err)
	require.Contains(t, output.Stdout, `"done"`)
	require.DirExists(t, mounted, "挂载点不能删")
	require.NoFileExists(t, filepath.Join(mounted, "history.bin"))
	report := s.inspect(t, map[string]any{"deploy_root": s.root, "mountinfo": mountinfo})
	require.True(t, report.Empty(), "清空的挂载点不算残留")
}

func TestSymlinkArchiveIsReported(t *testing.T) {
	s := newSandbox(t)
	target := filepath.Join(s.home, "nas")
	write(t, filepath.Join(target, ".state/staging.db"), "x")
	link := filepath.Join(filepath.Dir(s.home), "archive-link")
	require.NoError(t, os.Symlink(target, link))
	report := s.inspect(t, map[string]any{"deploy_root": s.root, "archive_root": link})
	require.Equal(t, []DirReport{{Path: link, Marked: true, Symlink: true}}, report.ArchiveDirs)
	report.Home = "/home/ubuntu"
	_, err := BuildPlan(planManifest(), []HostReport{report}, nil)
	require.ErrorContains(t, err, "符号链接")
}

func TestServiceWithoutWorkingDirectoryAddsNoDir(t *testing.T) {
	s := newSandbox(t)
	write(t, filepath.Join(s.home, ".config/systemd/user/moox-factor-engine.service"), "[Service]\nExecStart=/usr/local/bin/moox-factor-engine\n")
	report := s.inspect(t, map[string]any{"deploy_root": s.root})
	require.Empty(t, report.ExternalDirs)
}

func TestUnmarkedExternalDirIsLeftAlone(t *testing.T) {
	s := newSandbox(t)
	projects := filepath.Join(s.home, "projects")
	write(t, filepath.Join(projects, "code.go"), "x")
	write(t, filepath.Join(s.home, ".config/systemd/user/moox-factor-engine.service"), "[Service]\nWorkingDirectory="+projects+"\n")
	report := s.inspect(t, map[string]any{"deploy_root": s.root})
	require.False(t, report.ExternalDirs[0].Marked)
	report.Home = "/home/ubuntu"
	plan, err := BuildPlan(planManifest(), []HostReport{report}, nil)
	require.NoError(t, err)
	require.Empty(t, plan.Hosts[0].Dirs)
	require.Equal(t, []string{projects}, plan.Hosts[0].Skipped)
}

func TestScriptsAreASCII(t *testing.T) {
	for _, source := range []string{inspectScript, wipeScript} {
		for _, r := range source {
			require.Less(t, r, rune(128), "python3 -c 在 C 语言环境下可能读不了非 ASCII 内容")
		}
	}
}

func TestWipeSurvivesNonUTF8Crontab(t *testing.T) {
	s := newSandbox(t)
	require.NoError(t, os.MkdirAll(s.root, 0o755))
	s.cron(t, "1 2 3 4 5 \xff\n* * * * * run # moox-watchdog\n")
	wipe(t, s)
	require.Equal(t, "1 2 3 4 5 \xff\n", s.cronContent())
}

func TestArchiveLocalKeepsKeyAndFingerprints(t *testing.T) {
	base := t.TempDir()
	operator, state := filepath.Join(base, "moox"), filepath.Join(base, "state", "abc")
	for _, name := range []string{"config.key", "known_hosts", "gateway-client.yaml", "gateway-bootstrap.json", "caller-moox-cli.key", "storage-internal-auth.env", "notes.txt"} {
		write(t, filepath.Join(operator, name), "x")
	}
	write(t, filepath.Join(state, "core-software.json"), "{}")
	now := time.Date(2026, 10, 31, 1, 2, 3, 0, time.UTC)
	archived, err := ArchiveLocal(operator, state, now)
	require.NoError(t, err)
	require.Len(t, archived, 2)
	entries, err := os.ReadDir(operator)
	require.NoError(t, err)
	names := []string{}
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	require.Equal(t, []string{".bak-20261031010203", "config.key", "known_hosts", "notes.txt"}, names)
	require.FileExists(t, filepath.Join(operator, ".bak-20261031010203/gateway-client.yaml"))
	require.NoDirExists(t, state)
	require.DirExists(t, state+".bak-20261031010203")
	_, err = ArchiveLocal(operator, state, now)
	require.NoError(t, err, "重跑不能失败")
}

func TestPendingRoundTrip(t *testing.T) {
	operator := filepath.Join(t.TempDir(), "moox")
	require.Nil(t, ReadPending(operator))
	require.NoError(t, MarkPending(operator, Plan{Hosts: []HostPlan{{Host: "h", Address: "a", DeployRoot: "/r", Dirs: []string{"/x/y"}}}}))
	host, ok := ReadPending(operator).host("h", "a", "/r")
	require.True(t, ok)
	require.Equal(t, []string{"/x/y"}, host.Dirs)
	require.NoError(t, ClearPending(operator))
	require.NoError(t, ClearPending(operator))
	require.Nil(t, ReadPending(operator))
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	var out HostReport
	raw, err := json.Marshal(map[string]any{"bogus": 1})
	require.NoError(t, err)
	require.Error(t, decode(string(raw), &out))
}

func TestOrderClearsStorageLast(t *testing.T) {
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{"control": {Address: "198.51.100.1"}, "storage": {Address: "198.51.100.2"}, "overseas": {Address: "198.51.100.3"}, "factor": {Address: "198.51.100.4"}},
		Deployments: map[string][]string{"control": {"admin"}, "storage": {"storage-primary"}, "overseas": {"trade"}, "factor": {"factor-engine"}},
	}
	names := []string{}
	for _, host := range Order(manifest) {
		names = append(names, host.Name)
	}
	require.Equal(t, []string{"factor", "overseas", "control", "storage"}, names)
}
