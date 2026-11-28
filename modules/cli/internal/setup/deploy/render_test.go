package deploy

import (
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

// repositoryRoot 是仓库根：渲染测试直接读仓库里的配置模板、单元模板与示例配置。
const repositoryRoot = "../../../../.."

func readRepository(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(repositoryRoot, filepath.FromSlash(name)))
}

func exampleConfig(t *testing.T, extra string) *topology.Config {
	t.Helper()
	raw, err := readRepository("moox.toml.example")
	require.NoError(t, err)
	cfg, err := topology.Parse(append(raw, []byte(extra)...))
	require.NoError(t, err)
	return &cfg
}

// fakeIdentity 造一份形状和真的一样的身份材料：新签发的主机签名私钥、证书、根证书，内容是固定的假值。
func fakeIdentity(t *testing.T, cfg *topology.Config, catalog servicecatalog.Catalog, host string, components []string) Identity {
	t.Helper()
	identity := Identity{SignPublicKey: "public-key-of-" + host, Secrets: map[string][]byte{}, EventBus: map[string][]byte{}}
	identity.Secrets["host.key"] = []byte("private-key-of-" + host + "\n")
	if slices.Contains(components, "collector") {
		identity.SCFCollectorPublicKey = "public-key-of-scf-collector"
		identity.Secrets["scf-collector.key"] = []byte("private-key-of-scf-collector\n")
	}
	identity.Secrets["moox-ca.crt"], identity.Secrets["host.crt"], identity.Secrets["host.tls.key"] = []byte("ca\n"), []byte("cert\n"), []byte("key\n")
	for _, id := range components {
		component, _ := catalog.Component(id)
		for _, role := range component.EventBusRoles() {
			identity.EventBus[role+".yaml"] = []byte("version: 1\nurls:\n  - tls://203.0.113.11:4222\nusername: " + role + "\ntoken: fake-token-" + role + "\nca_file: ../moox-ca.crt\n")
		}
	}
	identity.EventBus["users.yaml"] = []byte("fake users.yaml\n")
	return identity
}

func renderHost(t *testing.T, cfg *topology.Config, host string) (Output, []string) {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	components, err := cfg.HostComponents(host)
	require.NoError(t, err)
	output, err := Render(Request{
		Config: cfg, Catalog: catalog, Host: cfg.HostByID(host), Components: components, HostComponents: components,
		ReadSource: readRepository, Identity: fakeIdentity(t, cfg, catalog, host, components),
		CLS: topology.CLSWriter{WriterSecretID: "cls-id", WriterSecretKey: "cls-key"},
	})
	require.NoError(t, err)
	return output, components
}

// goldenFile 报告一个渲染结果要不要进金样：业务配置、环境文件与单元文件。tRPC 框架配置只改了日志目录，另外核对。
func goldenFile(name string) bool {
	if strings.HasPrefix(name, "secrets/env/") {
		return true
	}
	return strings.HasPrefix(name, "config/") && slices.Contains([]string{"app.yaml", "storage.yaml", "engine.yaml"}, path.Base(name))
}

func compareGolden(t *testing.T, name string, actual []byte) {
	t.Helper()
	golden := filepath.Join("testdata", "golden", filepath.FromSlash(name))
	if os.Getenv("MOOX_UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(golden), 0o755))
		require.NoError(t, os.WriteFile(golden, actual, 0o644))
		return
	}
	expected, err := os.ReadFile(golden)
	require.NoError(t, err, "缺金样 %s：确认渲染结果后设 MOOX_UPDATE_GOLDEN=1 重新生成", golden)
	require.Equal(t, string(expected), string(actual), "%s 和金样不一致；确认变化符合预期后设 MOOX_UPDATE_GOLDEN=1 重新生成", name)
}

