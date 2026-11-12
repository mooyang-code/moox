package unitdeploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/security"
)

func unitComponents(manifest setupconfig.Manifest, hostID, profile string) ([]string, error) {
	if profile == "host" {
		return []string{"host-gateway", "host-agent"}, nil
	}
	available, err := unitpackage.Components(profile)
	if err != nil {
		return nil, err
	}
	var selected []string
	for _, component := range available {
		if slices.Contains(manifest.Deployments[hostID], component.ID) {
			selected = append(selected, component.ID)
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("requested software unit has no registered component on this host")
	}
	return selected, nil
}

func unitConfiguration(snapshot *setupconfig.Snapshot, profile string) ([]byte, error) {
	if profile == "egress-proxy" {
		return setupconfig.RenderEgressConfig(snapshot, nil)
	}
	if profile == "storage" {
		return snapshot.Manifest.StoragePolicy().Encode()
	}
	return nil, nil
}

// defaultSpaceID 是 Collector 行情调度的默认空间。
const defaultSpaceID = "crypto"

// collectorStorageAppID 是 Collector 与 SCF 函数写 Storage 的应用身份，collector-stream 沿用它。
const collectorStorageAppID = "moox-collector"

// serviceLogComponents 是接入了重要日志与 CLS 上报的组件，和 docs/模块/监控.md 的接入范围一致。
var serviceLogComponents = []string{
	"admin", "monitor", "collector", "cloudfunction", "factor-mgr", "archive-mgr", "strategy", "eventbus",
	"storage-primary", "storage-view", "trade", "collector-stream", "callback-dispatcher",
}

func unitEnvironment(manifest setupconfig.Manifest, identity runtimeIdentity, hostID string, components []string) (map[string]map[string]string, error) {
	health := map[string]string{"MOOX_HEALTH_AUTH_VERSION": "moox-health-v1", "MOOX_HEALTH_AUTH_ACCESS_KEY": identity.HealthAccessKey, "MOOX_HEALTH_AUTH_SECRET_KEY": identity.HealthSecret}
	environment := make(map[string]map[string]string, len(components))
	for _, id := range components {
		values := maps.Clone(health)
		environment[id] = values
		// 服务日志上报 CLS：只给接入了日志上报的组件（main 引入了 logcls 的那些）写入，别的组件不拿这把密钥。
		// 变量名用 MOOX_LOG_CLS_ 前缀，和云函数、CloudFunction 自己的 MOOX_CLS_* 分开（那一组是采集日志的主题和另一把密钥）。
		if cls := manifest.Logging.CLS; cls.Enabled && slices.Contains(serviceLogComponents, id) {
			values["MOOX_LOG_CLS_ENABLED"] = "true"
			values["MOOX_LOG_CLS_ENDPOINT"] = cls.Endpoint
			values["MOOX_LOG_CLS_TOPIC_ID"] = cls.TopicID
			values["MOOX_LOG_CLS_SECRET_ID"] = cls.WriterSecretID
			values["MOOX_LOG_CLS_SECRET_KEY"] = cls.WriterSecretKey
			values["MOOX_LOG_CLS_MIN_LEVEL"] = cls.MinLevel
			values["MOOX_LOG_CLS_SOURCE"] = hostID
		}
		if slices.Contains([]string{"monitor", "collector", "factor-mgr", "archive-mgr", "strategy", "admin"}, id) {
			if len(identity.StoragePrimarySecret) < 32 || (id == "strategy" || id == "admin") && len(identity.StorageViewSecret) < 32 {
				return nil, errors.New("control Storage clients require the original persistent role secrets")
			}
			values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"] = identity.StoragePrimarySecret
			// Admin 的 Console 代浏览器调用 Storage，需要 Primary 与 View 两个角色密钥。
			if id == "strategy" || id == "admin" {
				values["MOOX_STORAGE_VIEW_AUTH_SECRET"] = identity.StorageViewSecret
			}
		}
		if id == "monitor" {
			// 告警推送渠道来自 moox.toml 的 [notification]；Monitor 首次建库时据此写入全局渠道，没填地址就不写，用户稍后在管理台添加。
			values["MOOX_NOTIFICATION_CHANNEL_TYPE"] = manifest.Notification.ChannelType
			values["MOOX_NOTIFICATION_WEBHOOK_URL"] = manifest.Notification.WebhookURL
			values["MOOX_NOTIFICATION_SECRET"] = manifest.Notification.Secret
		}
		if id == "cloudfunction" {
			// 开通采集日志主题时的保留天数来自 moox.toml 的 [logging] retention_days（只在创建主题时生效）。
			values["MOOX_CLS_RETENTION_DAYS"] = strconv.FormatInt(manifest.Logging.RetentionDays, 10)
		}
		if id == "admin" {
			// 证书巡检每次从本机主机策略缓存读取告警渠道（HostAgent 从 Monitor 同步）。
			values["MOOX_HOST_POLICY_FILE"] = hostpolicy.Path(manifest.Paths.Resolved().DeployRoot)
		}
		if id == "collector" {
			// Collector 的行情调度必须有目标空间；与旧部署脚本一致，默认 crypto。
			values["MOOX_SPACE_ID"] = defaultSpaceID
			values["MOOX_SPACE_IDS"] = defaultSpaceID
		}
		if id == "collector-stream" {
			// collector-stream 写 Storage 沿用 moox-collector 身份（周期 RPC 只接受 Collector 身份）：
			// 在本机按 Storage 主密钥派生这一把应用密钥注入，主密钥不放到海外机。
			if len(identity.StoragePrimarySecret) < 32 {
				return nil, errors.New("collector-stream requires the original Storage primary secret to derive its app key")
			}
			keys, err := json.Marshal(map[string]string{collectorStorageAppID: security.HMACSHA256Hex(identity.StoragePrimarySecret, []byte(collectorStorageAppID))})
			if err != nil {
				return nil, err
			}
			values["MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"] = string(keys)
			values["MOOX_SPACE_ID"] = defaultSpaceID
		}
		if id != "storage-primary" && id != "storage-view" {
			continue
		}
		if len(identity.StoragePrimarySecret) < 32 || len(identity.StorageViewSecret) < 32 {
			return nil, errors.New("Storage requires its original complete private admin identity; missing credentials cannot be regenerated during deployment")
		}
		values["MOOX_STORAGE_HOME"] = "./var/storage"
		values["MOOX_STORAGE_ROLE"] = strings.TrimPrefix(id, "storage-")
		values["MOOX_STORAGE_CONFIG"] = "config/storage.yaml"
		values["MOOX_STORAGE_EVENTBUS_URL"] = manifest.EventBusURLFor(hostID)
		values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"] = identity.StoragePrimarySecret
		values["MOOX_STORAGE_VIEW_AUTH_SECRET"] = identity.StorageViewSecret
		values["MOOX_STORAGE_PRIMARY_TARGET"] = "ip://127.0.0.1:20102"
		values["MOOX_STORAGE_METADATA_TARGET"] = "ip://127.0.0.1:20100"
		values["MOOX_STORAGE_VIEW_TARGET"] = "ip://127.0.0.1:20103"
		// The prebuilt Storage templates bind internal RPC to loopback. Until
		// split-role routing is supplied, never report readiness for an
		// installation whose peers cannot reach one another.
		primary := manifest.ComponentHost("storage-primary")
		if primary == "" || manifest.HostByID(primary).Name == "" || primary != manifest.ComponentHost("storage-view") {
			return nil, errors.New("native Storage requires primary and view on one host; split-role RPC routing is not configured")
		}
	}
	return environment, nil
}

func prepareUnitSoftware(ctx context.Context, root, state *os.Root, options DeployOptions, arch, profile string, admin adminSoftware) (unitpackage.Result, string, error) {
	if options.downloads() {
		software, err := bundleSoftware(options.Bundle, profile, arch)
		return software, "", err
	}
	if admin.Architecture != arch {
		return unitpackage.Result{}, "", fmt.Errorf("target_arch_unsupported: 目标机是 %s，首次安装的软件是 %s，所有主机要同一架构", arch, admin.Architecture)
	}
	if profile == "host" {
		return admin.Packages[0], admin.Helper, nil
	}
	work, err := os.MkdirTemp(state.Name(), "software-")
	if err != nil {
		return unitpackage.Result{}, "", err
	}
	bin := options.BinaryDirectory
	if bin == "" {
		if profile == "storage" {
			return unitpackage.Result{}, "", errors.New("storage deployment requires prebuilt Linux artifacts from setup build-linux in --binary-dir")
		}
		bin = filepath.Join(work, "bin")
		if err := os.Mkdir(bin, 0o700); err != nil {
			return unitpackage.Result{}, "", err
		}
		if err := buildLocalTargets(ctx, options.RepositoryRoot, bin, arch, options.Log, []string{profile}); err != nil {
			return unitpackage.Result{}, "", err
		}
	}
	software, err := unitpackage.Package(ctx, unitpackage.Options{RepositoryRoot: options.RepositoryRoot, BinaryDirectory: bin, Output: filepath.Join(work, profile+".tar.gz"), Profile: profile, GOOS: "linux", GOARCH: arch})
	if err != nil {
		return software, "", err
	}
	return software, admin.Helper, nil
}
