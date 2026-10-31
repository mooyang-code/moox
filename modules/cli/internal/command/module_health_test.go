package command

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// healthFilesSSH 按 cat 的路径返回预置的文件内容，其余路径视为不存在。
type healthFilesSSH struct {
	hostFakeSSH
	files map[string]string
	cats  []string
}

func (h *healthFilesSSH) Run(ctx context.Context, argv []string, stdin io.Reader) (setupssh.Result, error) {
	key := strings.Join(argv, " ")
	h.cats = append(h.cats, key)
	if content, ok := h.files[key]; ok {
		return setupssh.Result{Stdout: content}, nil
	}
	return setupssh.Result{ExitCode: 1}, errors.New("ssh_command_failed")
}

func healthTestRemote(t *testing.T, ssh setupssh.Client) *hostRemote {
	t.Helper()
	manifest := setupconfig.Manifest{
		HostCatalog: map[string]setupconfig.HostDefinition{
			"control": {Address: "198.51.100.10", SSH: setupconfig.SSHConfig{Port: 22, Username: "ubuntu"}},
			"factor":  {Address: "192.168.0.103", SSH: setupconfig.SSHConfig{Port: 22, Username: "builder"}},
		},
		Deployments: map[string][]string{
			"control": {"admin", "collector", "strategy", "monitor"},
			"factor":  {"factor-engine"},
		},
	}
	remote := newHostRemote(manifest, "", nil)
	remote.dial = func(context.Context, setupconfig.Host) (setupssh.Client, error) { return ssh, nil }
	return remote
}

func clearHealthEnv(t *testing.T) {
	t.Helper()
	for _, module := range healthModules {
		for _, suffix := range []string{"VERSION", "ACCESS_KEY", "SECRET_KEY"} {
			t.Setenv("MOOX_"+module.EnvPrefix+"_HEALTH_AUTH_"+suffix, "")
		}
		for _, suffix := range []string{"ACCESS_KEY", "SECRET_KEY"} {
			t.Setenv("MOOX_"+module.EnvPrefix+"_HEALTH_RESET_"+suffix, "")
		}
	}
	for _, suffix := range []string{"VERSION", "ACCESS_KEY", "SECRET_KEY"} {
		t.Setenv("MOOX_HEALTH_AUTH_"+suffix, "")
	}
}

func TestParseHealthSecretsJSONAndEnv(t *testing.T) {
	values, err := parseHealthSecrets(`{"MOOX_HEALTH_AUTH_ACCESS_KEY":"a","MOOX_HEALTH_AUTH_SECRET_KEY":"s"}`, "json")
	require.NoError(t, err)
	assert.Equal(t, "s", values["MOOX_HEALTH_AUTH_SECRET_KEY"])
	_, err = parseHealthSecrets("not json", "json")
	require.Error(t, err)

	values, err = parseHealthSecrets("# 注释\nexport MOOX_HEALTH_AUTH_ACCESS_KEY=\"a\"\nMOOX_HEALTH_AUTH_SECRET_KEY='s'\nMOOX_HEALTH_RESET_SECRET_KEY=r\n", "env")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"MOOX_HEALTH_AUTH_ACCESS_KEY": "a", "MOOX_HEALTH_AUTH_SECRET_KEY": "s", "MOOX_HEALTH_RESET_SECRET_KEY": "r"}, values)
}

func TestHealthSecretSourcesFollowDeploymentLayout(t *testing.T) {
	sources := healthSecretSources(setupconfig.Manifest{}, "trade")
	var labels []string
	for _, source := range sources {
		labels = append(labels, source.Label)
	}
	assert.Equal(t, []string{
		"/data/moox/prod/current/secrets/runtime-trade.json",
		"/data/moox/trade/current/secrets/runtime-trade.json",
		"/data/moox/trade/secrets/health-auth.env",
		"~/moox/trade/secrets/health-auth.env",
	}, labels)
}