// 每个组件的配置、环境文件、单元文件各一份金样：按示例配置的四台主机渲染全部组件。
func TestRenderMatchesGoldenForEveryComponent(t *testing.T) {
	cfg := exampleConfig(t, "")
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, host := range cfg.Hosts() {
		output, components := renderHost(t, cfg, host.Name)
		for _, id := range components {
			seen[id] = true
			require.NotEmpty(t, output.Fingerprints[id], id)
			require.Contains(t, output.Units, UnitName(id))
			compareGolden(t, path.Join(host.Name, "units", UnitName(id)), output.Units[UnitName(id)])
		}
		names := []string{}
		for name := range output.Files {
			if goldenFile(name) {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			compareGolden(t, path.Join(host.Name, name), output.Files[name].Data)
		}
	}
	for _, component := range catalog.Components {
		require.True(t, seen[component.ID], "示例配置没有覆盖组件 %s", component.ID)
	}
}

// 渲染出的配置里路径都是绝对路径：不能留下相对工作目录或家目录的写法；tRPC 文件日志写到 logs/<组件>/。
func TestRenderedConfigsUseAbsolutePaths(t *testing.T) {
	cfg := exampleConfig(t, "")
	for _, host := range cfg.Hosts() {
		output, components := renderHost(t, cfg, host.Name)
		for name, file := range output.Files {
			if !strings.HasPrefix(name, "config/") || path.Ext(name) != ".yaml" {
				continue
			}
			// 这份是原样带上的业务种子，不含本机路径。
			if path.Base(name) == "collection-tasks.yaml" {
				continue
			}
			var document yaml.Node
			require.NoError(t, yaml.Unmarshal(file.Data, &document), name)
			var walk func(node *yaml.Node)
			walk = func(node *yaml.Node) {
				if node.Kind == yaml.ScalarNode && node.Tag == "!!str" {
					for _, prefix := range []string{"./", "../", "~/"} {
						require.False(t, strings.HasPrefix(node.Value, prefix), "%s 里还有相对路径 %q", name, node.Value)
					}
				}
				for _, child := range node.Content {
					walk(child)
				}
			}
			walk(&document)
		}
		for _, id := range components {
			raw, ok := output.Files[path.Join("config", id, "trpc_go.yaml")]
			if !ok {
				continue
			}
			document, err := parseYAML(raw.Data)
			require.NoError(t, err)
			for _, writer := range logWriters(document) {
				require.Equal(t, "/data/moox/logs/"+id, member(writer, "log_path").Value, id)
				require.Equal(t, "50", member(writer, "max_size").Value, "%s 的文件日志单个 50MiB", id)
				require.Equal(t, "5", member(writer, "max_backups").Value, "%s 的文件日志保留 5 份", id)
			}
		}
	}
}

// 因子引擎按配置文件所在目录解析相对路径，而 config/<组件> 每次部署整目录替换：引擎配置里的每个路径项
// （modules/factor/internal/engine/config.go 的 resolvePaths 列出的八项，加上网关密钥）都要渲染出来，并且落在数据目录或秘密目录。
// 模板里不写的项会用引擎的相对默认值，上面“没有相对路径”的检查看不到它，所以这里逐项点名。
func TestRenderedFactorEngineConfigSetsEveryPath(t *testing.T) {
	cfg := exampleConfig(t, "")
	output, _ := renderHost(t, cfg, cfg.ComponentHost("factor-engine"))
	document, err := parseYAML(output.Files["config/factor-engine/engine.yaml"].Data)
	require.NoError(t, err)
	data := "/data/moox/data/factor-engine/"
	for key, prefix := range map[string]string{
		"catalog_sync.state_file": data, "seriesstore.dir": data, "gap.ledger_dir": data, "watermark.state_file": data,
		"lifecycle.state_file": data, "params.cache_file": data, "udf.dir": data,
		"eventbus.credential_file": "/data/moox/secrets/", "gateway_client.key_file": "/data/moox/secrets/",
	} {
		node := document.Content[0]
		for _, part := range strings.Split(key, ".") {
			node = member(node, part)
			require.NotNil(t, node, "engine.yaml 没有渲染 %s", key)
		}
		require.True(t, strings.HasPrefix(node.Value, prefix), "%s = %q，应在 %s 下", key, node.Value, prefix)
	}
}

func TestRenderWritesIdentityAndRuntimeEnvironment(t *testing.T) {
	cfg := exampleConfig(t, "")
	cfg.Logging.CLS = topology.LoggingCLS{Enabled: true, Endpoint: "ap-nanjing.cls.tencentcs.com", TopicID: "topic-1", MinLevel: "warn"}
	output, _ := renderHost(t, cfg, "control")
	collector := string(output.Files["secrets/env/collector.env"].Data)
	require.Contains(t, collector, `MOOX_INSTANCE_ID="collector@control"`)
	require.Contains(t, collector, `MOOX_NODE_ID="control"`)
	require.Contains(t, collector, `MOOX_EVENTBUS_URL="tls://203.0.113.11:4222"`, "EventBus 不在控制机上，连它所在主机的对外地址")
	require.Contains(t, collector, `MOOX_LOG_CLS_SECRET_KEY="cls-key"`)
	require.NotContains(t, collector, "MOOX_BOOT_ID", "启动标识由进程自己生成")
	require.NotContains(t, collector, "MOOX_BINARY_SHA256", "二进制摘要由进程启动时自己算")
	// host-agent 没有声明 cls 凭据，拿不到写入密钥。
	require.NotContains(t, string(output.Files["secrets/env/host-agent.env"].Data), "MOOX_LOG_CLS")
	// 环境文件与全部秘密文件都是 0600。
	for name, file := range output.Files {
		require.Equal(t, os.FileMode(0o600), file.Mode, name)
	}
	storage, _ := renderHost(t, cfg, "storage")
	require.Contains(t, string(storage.Files["secrets/env/storage-primary.env"].Data), `MOOX_EVENTBUS_URL="tls://127.0.0.1:4222"`, "和 EventBus 同机用回环地址")
	require.Contains(t, string(storage.Files["secrets/eventbus/storage-eventbus.yaml"].Data), "ca_file: /data/moox/secrets/moox-ca.crt", "角色文件指向 MooX CA 根证书")
	// EventBus 没有自己的证书：服务端用所在主机的主机证书。
	bus := string(storage.Files["config/eventbus/app.yaml"].Data)
	require.Contains(t, bus, "cert_file: /data/moox/secrets/host.crt")
	require.Contains(t, bus, "key_file: /data/moox/secrets/host.tls.key")
	require.Contains(t, bus, "ca_file: /data/moox/secrets/moox-ca.crt")
	for _, name := range []string{"secrets/eventbus/ca.pem", "secrets/eventbus/server.pem", "secrets/eventbus/server-key.pem"} {
		require.NotContains(t, storage.Files, name)
	}
	require.Contains(t, storage.Files, "secrets/eventbus/users.yaml")
	require.NotContains(t, output.Files, "secrets/eventbus/users.yaml", "broker 的用户表只在 EventBus 所在主机")
	// 主机网关配置里的路径换成新布局。
	gateway, err := hostgatewayconfig.Decode(strings.NewReader(string(output.Files["config/host-gateway/app.yaml"].Data)))
	require.NoError(t, err)
	require.Equal(t, "/data/moox/secrets/host.crt", gateway.TLS.CertificateFile)
	require.Equal(t, "/data/moox/data/host-gateway", gateway.Store.Path)
	// 网关配置由部署按拓扑生成：本机的主机签名私钥，控制机上直连本机 Admin，其余主机经控制机的跨主机端口。
	require.Equal(t, "/data/moox/secrets/host.key", gateway.Control.KeyFile)
	require.Equal(t, "127.0.0.1:11112", gateway.Control.Target)
	storageGateway, err := hostgatewayconfig.Decode(strings.NewReader(string(storage.Files["config/host-gateway/app.yaml"].Data)))
	require.NoError(t, err)
	require.Equal(t, "storage", storageGateway.Host.ID)
	require.Equal(t, net.JoinHostPort(cfg.ControlHost().Address, "11003"), storageGateway.Control.Target)
}

func TestUnitOnlyListsSameHostDependenciesAndHasNoNetworkTarget(t *testing.T) {
	cfg := exampleConfig(t, "")
	control, _ := renderHost(t, cfg, "control")
	collector := string(control.Units["moox-collector.service"])
	// collector 的 after 里有 eventbus、storage-primary，它们不在控制机上，不写进 After=。
	require.Contains(t, collector, "After=moox-host-gateway.service moox-admin.service\n")
	require.NotContains(t, collector, "network-online.target")
	require.Contains(t, collector, "UnsetEnvironment=HTTP_PROXY HTTPS_PROXY http_proxy https_proxy\n")
	require.Contains(t, collector, "ExecStart=/data/moox/bin/moox-collector -config=/data/moox/config/collector/app.yaml -conf=/data/moox/config/collector/trpc_go.yaml\n")
	require.Contains(t, collector, "WorkingDirectory=/data/moox/data/collector\n")
	require.Contains(t, collector, "EnvironmentFile=/data/moox/secrets/env/collector.env\n")
	require.Contains(t, collector, "TimeoutStopSec=120\n")
	factor, components := renderHost(t, cfg, "factor")
	require.NotContains(t, components, "seaweedfs")
	require.Contains(t, string(factor.Units["moox-archive-worker.service"]), "After=moox-host-gateway.service\n")
	require.NotContains(t, string(factor.Files["config/archive-worker/app.yaml"].Data), "\nobject_store:")
}

func TestExternalObjectStoreRendersWhenConfigured(t *testing.T) {
	cfg := exampleConfig(t, "\n[archive_object_store]\nendpoint = \"cos.ap-nanjing.myqcloud.com\"\nregion = \"ap-nanjing\"\nbucket = \"moox-archive-1250000000\"\nurl_style = \"virtual\"\nsecret_id = \"archive-cos\"\n")
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	components, err := cfg.HostComponents("factor")
	require.NoError(t, err)
	require.NotContains(t, components, "seaweedfs")
	request := Request{Config: cfg, Catalog: catalog, Host: cfg.HostByID("factor"), Components: components, HostComponents: components, ReadSource: readRepository, Identity: fakeIdentity(t, cfg, catalog, "factor", components)}
	_, err = Render(request)
	require.ErrorContains(t, err, "没有取到它的密钥")
	request.ObjectStoreEnv = []byte("ACCESS_KEY_ID=a\nSECRET_ACCESS_KEY=b\n")
	output, err := Render(request)
	require.NoError(t, err)
	require.Equal(t, "ACCESS_KEY_ID=a\nSECRET_ACCESS_KEY=b\n", string(output.Files["secrets/archive-object-store.env"].Data))
	require.Contains(t, string(output.Files["config/archive-worker/app.yaml"].Data), "endpoint: cos.ap-nanjing.myqcloud.com")
}

func TestOverridesApplyAfterRenderingAndRejectUnknownPaths(t *testing.T) {
	cfg := exampleConfig(t, "\n[overrides.\"factor-engine\".\"engine.yaml\"]\n\"window.lookback\" = 60000\n\"compute.max_memory\" = \"8GiB\"\n")
	output, _ := renderHost(t, cfg, "factor")
	engine := string(output.Files["config/factor-engine/engine.yaml"].Data)
	require.Contains(t, engine, "lookback: 60000")
	require.Contains(t, engine, "max_memory: 8GiB")
	// 覆盖项算进指纹：改了调参，下次部署会重启这个组件。
	plain, _ := renderHost(t, exampleConfig(t, ""), "factor")
	require.NotEqual(t, plain.Fingerprints["factor-engine"], output.Fingerprints["factor-engine"])
	require.Equal(t, plain.Fingerprints["archive-worker"], output.Fingerprints["archive-worker"])
	// 别的主机上没有这个组件，这一项不影响它们。
	_, _ = renderHost(t, cfg, "control")

	for extra, message := range map[string]string{
		"\n[overrides.\"factor-engine\".\"engine.yaml\"]\n\"window.no_such_key\" = 1\n": "键路径 window.no_such_key 不存在",
		"\n[overrides.\"factor-engine\".\"engine.yaml\"]\n\"nothing.lookback\" = 1\n":   "键路径 nothing.lookback 不存在",
		"\n[overrides.\"factor-engine\".\"missing.yaml\"]\n\"a\" = 1\n":                 "没有 YAML 配置文件 missing.yaml",
	} {
		cfg := exampleConfig(t, extra)
		catalog, err := servicecatalog.LoadEmbedded()
		require.NoError(t, err)
		components, err := cfg.HostComponents("factor")
		require.NoError(t, err)
		_, err = Render(Request{Config: cfg, Catalog: catalog, Host: cfg.HostByID("factor"), Components: components, HostComponents: components, ReadSource: readRepository, Identity: fakeIdentity(t, cfg, catalog, "factor", components)})
		require.ErrorContains(t, err, message)
	}
}

func TestFingerprintIgnoresReissuedHostCertificate(t *testing.T) {
	cfg := exampleConfig(t, "")
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	components, err := cfg.HostComponents("overseas")
	require.NoError(t, err)
	request := Request{Config: cfg, Catalog: catalog, Host: cfg.HostByID("overseas"), Components: components, HostComponents: components, ReadSource: readRepository, Identity: fakeIdentity(t, cfg, catalog, "overseas", components)}
	first, err := Render(request)
	require.NoError(t, err)
	request.Identity.Secrets["host.crt"], request.Identity.Secrets["host.tls.key"] = []byte("reissued\n"), []byte("reissued-key\n")
	second, err := Render(request)
	require.NoError(t, err)
	require.Equal(t, first.Fingerprints, second.Fingerprints, "主机证书每次签发都不同，不能让每次部署都重启网关")
	// 例行升级：密钥没换，身份包里没有私钥文件。指纹不变，不重启。
	delete(request.Identity.Secrets, "host.key")
	kept, err := Render(request)
	require.NoError(t, err)
	require.Equal(t, first.Fingerprints, kept.Fingerprints, "密钥没换时不能因为包里没有私钥文件而重启")
	require.NotContains(t, kept.Files, "secrets/host.key", "没签发新私钥时不写这个文件，目标机上的原样沿用")
	// 换了主机签名密钥：这台主机上用它签名的组件都要重启；第三方程序不签名，不受影响。
	request.Identity.SignPublicKey = "rotated-public-key"
	request.Identity.Secrets["host.key"] = []byte("rotated\n")
	rotated, err := Render(request)
	require.NoError(t, err)
	for _, id := range components {
		require.NotEqual(t, first.Fingerprints[id], rotated.Fingerprints[id], "换了主机密钥要重启 %s", id)
	}
}

// scf-collector 的密钥只影响 collector；开不开外部入口只影响主机网关（它的配置里多一个监听地址）。
func TestFingerprintFollowsOnlyTheKeysAComponentUses(t *testing.T) {
	cfg := exampleConfig(t, "")
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	for host, sensitive := range map[string]string{"control": "collector", "storage": "host-gateway"} {
		components, err := cfg.HostComponents(host)
		require.NoError(t, err)
		request := Request{Config: cfg, Catalog: catalog, Host: cfg.HostByID(host), Components: components, HostComponents: components, ReadSource: readRepository, Identity: fakeIdentity(t, cfg, catalog, host, components)}
		first, err := Render(request)
		require.NoError(t, err)
		if sensitive == "collector" {
			request.Identity.SCFCollectorPublicKey = "rotated-scf-public-key"
		} else {
			require.True(t, request.Host.ExternalEntry, "示例配置里存储机开外部入口")
			require.Contains(t, string(first.Files["config/host-gateway/app.yaml"].Data), "external_addr: 0.0.0.0:11004")
			request.Host.ExternalEntry = false
		}
		second, err := Render(request)
		require.NoError(t, err)
		for _, id := range components {
			if id == sensitive {
				require.NotEqual(t, first.Fingerprints[id], second.Fingerprints[id], id)
			} else {
				require.Equal(t, first.Fingerprints[id], second.Fingerprints[id], id)
			}
		}
	}
}

func TestEnvironmentFileEscapesValues(t *testing.T) {
	raw, err := encodeEnvironment(map[string]string{"B": `a"b\c`, "A": "plain value"})
	require.NoError(t, err)
	require.Equal(t, "A=\"plain value\"\nB=\"a\\\"b\\\\c\"\n", string(raw))
	_, err = encodeEnvironment(map[string]string{"A": "line\nbreak"})
	require.Error(t, err)
}

func TestStatusAndVersionChange(t *testing.T) {
	version, err := ParseVersion([]byte(`{"version":"v0.0.2","commit":"abc","catalog_sha256":"sha256:x","deployed_at":"2026-11-16T00:00:00Z","components":{"admin":{"binaries":{"moox-admin":"aa"},"fingerprint":"sha256:1"}}}`))
	require.NoError(t, err)
	require.False(t, version.changed("admin", ComponentVersion{Binaries: map[string]string{"moox-admin": "aa"}, Fingerprint: "sha256:1"}))
	require.True(t, version.changed("admin", ComponentVersion{Binaries: map[string]string{"moox-admin": "bb"}, Fingerprint: "sha256:1"}))
	require.True(t, version.changed("admin", ComponentVersion{Binaries: map[string]string{"moox-admin": "aa"}, Fingerprint: "sha256:2"}))
	require.True(t, version.changed("monitor", ComponentVersion{}))
	var none *Version
	require.True(t, none.changed("admin", ComponentVersion{}))
	require.False(t, none.known("admin"))
	empty, err := ParseVersion(nil)
	require.NoError(t, err)
	require.Nil(t, empty)

	status := Status("control", []string{"host-gateway", "admin", "monitor"}, version,
		[]string{"moox-admin.service", "moox-host-gateway.service", "moox-old.service"}, []string{"moox-admin.service"})
	require.True(t, status.Installed)
	require.Equal(t, "v0.0.2", status.Version)
	require.Equal(t, []ComponentStatus{
		{ID: "host-gateway", State: "inactive"}, {ID: "admin", State: "active", Installed: true},
		{ID: "monitor", State: "missing"}, {ID: "old", State: "inactive", Extra: true},
	}, status.Components)
	// 已部署：version.json 里有记录或还有单元文件；version.json 里记着、但已不属于这台主机的组件也列出来（Extra）。
	require.True(t, status.Components[0].Deployed())
	require.False(t, status.Components[2].Deployed())
	moved := Status("control", []string{"host-gateway"}, version, nil, nil)
	require.Equal(t, []ComponentStatus{{ID: "host-gateway", State: "missing"}, {ID: "admin", State: "missing", Installed: true, Extra: true}}, moved.Components)
	require.False(t, Status("storage", []string{"eventbus"}, nil, nil, nil).Installed)
}

func TestPreflightRenderChecksAllHostsAndRejectsObsoleteOverrides(t *testing.T) {
	secrets, err := topology.ParseSecrets([]byte(`[admin]
username = "admin"
password = "admin-test-password"

[tencent_cloud]
secret_id = "AKID-test-secret"
secret_key = "cloud-test-secret"

[logging.cls]
writer_secret_id = "cls-id"
writer_secret_key = "cls-key"
`))
	require.NoError(t, err)

	// 示例拓扑与带外部对象存储配置的拓扑均可通过预检试渲染。
	cfg := exampleConfig(t, "\n[archive_object_store]\nendpoint = \"cos.ap-nanjing.myqcloud.com\"\nregion = \"ap-nanjing\"\nbucket = \"moox-archive-1250000000\"\nurl_style = \"virtual\"\nsecret_id = \"archive-cos\"\n")
	require.NoError(t, PreflightRender(cfg, &secrets, repositoryRoot))

	// 旧键路径在预检阶段即报错，不必等到清空主机后部署才暴露。
	cases := map[string]string{
		"collector_event_bus":    "\n[overrides.collector.\"app.yaml\"]\n\"event_bus.credential_file\" = \"/tmp/eb.yaml\"\n",
		"monitor_page_size":      "\n[overrides.monitor.\"app.yaml\"]\n\"kline_freshness.inventory_page_size\" = 100\n",
		"monitor_policy_path":    "\n[overrides.monitor.\"app.yaml\"]\n\"metrics.dataset_health_policy_path\" = \"/tmp/p.yaml\"\n",
		"archive_mgr_retention":  "\n[overrides.\"archive-mgr\".\"app.yaml\"]\n\"retention.other\" = 7\n",
		"strategy_database_flat": "\n[overrides.strategy.\"app.yaml\"]\n\"database.no_such_key\" = \"/tmp/s.db\"\n",
	}
	for name, extra := range cases {
		t.Run(name, func(t *testing.T) {
			badCfg := exampleConfig(t, extra)
			err := PreflightRender(badCfg, &secrets, repositoryRoot)
			require.ErrorContains(t, err, "config_invalid")
		})
	}
}
