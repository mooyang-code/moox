package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// 主机上共用的密钥文件（<root>/secrets 下）。health-auth.env 与 storage-internal-auth.env 以 control 上的为准，
// 部署其他主机时复制过去；其余的只在本机生成。
const (
	secretHealthAuth          = "health-auth.env"
	secretStorageInternalAuth = "storage-internal-auth.env"
	secretStorageNodeAuth     = "storage-node-auth.env"
	secretAdminJWT            = "admin-jwt.env"
	secretNotification        = "notification.env"
	secretAdminEncryptionKey  = "admin-encryption-key"
	eventBusCA                = "ca.pem"
	eventBusMetricsPublisher  = "metrics-publisher.yaml"
)

// component 渲染一个组件：公共的身份、上报与健康探测，加上组件自己的配置。
func (r *renderer) component(id string) (Component, error) {
	definition, ok := r.catalog.Component(id)
	if !ok {
		return Component{}, fmt.Errorf("组件目录中没有 %s", id)
	}
	c := Component{
		ID: id, Binary: definition.Binary, Binaries: []string{definition.Binary}, Workdir: id,
		StartupGrace: 60, StopTimeout: 30,
		Env: []string{
			"MOOX_SERVICE_NAME=" + id,
			"MOOX_INSTANCE_ID=" + id + "@" + r.host.ID,
			"MOOX_NODE_ID=" + r.host.ID,
			"MOOX_VERSION=" + r.opts.Version,
			"MOOX_OTEL_SERVICE_NAME=moox-" + id,
			"MOOX_EVENTBUS_NATS_URL=" + r.eventBusURL(),
		},
		DataDirs: []string{"data/" + id},
	}
	switch definition.Health.Kind {
	case servicecatalog.HealthReadyz:
		c.Health = Health{Kind: "readyz", Port: definition.Health.Port}
		c.SecretEnv = append(c.SecretEnv, secretHealthAuth)
		c.SharedSecrets = append(c.SharedSecrets, secretHealthAuth)
	case servicecatalog.HealthHTTPS:
		c.Health = Health{Kind: "https", Port: definition.Health.Port,
			URL: "https://" + net.JoinHostPort(r.host.Address, strconv.Itoa(definition.Health.Port)) + "/"}
	default:
		c.Health = Health{Kind: "none"}
	}
	if definition.Observability.Transport == servicecatalog.TransportReporter {
		c.Env = append(c.Env,
			"MOOX_METRICS_EVENTBUS_URL="+r.eventBusURL(),
			"MOOX_METRICS_EVENTBUS_CREDENTIAL_FILE="+r.eventBusFile(eventBusMetricsPublisher),
		)
		c.EventBusFiles = append(c.EventBusFiles, eventBusMetricsPublisher, eventBusCA)
	}
	render, ok := componentRenderers[id]
	if !ok {
		return Component{}, fmt.Errorf("不知道怎样部署组件 %s", id)
	}
	if err := render(r, &c); err != nil {
		return Component{}, err
	}
	c.EventBusFiles = unique(c.EventBusFiles)
	c.SharedSecrets = unique(c.SharedSecrets)
	c.SecretEnv = unique(c.SecretEnv)
	return c, nil
}

var componentRenderers = map[string]func(*renderer, *Component) error{
	"host-gateway":    renderHostGateway,
	"host-agent":      renderHostAgent,
	"console-proxy":   renderConsoleProxy,
	"web-host":        renderWebHost,
	"admin":           renderAdmin,
	"eventbus":        renderEventBus,
	"monitor":         renderMonitor,
	"collector":       renderCollector,
	"cloudnode":       renderCloudNode,
	"factor-mgr":      renderFactorMgr,
	"strategy":        renderStrategy,
	"archive":         renderArchive,
	"trade":           renderTrade,
	"storage-primary": renderStoragePrimary,
	"storage-node":    renderStorageNode,
	"storage-view":    renderStorageView,
	"access":          renderAccess,
	"egress-proxy":    renderEgressProxy,
}

// ownCallerKey 让组件使用以自己的组件 ID 为身份的签名密钥。
func ownCallerKey(c *Component) {
	c.CallerKeys = append(c.CallerKeys, CallerKey{Identity: c.ID, File: "caller-" + c.ID + ".key"})
}

