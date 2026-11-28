package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"sync"
	"testing"

	setupdataformat "github.com/mooyang-code/moox/modules/cli/internal/setup/dataformat"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	pkgdataformat "github.com/mooyang-code/moox/packages/dataformat"
	"github.com/stretchr/testify/require"
)

type preflightSpySSH struct {
	inspectStdout string
	mu            *sync.Mutex
	calls         *int
}

func (s preflightSpySSH) Check(context.Context) error { return nil }
func (s preflightSpySSH) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("not used")
}
func (s preflightSpySSH) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error {
	return nil
}
func (s preflightSpySSH) Download(context.Context, string, io.Writer) (int64, error) { return 0, nil }
func (s preflightSpySSH) Close() error                                               { return nil }
func (s preflightSpySSH) Run(_ context.Context, _ []string, _ io.Reader) (setupssh.Result, error) {
	s.mu.Lock()
	*s.calls++
	call := *s.calls
	s.mu.Unlock()
	if call == 1 {
		return setupssh.Result{Stdout: s.inspectStdout}, nil
	}
	// 第二次调用说明已经通过预检进入目标机检查/准备阶段，用哨兵错误提前中止。
	return setupssh.Result{}, errReachedDeployPhase
}

var errReachedDeployPhase = errors.New("reached_deploy_phase")

func preflightTestTopology(t *testing.T) *topology.Config {
	t.Helper()
	cfg, err := topology.Parse([]byte(`
[paths]
deploy_root = "/data/moox"

[hosts.control]
address = "203.0.113.10"
ssh = { username = "ubuntu" }

[deployments]
control = ["admin", "monitor", "storage-primary", "storage-view", "eventbus"]
`))
	require.NoError(t, err)
	return &cfg
}

func encodeInspectWithFormats(t *testing.T, formats map[string]deployscan.ComponentDataFormat) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"root":         "/data/moox",
		"root_exists":  true,
		"entries":      []string{"data"},
		"installed":    false,
		"version_text": "",
		"units":        []string{},
		"active":       []string{},
		"disabled":     []string{},
		"processes":    0,
		"archive_kb":   -1,
		"disk_free_kb": 102400,
		"linger":       true,
		"journald":     true,
		"data_formats": formats,
	})
	require.NoError(t, err)
	return string(raw)
}

func TestDeployPreflightRejectsIncompatibleAndUnknownDataFormatBeforeTouchingTarget(t *testing.T) {
	cfg := preflightTestTopology(t)

	staleRec, err := pkgdataformat.Build("admin", map[string]string{pkgdataformat.PartSQLiteSchema: "sha256:old"})
	require.NoError(t, err)
	staleRaw, err := json.Marshal(staleRec)
	require.NoError(t, err)

	for name, formats := range map[string]map[string]deployscan.ComponentDataFormat{
		"changed": {
			"admin": {HasData: true, FormatText: string(staleRaw)},
		},
		"unknown_missing_file": {
			"monitor": {HasData: true, FormatText: ""},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var (
				mu        sync.Mutex
				dialCount int
				runCalls  int
			)
			stdout := encodeInspectWithFormats(t, formats)
			dial := func(_ context.Context, _ topology.Host) (setupssh.Client, error) {
				mu.Lock()
				dialCount++
				mu.Unlock()
				return preflightSpySSH{inspectStdout: stdout, mu: &mu, calls: &runCalls}, nil
			}
			_, deployErr := Deploy(context.Background(), Options{
				Config:         cfg,
				HostID:         "control",
				RepositoryRoot: t.TempDir(),
				Dial:           dial,
			})
			require.Error(t, deployErr)
			require.ErrorContains(t, deployErr, "data_format_incompatible")
			require.Equal(t, 1, dialCount, "预检失败后不应再连接目标机执行部署")
			require.Equal(t, 1, runCalls, "预检失败后不应执行任何写操作命令")
		})
	}
}

func TestDeployPreflightAllowsMatchingAndNoDataHosts(t *testing.T) {
	cfg := preflightTestTopology(t)
	expected, err := setupdataformat.Expected()
	require.NoError(t, err)
	adminRaw, err := json.Marshal(expected["admin"])
	require.NoError(t, err)

	var (
		mu       sync.Mutex
		runCalls int
	)
	stdout := encodeInspectWithFormats(t, map[string]deployscan.ComponentDataFormat{
		"admin":   {HasData: true, FormatText: string(adminRaw)},
		"monitor": {HasData: false},
	})
	dial := func(_ context.Context, _ topology.Host) (setupssh.Client, error) {
		return preflightSpySSH{inspectStdout: stdout, mu: &mu, calls: &runCalls}, nil
	}
	_, deployErr := Deploy(context.Background(), Options{
		Config:         cfg,
		HostID:         "control",
		RepositoryRoot: t.TempDir(),
		Dial:           dial,
	})
	// 应该通过数据格式预检，直到进入 checkPlatform 阶段触发哨兵错误（说明格式预检放行）。
	require.Error(t, deployErr)
	require.NotContains(t, deployErr.Error(), "data_format_incompatible")
	require.GreaterOrEqual(t, runCalls, 2)
}