func TestResolveModuleHealthReadsSecretsForwardsPortsAndSetsEnv(t *testing.T) {
	clearHealthEnv(t)
	ssh := &healthFilesSSH{files: map[string]string{
		"cat /data/moox/prod/current/secrets/runtime-collector.json": `{"MOOX_HEALTH_AUTH_VERSION":"moox-health-v1","MOOX_HEALTH_AUTH_ACCESS_KEY":"monitor-x","MOOX_HEALTH_AUTH_SECRET_KEY":"secret-1","MOOX_HEALTH_RESET_ACCESS_KEY":"reset-collector","MOOX_HEALTH_RESET_SECRET_KEY":"reset-secret"}`,
		"cat /data/moox/factor-engine/secrets/health-auth.env":       "MOOX_HEALTH_AUTH_ACCESS_KEY=monitor-x\nMOOX_HEALTH_AUTH_SECRET_KEY=secret-2\n",
	}}
	remote := healthTestRemote(t, ssh)
	health, err := resolveModuleHealthWith(context.Background(), remote, moduleHealthRequest{Modules: []string{"collector", "factor-engine", "trade"}, GenericFrom: "factor-engine"})
	require.NoError(t, err)
	defer health.Close()

	for _, name := range []string{"collector", "factor-engine"} {
		host, _, err := net.SplitHostPort(strings.TrimPrefix(health.Endpoint(name), "http://"))
		require.NoError(t, err, name)
		assert.Equal(t, "127.0.0.1", host)
	}
	assert.Equal(t, []string{"trade"}, health.NotDeployed, "没有部署的模块自动跳过")
	assert.Equal(t, "secret-1", getenv("MOOX_COLLECTOR_HEALTH_AUTH_SECRET_KEY"))
	assert.Equal(t, "reset-secret", getenv("MOOX_COLLECTOR_HEALTH_RESET_SECRET_KEY"))
	assert.Equal(t, "secret-2", getenv("MOOX_FACTOR_HEALTH_AUTH_SECRET_KEY"))
	assert.Equal(t, "secret-2", getenv("MOOX_HEALTH_AUTH_SECRET_KEY"), "GenericFrom 的模块同时设为通用变量")
	assert.Equal(t, "moox-health-v1", getenv("MOOX_FACTOR_HEALTH_AUTH_VERSION"), "文件里没有版本时取默认值")
	assert.Equal(t, []string{"127.0.0.1:11412", "127.0.0.1:11417"}, ssh.forwarded)
}

func getenv(name string) string { return os.Getenv(name) }

func TestResolveModuleHealthPrefersUserProvidedSecret(t *testing.T) {
	clearHealthEnv(t)
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "mine")
	ssh := &healthFilesSSH{}
	health, err := resolveModuleHealthWith(context.Background(), healthTestRemote(t, ssh), moduleHealthRequest{Modules: []string{"strategy"}})
	require.NoError(t, err)
	defer health.Close()
	assert.NotEmpty(t, health.Endpoint("strategy"))
	assert.Empty(t, ssh.cats, "用户已经给了密钥就不读主机上的文件")
	assert.Equal(t, "mine", getenv("MOOX_HEALTH_AUTH_SECRET_KEY"))
}