func renderHostGateway(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-host-gateway-cli")
	c.BuildTargets = []string{"host-gateway"}
	c.Args = []string{"-config=config/app.yaml", "-conf=config/trpc_go.yaml"}
	if !r.isControl() {
		c.CallerKeys = append(c.CallerKeys, CallerKey{Identity: servicecatalog.HostGatewayIdentity(r.host.ID), File: "caller-host-gateway.key"})
	}
	if err := r.patchYAML("modules/hostgateway/config/app.yaml", "host-gateway/config/app.yaml", func(doc *yamlDoc) error {
		values := map[string]any{
			"host.id":            r.host.ID,
			"tls.cert_file":      r.rootPath("certs", "host-gateway", "server.crt"),
			"tls.key_file":       r.rootPath("certs", "host-gateway", "server.key"),
			"tls.ca_file":        r.caFile(),
			"control.caller":     servicecatalog.HostGatewayIdentity(r.host.ID),
			"store.path":         r.dataPath("host-gateway"),
			"server.health_addr": net.JoinHostPort(r.healthIP(), "11012"),
		}
		if r.isControl() {
			// control 的主机网关直连本机的网关控制，不需要签名密钥。
			values["control.target"] = "127.0.0.1:11112"
			doc.remove("control.key_file")
		} else {
			values["control.target"] = net.JoinHostPort(r.manifest.ControlHost().Address, "11003")
			values["control.key_file"] = r.secretPath("caller-host-gateway.key")
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/hostgateway/config/trpc_go.yaml", c.ID, "")
}

func renderHostAgent(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-host-agent-cli")
	c.BuildTargets = []string{"host-agent"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	c.EventBusFiles = append(c.EventBusFiles, "hostagent-publisher.yaml", eventBusCA)
	healthAddr := net.JoinHostPort(r.healthIP(), strconv.Itoa(c.Health.Port))
	c.Env = append(c.Env, "MOOX_HOST_AGENT_HEALTH_ADDR="+healthAddr)
	if err := r.patchYAML("modules/hostagent/config/app.yaml", "host-agent/config/app.yaml", func(doc *yamlDoc) error {
		if err := doc.set("eventbus_config", r.eventBusFile("hostagent-publisher.yaml")); err != nil {
			return err
		}
		return doc.set("health_addr", healthAddr)
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/hostagent/config/trpc_go.yaml", c.ID, "trpc.moox.hostagent.Health")
}

func renderConsoleProxy(r *renderer, c *Component) error {
	c.Caddy = true
	c.Args = []string{"run", "--config", "config/Caddyfile", "--adapter", "caddyfile"}
	c.Env = append(c.Env,
		"XDG_DATA_HOME="+r.dataPath("console-proxy"),
		"XDG_CONFIG_HOME="+r.dataPath("console-proxy", "config"),
	)
	caddyfile, err := renderCaddyfile(r.host.Address, ResolveTLSMode(r.host.TLSMode, r.host.Address), c.Health.Port)
	if err != nil {
		return err
	}
	r.addFile("console-proxy/config/Caddyfile", 0o644, caddyfile)
	return nil
}

func renderWebHost(r *renderer, c *Component) error {
	c.BuildTargets = []string{"web-host"}
	c.Env = append(c.Env,
		"MOOX_WEB_HOST_ADDR=127.0.0.1:9528",
		"MOOX_WEB_HOST_HEALTH_ADDR="+net.JoinHostPort(r.healthIP(), strconv.Itoa(c.Health.Port)),
	)
	r.addFile("web-host/.keep", 0o644, nil)
	return nil
}

func renderAdmin(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-admin-cli")
	c.BuildTargets = []string{"admin"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	c.CallerKeys = append(c.CallerKeys,
		CallerKey{Identity: servicecatalog.ConsoleCaller, File: "caller-console.key"},
		CallerKey{Identity: "admin", File: "caller-admin.key"},
	)
	c.SecretEnv = append(c.SecretEnv, secretAdminJWT, secretStorageInternalAuth, secretNotification)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	c.EventBusFiles = append(c.EventBusFiles, eventBusCA)
	db := r.dataPath("admin", "admin.db")
	c.Env = append(c.Env,
		"MOOX_ADMIN_DB_PATH="+db,
		"MOOX_ADMIN_ENCRYPTION_KEY_FILE="+r.secretPath(secretAdminEncryptionKey),
		"MOOX_PKI_CA_FILE="+r.rootPath("secrets", "pki", "ca.crt"),
		"MOOX_EVENTBUS_CA_FILE="+r.eventBusFile(eventBusCA),
	)
	c.Prestart = []string{shellCommand("$RELEASE/bin/moox-admin-cli", "init", "--db-path", db)}
	if err := r.patchYAML("modules/admin/config/app.yaml", "admin/config/app.yaml", func(doc *yamlDoc) error {
		for path, value := range map[string]string{
			"database.path":                   db,
			"gateway_client.ca_file":          r.caFile(),
			"gateway_client.cache_dir":        r.dataPath("admin", "gatewayclient"),
			"gateway_client.console_key_file": r.secretPath("caller-console.key"),
			"gateway_client.admin_key_file":   r.secretPath("caller-admin.key"),
		} {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := r.patchYAML("modules/admin/config/console.yaml", "admin/config/console.yaml", func(doc *yamlDoc) error {
		return doc.set("cache.data_dir", r.dataPath("admin", "badger"))
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/admin/config/trpc_go.yaml", c.ID, "trpc.moox.admin.Health")
}

func renderEventBus(r *renderer, c *Component) error {
	c.BuildTargets = []string{"eventbus"}
	c.Binaries = append(c.Binaries, "moox-admin-cli")
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	c.EventBusFiles = append(c.EventBusFiles, eventBusCA, "users.yaml", "server.pem", "server-key.pem", "internal-admin.yaml")
	// 消息总线的角色凭据保存在 Admin 的密钥表里，启动前导出到 <root>/secrets/eventbus（已有的复用）。
	db, keyFile, url := r.dataPath("admin", "admin.db"), r.secretPath(secretAdminEncryptionKey), r.manifest.EventBusURL()
	c.Prestart = []string{
		shellCommand("$RELEASE/bin/moox-admin-cli", "init", "--db-path", db),
		shellCommand("$RELEASE/bin/moox-admin-cli", "eventbus-credentials", "ensure", "--db-path", db, "--encryption-key-file", keyFile, "--nats-url", url),
		shellCommand("$RELEASE/bin/moox-admin-cli", "eventbus-credentials", "export", "--db-path", db, "--encryption-key-file", keyFile, "--nats-url", url,
			"--output-dir", r.rootPath("secrets", "eventbus")),
	}
	if err := r.patchYAML("modules/eventbus/config/app.yaml", "eventbus/config/app.yaml", func(doc *yamlDoc) error {
		values := map[string]any{
			"broker.host":                     "0.0.0.0",
			"broker.port":                     r.manifest.EventBus.Port,
			"broker.store_dir":                r.dataPath("eventbus", "jetstream"),
			"broker.auth.enabled":             true,
			"broker.auth.users_file":          r.eventBusFile("users.yaml"),
			"broker.tls.enabled":              r.manifest.EventBus.TLSEnabled,
			"broker.tls.cert_file":            r.eventBusFile("server.pem"),
			"broker.tls.key_file":             r.eventBusFile("server-key.pem"),
			"broker.tls.ca_file":              r.eventBusFile(eventBusCA),
			"internal_client.credential_file": r.eventBusFile("internal-admin.yaml"),
			"internal_client.tls_ca_file":     r.eventBusFile(eventBusCA),
			"health.addr":                     net.JoinHostPort(r.healthIP(), strconv.Itoa(c.Health.Port)),
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/eventbus/config/trpc_go.yaml", c.ID, "trpc.moox.eventbus.Health")
}

func renderMonitor(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-monitor-cli")
	c.BuildTargets = []string{"monitor"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.SecretEnv = append(c.SecretEnv, secretStorageInternalAuth, secretNotification)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	c.EventBusFiles = append(c.EventBusFiles, "monitor-observability.yaml", eventBusCA)
	// 就绪要求 Storage 里已有 mooxsys 空间和监控数据集：空环境里要等 setup init 之后才能就绪。
	c.NeedsMetadata = true
	db := r.dataPath("monitor", "monitor.db")
	policy := r.currentPath("monitor", "config", "dataset-health-policy.yaml")
	// 策略文件和它的校验和一起交给 Monitor：校验和不一致或缺失时 Monitor 拒绝启动，防止策略被悄悄改动。
	policyRaw, err := r.readRepositoryFile("config/setup/dataset-health-policy.yaml")
	if err != nil {
		return err
	}
	policySum := sha256.Sum256(policyRaw)
	c.Env = append(c.Env, "MOOX_DATASET_HEALTH_POLICY="+policy, "MOOX_DATASET_HEALTH_POLICY_HASH=sha256:"+hex.EncodeToString(policySum[:]))
	c.Prestart = []string{shellCommand("$RELEASE/bin/moox-monitor-cli", "init", "--db-path", db)}
	if err := r.copyFile("config/setup/dataset-health-policy.yaml", "monitor/config/dataset-health-policy.yaml"); err != nil {
		return err
	}
	if err := r.patchYAML("modules/monitor/config/app.yaml", "monitor/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		values := map[string]any{
			"database.path":                      db,
			"health.addr":                        net.JoinHostPort(r.healthIP(), strconv.Itoa(c.Health.Port)),
			"instance.instance_id":               c.ID + "@" + r.host.ID,
			"observability.eventbus_urls":        []string{r.eventBusURL()},
			"observability.credential_file":      r.eventBusFile("monitor-observability.yaml"),
			"metrics.dataset_health_policy_path": policy,
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/monitor/config/trpc_go.yaml", c.ID, "trpc.moox.monitor.Health")
}

func renderCollector(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-collector-cli")
	c.BuildTargets = []string{"collector"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.SecretEnv = append(c.SecretEnv, secretStorageInternalAuth)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	c.EventBusFiles = append(c.EventBusFiles, "market-fetch-publisher.yaml", "collector-market-fetch-consumer.yaml", eventBusCA)
	c.StartupGrace = 120
	// 启动时要为每个采集任务确认结果数据集，需要 Storage 里已有标签：空环境里要等 setup init 之后才能启动。
	c.NeedsMetadata = true
	db := r.dataPath("collector", "moox_collector.db")
	if spaces := r.marketFetchSpaces(); spaces != "" {
		c.Env = append(c.Env, "MOOX_SPACE_IDS="+spaces)
	}
	c.Prestart = []string{shellCommand("$RELEASE/bin/moox-collector-cli", "init", "--db-path", db,
		"--seed-file", "$RELEASE/collector/config/setup/collection-tasks.yaml")}
	if err := r.copyFile("config/setup/collection-tasks.yaml", "collector/config/setup/collection-tasks.yaml"); err != nil {
		return err
	}
	if err := r.copyTree("modules/collector/config/markets", "collector/config/markets"); err != nil {
		return err
	}
	// 行情采集的 Storage 绑定（现货、合约各绑定哪个数据集）：Collector 启动时从工作目录下的 config/sources/market 读取。
	if err := r.copyTree("modules/collector/configs/sources/market", "collector/config/sources/market"); err != nil {
		return err
	}
	// A 股各数据源（sina、eastmoney 等）的接口配置：单独一个目录（其中的 binance.yaml 与上面的加密货币绑定同名，不能混放），
	// 用环境变量告诉 Collector。
	if err := r.copyTree("modules/collector/configs/scf/stockcn/sources/market", "collector/config/sources/stockcn"); err != nil {
		return err
	}
	c.Env = append(c.Env, "MOOX_STOCK_CN_SOURCE_CONFIG_DIR="+r.currentPath("collector", "config", "sources", "stockcn"))
	raw, err := r.readRepositoryFile("modules/collector/config/app.yaml")
	if err != nil {
		return err
	}
	// 出口代理、SCF 地域黑名单、运行数据保留和 stockcn 容量由 moox.toml 决定。
	rendered, err := renderCollectorRuntime(r.manifest, raw)
	if err != nil {
		return err
	}
	doc, err := parseYAML("collector/config/app.yaml", rendered)
	if err != nil {
		return err
	}
	if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
		return err
	}
	if err := doc.set("database.path", db); err != nil {
		return err
	}
	out, err := doc.bytes()
	if err != nil {
		return err
	}
	r.addFile("collector/config/app.yaml", 0o644, out)
	return r.trpcConfig("modules/collector/config/trpc_go.yaml", c.ID, "trpc.moox.collector.Health")
}

func renderCloudNode(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-cloudnode-cli")
	c.BuildTargets = []string{"cloudnode"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.SecretEnv = append(c.SecretEnv, secretNotification)
	db := r.dataPath("cloudnode", "moox_cloudnode.db")
	c.Prestart = []string{shellCommand("$RELEASE/bin/moox-cloudnode-cli", "init", "--db-path", db)}
	if err := r.patchYAML("modules/cloudnode/config/app.yaml", "cloudnode/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		return doc.set("database.path", db)
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/cloudnode/config/trpc_go.yaml", c.ID, "trpc.moox.cloudnode.Health")
}

func renderFactorMgr(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-factor-mgr-cli")
	c.BuildTargets = []string{"factor-mgr"}
	c.CGOTarget = "factor-mgr"
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.SecretEnv = append(c.SecretEnv, secretStorageInternalAuth)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	c.EventBusFiles = append(c.EventBusFiles, "factor-eventbus.yaml", eventBusCA)
	db := r.dataPath("factor-mgr", "factor.db")
	c.Env = append(c.Env, "MOOX_FACTOR_DB_PATH="+db)
	// 创建或修改因子定义时会试加载源码，需要带 pandas 和 numpy 的 python3。
	c.Prestart = []string{`python3 -c 'import pandas, numpy' >/dev/null 2>&1 || { echo "因子管理需要安装了 pandas 和 numpy 的 python3（modules/factor/pyworker/runtime-requirements.txt）" >&2; exit 1; }`}
	if err := r.patchYAML("modules/factor/config/app.yaml", "factor-mgr/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		return doc.set("database.path", db)
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/factor/config/trpc_go.yaml", c.ID, "trpc.moox.factor.Health")
}

func renderStrategy(r *renderer, c *Component) error {
	c.BuildTargets = []string{"strategy"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.SecretEnv = append(c.SecretEnv, secretStorageInternalAuth)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	c.EventBusFiles = append(c.EventBusFiles, "strategy-eventbus.yaml", eventBusCA)
	if err := r.patchYAML("modules/strategy/config/app.yaml", "strategy/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		values := map[string]any{
			"database":                 r.dataPath("strategy", "strategy.sqlite"),
			"instance_id":              c.ID + "@" + r.host.ID,
			"eventbus.urls":            []string{r.eventBusURL()},
			"eventbus.credential_file": r.eventBusFile("strategy-eventbus.yaml"),
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/strategy/config/trpc_go.yaml", c.ID, "trpc.moox.strategy.Health")
}

func renderArchive(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-archive-cli")
	c.BuildTargets = []string{"archive"}
	c.Args = []string{"-config=config/app.yaml", "-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.EventBusFiles = append(c.EventBusFiles, "archive-eventbus.yaml", eventBusCA)
	if err := r.patchYAML("modules/archive/config/app.yaml", "archive/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		values := map[string]any{
			"archive.root_dir":                 r.dataPath("archive", "parquet"),
			"archive.state_dir":                r.dataPath("archive", "state"),
			"archive.eventbus.urls":            []string{r.eventBusURL()},
			"archive.eventbus.credential_file": r.eventBusFile("archive-eventbus.yaml"),
			"health.addr":                      net.JoinHostPort(r.healthIP(), strconv.Itoa(c.Health.Port)),
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/archive/config/trpc_go.yaml", c.ID, "trpc.moox.archive.Health")
}

func renderTrade(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-trade-cli")
	c.BuildTargets = []string{"trade"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.EventBusFiles = append(c.EventBusFiles, "trade-eventbus.yaml", eventBusCA)
	db := r.dataPath("trade", "moox_trade.db")
	c.Prestart = []string{shellCommand("$RELEASE/bin/moox-trade-cli", "init", "--db-path", db)}
	if err := r.patchYAML("modules/trade/config/app.yaml", "trade/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		values := map[string]any{
			"database.path":            db,
			"eventbus.urls":            []string{r.eventBusURL()},
			"eventbus.credential_file": r.eventBusFile("trade-eventbus.yaml"),
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/trade/config/trpc_go.yaml", c.ID, "trpc.moox.trade.Health")
}

// 存储主服务与存储视图共用 <root>/data/storage（元数据、Pebble 和视图索引），数据节点使用 <root>/data/storage-node。
func (r *renderer) storageEnv(c *Component, role, home, configPath string) {
	c.Env = append(c.Env,
		"MOOX_STORAGE_ROLE="+role,
		"MOOX_STORAGE_HOME="+home,
		"MOOX_STORAGE_CONFIG="+configPath,
		"MOOX_STORAGE_EVENTBUS_URL="+r.eventBusURL(),
		"MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE="+r.eventBusFile("storage-eventbus.yaml"),
	)
	c.EventBusFiles = append(c.EventBusFiles, "storage-eventbus.yaml", eventBusCA)
	c.SecretEnv = append(c.SecretEnv, secretStorageNodeAuth)
	c.StopTimeout = 120
}

// storagePaths 把存储配置（storage 段）中的数据路径改到 home 下，存储策略写到组件自己的配置目录。
func (r *renderer) storagePaths(doc *yamlDoc, componentID, home string) error {
	values := map[string]any{
		"storage.root":                     home,
		"storage.eventbus.credential_file": r.eventBusFile("storage-eventbus.yaml"),
	}
	optional := map[string]any{
		"storage.metadata.path":           home + "/metadata/storage_metadata.db",
		"storage.devices.pebble_path":     home + "/pebble",
		"storage.devices.view_index_root": home + "/view-indexes",
		"storage.policy_file":             r.currentPath(componentID, "config", "storage-policy.json"),
	}
	for path, value := range optional {
		if _, ok := doc.lookup(path); ok {
			values[path] = value
		}
	}
	for path, value := range values {
		if err := doc.set(path, value); err != nil {
			return err
		}
	}
	if _, ok := doc.lookup("storage.policy_file"); ok {
		policy, err := json.MarshalIndent(r.manifest.StoragePolicy(), "", "  ")
		if err != nil {
			return fmt.Errorf("编码存储策略: %w", err)
		}
		r.addFile(componentID+"/config/storage-policy.json", 0o644, append(policy, '\n'))
	}
	return nil
}

func renderStoragePrimary(r *renderer, c *Component) error {
	c.Binaries = append(c.Binaries, "moox-storage-cli")
	c.BuildTargets = []string{"storage"}
	c.CGOTarget = "storage"
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	c.SecretEnv = append(c.SecretEnv, secretStorageInternalAuth)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	home := r.dataPath("storage")
	configPath := r.currentPath("storage-primary", "config", "storage.yaml")
	r.storageEnv(c, "primary", home, configPath)
	c.DataDirs = []string{"data/storage"}
	c.Prestart = []string{shellCommand("$RELEASE/bin/moox-storage-cli", "init",
		"--storage-conf="+configPath, "--schema-path=$RELEASE/storage-primary/schema/metadata.sql")}
	// 存储主服务就绪后登记本机的数据节点（已登记时不变）。
	c.Poststart = []string{shellCommand("$RELEASE/bin/moox-storage-cli", "register-node",
		"--metadata-target", "ip://127.0.0.1:20100", "--node-id", "storage-node-0",
		"--service-target", "ip://127.0.0.1:20107", "--name", "数据节点")}
	if err := r.copyFile("modules/storage/schema/metadata.sql", "storage-primary/schema/metadata.sql"); err != nil {
		return err
	}
	if err := r.patchYAML("modules/storage/config/storage.primary.yaml", "storage-primary/config/storage.yaml", func(doc *yamlDoc) error {
		return r.storagePaths(doc, c.ID, home)
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/storage/config/trpc_go.primary.yaml", c.ID, "trpc.moox.storage.Health")
}

func renderStorageNode(r *renderer, c *Component) error {
	c.BuildTargets = []string{"storage-node"}
	c.CGOTarget = "storage"
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	home := r.dataPath("storage-node")
	r.storageEnv(c, "node", home, r.currentPath("storage-node", "config", "storage.yaml"))
	c.Env = append(c.Env, "MOOX_STORAGE_NODE_ID=storage-node-0")
	if err := r.patchYAML("modules/storage/config/storage.node.yaml", "storage-node/config/storage.yaml", func(doc *yamlDoc) error {
		return r.storagePaths(doc, c.ID, home)
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/storage/config/trpc_go.node.yaml", c.ID, "trpc.moox.storage.Health")
}

func renderStorageView(r *renderer, c *Component) error {
	c.BuildTargets = []string{"storage"}
	c.CGOTarget = "storage"
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	c.SecretEnv = append(c.SecretEnv, secretStorageInternalAuth)
	c.SharedSecrets = append(c.SharedSecrets, secretStorageInternalAuth)
	home := r.dataPath("storage")
	configPath := r.currentPath("storage-view", "config", "trpc_go.yaml")
	r.storageEnv(c, "view", home, configPath)
	c.Env = append(c.Env, "MOOX_STORAGE_VIEW_DUCKDB_MEMORY_LIMIT=256MB")
	c.DataDirs = []string{"data/storage"}
	// 视图恢复时会先打开并校验 DuckDB 索引，可能很久才开始监听。
	c.StartupGrace = 900
	return r.patchYAML("modules/storage/config/storage_view/trpc_go.yaml", "storage-view/config/trpc_go.yaml", func(doc *yamlDoc) error {
		doc.setLogPath(r.rootPath("logs", c.ID))
		if err := doc.setServiceIP("trpc.moox.storage.Health", r.healthIP()); err != nil {
			return err
		}
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		return r.storagePaths(doc, c.ID, home)
	})
}

func renderAccess(r *renderer, c *Component) error {
	c.BuildTargets = []string{"access"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	ownCallerKey(c)
	if err := r.patchYAML("modules/access/config/app.yaml", "access/config/app.yaml", func(doc *yamlDoc) error {
		if err := r.gatewayClient(doc, "gateway_client", c.ID); err != nil {
			return err
		}
		if err := doc.set("principals_file", r.secretPath("access-principals.json")); err != nil {
			return err
		}
		return doc.set("nonce_path", r.dataPath("access", "nonces.db"))
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/access/config/trpc_go.yaml", c.ID, "trpc.moox.access.Health")
}

func renderEgressProxy(r *renderer, c *Component) error {
	c.BuildTargets = []string{"egress-proxy"}
	c.Args = []string{"-conf=config/trpc_go.yaml"}
	egress := r.manifest.EgressProxy
	if err := r.patchYAML("modules/egressproxy/config/app.yaml", "egress-proxy/config/app.yaml", func(doc *yamlDoc) error {
		values := map[string]any{
			"http.domains":           append([]string{}, egress.HTTPDomains...),
			"dns.domains":            append([]string{}, egress.DNS.Domains...),
			"dns.lookup_timeout":     strconv.Itoa(egress.DNS.LookupTimeoutMS) + "ms",
			"dns.probe_timeout":      strconv.Itoa(egress.DNS.ProbeTimeoutMS) + "ms",
			"dns.probe_port":         egress.DNS.ProbePort,
			"dns.cache_ttl":          strconv.Itoa(egress.DNS.CacheTTLSeconds) + "s",
			"dns.max_ips_per_domain": egress.DNS.MaxIPsPerDomain,
		}
		for path, value := range values {
			if err := doc.set(path, value); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return r.trpcConfig("modules/egressproxy/config/trpc_go.yaml", c.ID, "trpc.moox.egress.Health")
}

// marketFetchSpaces 是 Collector 负责行情采集的空间（MOOX_SPACE_IDS），取自启用的 SCF 采集配置。
func (r *renderer) marketFetchSpaces() string {
	if !r.manifest.SCFFetcher.Enabled {
		return ""
	}
	var ids []string
	for _, space := range r.manifest.SCFFetcher.Spaces {
		if id := strings.TrimSpace(space.SpaceID); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(unique(ids), ",")
}

func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