func TestResolveModuleHealthReportsTriedPathsWhenSecretMissing(t *testing.T) {
	clearHealthEnv(t)
	ssh := &healthFilesSSH{}
	_, err := resolveModuleHealthWith(context.Background(), healthTestRemote(t, ssh), moduleHealthRequest{Modules: []string{"monitor"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "读取 monitor 的健康密钥失败")
	assert.Contains(t, err.Error(), "/data/moox/prod/current/secrets/runtime-monitor.json")
	assert.Contains(t, err.Error(), "control:/data/moox/prod/current/secrets/runtime-admin.json", "也尝试了控制机上的共用密钥")
	assert.Contains(t, err.Error(), "MOOX_HEALTH_AUTH_SECRET_KEY")
	assert.True(t, ssh.closed, "出错时释放连接")
}

func TestResolveModuleHealthRejectsUnknownModule(t *testing.T) {
	_, err := resolveModuleHealthWith(context.Background(), healthTestRemote(t, &healthFilesSSH{}), moduleHealthRequest{Modules: []string{"nope"}})
	require.ErrorContains(t, err, "未知的模块")
}

// ---- 命令接入 ----

func useFakeHealth(t *testing.T, endpoints map[string]string, notDeployed ...string) *[]moduleHealthRequest {
	t.Helper()
	var requests []moduleHealthRequest
	previous := resolveModuleHealth
	resolveModuleHealth = func(_ context.Context, request moduleHealthRequest) (*moduleHealth, error) {
		requests = append(requests, request)
		result := map[string]string{}
		for _, name := range request.Modules {
			if endpoint, ok := endpoints[name]; ok {
				result[name] = endpoint
			}
		}
		return &moduleHealth{remote: newHostRemote(setupconfig.Manifest{}, "", nil), endpoints: result, NotDeployed: notDeployed}, nil
	}
	t.Cleanup(func() { resolveModuleHealth = previous; closeModuleHealth() })
	return &requests
}

func useUnavailableManifest(t *testing.T) {
	t.Helper()
	previous := resolveModuleHealth
	resolveModuleHealth = func(context.Context, moduleHealthRequest) (*moduleHealth, error) {
		return nil, fmt.Errorf("%w: missing", errManifestUnavailable)
	}
	t.Cleanup(func() { resolveModuleHealth = previous })
}

func TestLifecycleResolvesEndpointFromManifestUnlessGiven(t *testing.T) {
	requests := useFakeHealth(t, map[string]string{"factor-engine": "http://127.0.0.1:41000", "trade": "http://127.0.0.1:42000"})
	lifecycleFlags = lifecycleOptions{Module: "factor-engine", File: "moox.toml"}
	t.Cleanup(func() { lifecycleFlags = lifecycleOptions{} })
	cmd := &cobra.Command{Use: "status"}
	require.NoError(t, prepareLifecycleEndpoint(cmd, nil))
	assert.Equal(t, "http://127.0.0.1:41000", lifecycleFlags.Endpoint)
	assert.Equal(t, "factor-engine", (*requests)[0].GenericFrom, "lifecycle 只读通用密钥变量")

	lifecycleFlags = lifecycleOptions{Module: "strategy", Endpoint: "http://127.0.0.1:1"}
	require.NoError(t, prepareLifecycleEndpoint(cmd, nil))
	assert.Equal(t, "http://127.0.0.1:1", lifecycleFlags.Endpoint, "显式地址不解析")
	assert.Len(t, *requests, 1)

	lifecycleFlags = lifecycleOptions{Module: "factor-engine"}
	require.NoError(t, prepareLifecycleEndpoint(&cobra.Command{Use: "shadow"}, nil))
	assert.Equal(t, "http://127.0.0.1:42000", lifecycleFlags.Endpoint, "shadow 固定连接 trade")

	lifecycleFlags = lifecycleOptions{Module: "bogus"}
	require.ErrorContains(t, prepareLifecycleEndpoint(cmd, nil), "未知的模块")
	lifecycleFlags = lifecycleOptions{Module: "strategy"}
	require.ErrorContains(t, prepareLifecycleEndpoint(cmd, nil), "没有部署 strategy")
}

func TestLifecycleFallsBackToLoopbackWithoutManifest(t *testing.T) {
	useUnavailableManifest(t)
	lifecycleFlags = lifecycleOptions{Module: "factor-engine"}
	t.Cleanup(func() { lifecycleFlags = lifecycleOptions{} })
	var stderr strings.Builder
	cmd := &cobra.Command{Use: "status"}
	cmd.SetErr(&stderr)
	require.NoError(t, prepareLifecycleEndpoint(cmd, nil))
	assert.Equal(t, "http://127.0.0.1:11417", lifecycleFlags.Endpoint)
	assert.Contains(t, stderr.String(), "没有找到")
}

func TestSystemResolvesOnlyModulesWithoutExplicitEndpoint(t *testing.T) {
	requests := useFakeHealth(t, map[string]string{"collector": "http://127.0.0.1:1", "factor-engine": "http://127.0.0.1:2", "strategy": "http://127.0.0.1:3"}, "trade")
	systemFlags = systemOptions{Strategy: "http://explicit:9"}
	t.Cleanup(func() { systemFlags = systemOptions{} })
	var stderr strings.Builder
	status := &cobra.Command{Use: "status"}
	status.SetErr(&stderr)
	require.NoError(t, prepareSystemEndpoints(status, nil))
	assert.Equal(t, []string{"collector", "factor-engine", "trade"}, (*requests)[0].Modules, "显式给了地址的模块不再解析")
	assert.Equal(t, "http://127.0.0.1:1", systemFlags.Collector)
	assert.Equal(t, "http://127.0.0.1:2", systemFlags.Factor)
	assert.Equal(t, "http://explicit:9", systemFlags.Strategy)
	assert.Empty(t, systemFlags.Trade)
	assert.Contains(t, stderr.String(), "没有部署 trade")

	// 重置命令另外包含 factor-mgr、archive-mgr、monitor。
	systemFlags = systemOptions{}
	reset := &cobra.Command{Use: "reset"}
	plan := &cobra.Command{Use: "plan"}
	reset.AddCommand(plan)
	require.NoError(t, prepareSystemEndpoints(plan, nil))
	assert.Equal(t, []string{"collector", "factor-engine", "strategy", "trade", "factor-mgr", "archive-mgr", "monitor"}, (*requests)[1].Modules)
}

func TestSystemWithoutManifestKeepsExplicitBehavior(t *testing.T) {
	useUnavailableManifest(t)
	systemFlags = systemOptions{}
	t.Cleanup(func() { systemFlags = systemOptions{} })
	require.NoError(t, prepareSystemEndpoints(&cobra.Command{Use: "status"}, nil))
	assert.Empty(t, systemFlags.Collector)
}

func TestReplayAddsEndpointsForNetworkCommandsOnly(t *testing.T) {
	requests := useFakeHealth(t, map[string]string{"factor-engine": "http://127.0.0.1:1", "strategy": "http://127.0.0.1:2", "trade": "http://127.0.0.1:3"})
	cmd := &cobra.Command{Use: "replay"}

	args, err := prepareReplayEndpoints(cmd, []string{"start", "--file", "x.toml", "--instance", "i1", "--strategy-endpoint=http://mine:9"})
	require.NoError(t, err)
	assert.Equal(t, []string{"start", "--instance", "i1", "--strategy-endpoint=http://mine:9", "--factor-endpoint=http://127.0.0.1:1", "--trade-endpoint=http://127.0.0.1:3"}, args)
	assert.Equal(t, "x.toml", (*requests)[0].File)
	assert.Equal(t, []string{"factor-engine", "trade"}, (*requests)[0].Modules, "已给地址的模块不解析")

	args, err = prepareReplayEndpoints(cmd, []string{"status", "--replay", "r1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"status", "--replay", "r1"}, args)
	assert.Len(t, *requests, 1, "只读本地库的子命令不连接模块")

	useUnavailableManifest(t)
	args, err = prepareReplayEndpoints(cmd, []string{"report", "--replay", "r1"})
	require.NoError(t, err)
	assert.Equal(t, []string{"report", "--replay", "r1"}, args, "没有 moox.toml 时保持原样")
}

func TestTakeFlag(t *testing.T) {
	value, rest, found := takeFlag([]string{"a", "--file", "x", "b"}, "--file")
	assert.True(t, found)
	assert.Equal(t, "x", value)
	assert.Equal(t, []string{"a", "b"}, rest)
	value, rest, found = takeFlag([]string{"--file=y", "b"}, "--file")
	assert.True(t, found)
	assert.Equal(t, "y", value)
	assert.Equal(t, []string{"b"}, rest)
	_, rest, found = takeFlag([]string{"a"}, "--file")
	assert.False(t, found)
	assert.Equal(t, []string{"a"}, rest)
}

// 只能出站的主机（局域网因子机）没法 SSH：健康密钥借用控制机上的共用密钥文件，端点经中转得到。
func TestResolveModuleHealthBorrowsControlSecretForOutboundHost(t *testing.T) {
	clearHealthEnv(t)
	ssh := &healthFilesSSH{files: map[string]string{
		"cat /data/moox/prod/current/secrets/runtime-monitor.json": `{"MOOX_HEALTH_AUTH_ACCESS_KEY":"monitor-x","MOOX_HEALTH_AUTH_SECRET_KEY":"shared-secret"}`,
	}}
	remote := healthTestRemote(t, ssh)
	definition := remote.manifest.HostCatalog["factor"]
	definition.Reachability = "outbound"
	remote.manifest.HostCatalog["factor"] = definition
	remote.dial = func(_ context.Context, host setupconfig.Host) (setupssh.Client, error) {
		require.Equal(t, "control", host.Name, "只能出站的主机不能 SSH")
		return ssh, nil
	}
	secrets, err := remote.readHealthSecrets(context.Background(), "factor", "factor-engine")
	require.NoError(t, err)
	assert.Equal(t, "shared-secret", secrets["MOOX_HEALTH_AUTH_SECRET_KEY"])
	assert.Equal(t, []string{"cat /data/moox/prod/current/secrets/runtime-monitor.json"}, ssh.cats)
}
