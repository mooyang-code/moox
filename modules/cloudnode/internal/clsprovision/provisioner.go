// Package clsprovision 让 CloudNode 在创建或更新 SCF 采集函数时自动带上日志服务（CLS）配置：
// 首次使用时幂等开通 CLS（服务、日志集、主题、采集日志字段索引），再把上报所需的环境变量补进函数环境。
// 整个过程对用户无感知，任何失败都只记日志，绝不阻断函数的创建与更新。
package clsprovision

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"trpc.group/trpc-go/trpc-go/log"
)

// Config 是 CLS 开通与下发的配置。
type Config struct {
	// Enabled 为 false 时完全不介入函数环境。
	Enabled bool `yaml:"enabled"`
	// Region 是 CLS 资源所在地域（日志集与主题固定在这里创建）。
	Region string `yaml:"region"`
	// APIEndpoint 是 CLS 管理接口地址。
	APIEndpoint string `yaml:"api_endpoint"`
	// IngestEndpoint 是函数上报日志用的写入地址（函数经公网出口访问）。
	IngestEndpoint string `yaml:"ingest_endpoint"`
	LogsetName     string `yaml:"logset_name"`
	TopicName      string `yaml:"topic_name"`
	RetentionDays  int64  `yaml:"retention_days"`
	Partitions     int64  `yaml:"partitions"`
	// TimeoutMS 是函数单次上报 CLS 的超时。
	TimeoutMS int `yaml:"timeout_ms"`
	// WriterSecretID 与 WriterSecretKeyFile 指定函数上报日志使用的专用凭证（建议是只有该主题写入权限的子账号密钥）。
	// 两者都为空时退回使用云账号的管理密钥（仍能工作，但函数环境泄露会扩大到云资源控制面），并记一次告警。
	WriterSecretID      string `yaml:"writer_secret_id"`
	WriterSecretKeyFile string `yaml:"writer_secret_key_file"`
}

// DefaultConfig 返回默认配置：开启，沿用 moox-cli 初始化使用的固定日志集与主题名。
func DefaultConfig() Config {
	return Config{
		Enabled: true, Region: "ap-guangzhou", APIEndpoint: "cls.tencentcloudapi.com", IngestEndpoint: "ap-guangzhou.cls.tencentcs.com",
		LogsetName: "moox", TopicName: "moox-application", RetentionDays: 2, Partitions: 1, TimeoutMS: 3000,
	}
}

// Validate 校验启用时的配置完整性。
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	for name, value := range map[string]string{"region": c.Region, "api_endpoint": c.APIEndpoint, "ingest_endpoint": c.IngestEndpoint, "logset_name": c.LogsetName, "topic_name": c.TopicName} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("cls.%s is required when cls is enabled", name)
		}
	}
	if c.TimeoutMS < 100 || c.TimeoutMS > 3000 {
		return fmt.Errorf("cls.timeout_ms must be between 100 and 3000")
	}
	if (strings.TrimSpace(c.WriterSecretID) == "") != (strings.TrimSpace(c.WriterSecretKeyFile) == "") {
		return fmt.Errorf("cls.writer_secret_id and cls.writer_secret_key_file must be set together")
	}
	return nil
}

// writer 返回函数上报使用的凭证：配置了专用写入凭证就用它，否则退回云账号管理密钥。
func (p *Provisioner) writer(management Credential) (Credential, error) {
	id, file := strings.TrimSpace(p.cfg.WriterSecretID), strings.TrimSpace(p.cfg.WriterSecretKeyFile)
	if id == "" {
		p.warnOnce.Do(func() {
			log.Warnf("cloudnode cls: functions report with the cloud account management key; configure cls.writer_secret_id and cls.writer_secret_key_file with a CLS write-only key")
		})
		return management, nil
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return Credential{}, fmt.Errorf("read cls writer secret key file failed")
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return Credential{}, fmt.Errorf("cls writer secret key file is empty")
	}
	return Credential{SecretID: id, SecretKey: key}, nil
}

// Credential 是云账号的 API 密钥。
type Credential struct{ SecretID, SecretKey string }

// Factory 用云账号密钥创建 CLS 管理客户端。
type Factory func(credential Credential, region, endpoint string) (tencent.CLSAPI, error)

// DefaultFactory 使用腾讯云 SDK。
func DefaultFactory(credential Credential, region, endpoint string) (tencent.CLSAPI, error) {
	return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{SecretID: credential.SecretID, SecretKey: credential.SecretKey, Region: region, Endpoint: endpoint})
}

type settings struct {
	logsetID, topicID string
	resolvedAt        time.Time
}

// Provisioner 缓存每个云账号的 CLS 资源，并把上报环境补进函数环境。
type Provisioner struct {
	cfg     Config
	factory Factory
	now     func() time.Time

	warnOnce sync.Once
	mu       sync.Mutex
	accounts map[string]*accountEntry
}

// accountEntry 让开通按云账号串行：同一账号的并发请求合并，不同账号互不等待。
type accountEntry struct {
	mu    sync.Mutex
	value settings
	set   bool
}

const (
	cacheTTL         = 10 * time.Minute
	provisionTimeout = 60 * time.Second
	// failureBackoff 是开通失败后的退避：期间不再重试，避免每次创建函数都重复打云接口。
	failureBackoff = 2 * time.Minute
)

// New 创建 Provisioner；配置未启用时返回的实例对 Apply 恒为空操作。
func New(cfg Config, factory Factory) *Provisioner {
	if factory == nil {
		factory = DefaultFactory
	}
	return &Provisioner{cfg: cfg, factory: factory, now: time.Now, accounts: map[string]*accountEntry{}}
}

// Apply 在 environment 里补齐 CLS 上报环境，返回是否有改动。已声明 MOOX_CLS_ENABLED 的环境（运维手工指定）原样保留。
// 开通失败只记日志并返回 false。
func (p *Provisioner) Apply(ctx context.Context, accountID string, credential Credential, environment map[string]string) bool {
	if p == nil || !p.cfg.Enabled || environment == nil {
		return false
	}
	if strings.TrimSpace(environment["MOOX_CLS_ENABLED"]) != "" {
		return false
	}
	resolved, err := p.resolve(ctx, accountID, credential)
	if err != nil {
		log.WarnContextf(ctx, "cloudnode cls provision skipped account=%s: %v", accountID, err)
		return false
	}
	writer, err := p.writer(credential)
	if err != nil {
		log.WarnContextf(ctx, "cloudnode cls skipped account=%s: %v", accountID, err)
		return false
	}
	environment["MOOX_CLS_ENABLED"] = "true"
	environment["MOOX_CLS_ENDPOINT"] = p.cfg.IngestEndpoint
	environment["MOOX_CLS_LOGSET_ID"] = resolved.logsetID
	environment["MOOX_CLS_TOPIC_ID"] = resolved.topicID
	environment["MOOX_CLS_SECRET_ID"] = writer.SecretID
	environment["MOOX_CLS_SECRET_KEY"] = writer.SecretKey
	environment["MOOX_CLS_TIMEOUT_MS"] = strconv.Itoa(p.cfg.TimeoutMS)
	return true
}

func (p *Provisioner) entry(accountID string) *accountEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok := p.accounts[accountID]
	if !ok {
		entry = &accountEntry{}
		p.accounts[accountID] = entry
	}
	return entry
}

func (p *Provisioner) resolve(ctx context.Context, accountID string, credential Credential) (settings, error) {
	entry := p.entry(accountID)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.set && p.now().Sub(entry.value.resolvedAt) < cacheTTL {
		if entry.value.topicID == "" {
			return settings{}, fmt.Errorf("cls provisioning failed recently; retrying later")
		}
		return entry.value, nil
	}
	// 开通尊重调用方的取消与截止时间（最长 provisionTimeout）：调用方放弃后不再占着账号锁。
	provisionCtx, cancel := context.WithTimeout(ctx, provisionTimeout)
	defer cancel()
	api, err := p.factory(credential, p.cfg.Region, p.cfg.APIEndpoint)
	if err != nil {
		return p.remember(entry, settings{}, fmt.Errorf("create cls client failed"))
	}
	result, err := tencent.BootstrapCLS(provisionCtx, api, tencent.CLSBootstrapOptions{
		LogsetName: p.cfg.LogsetName, TopicName: p.cfg.TopicName, RetentionDays: p.cfg.RetentionDays, Partitions: p.cfg.Partitions,
		IndexFields: tencent.MarketFetchLogIndexFields,
	})
	if err != nil {
		if ctx.Err() != nil {
			// 调用方取消不算开通失败，不进入退避。
			return settings{}, fmt.Errorf("cls provisioning canceled")
		}
		// SDK 与 HTTP 错误可能带出密钥相关信息：只记录操作，不记录错误文本。
		return p.remember(entry, settings{}, fmt.Errorf("prepare cls resources failed"))
	}
	if result.LogsetCreated || result.TopicCreated || result.IndexFieldsChanged {
		log.InfoContextf(ctx, "cloudnode cls provisioned account=%s logset=%s topic=%s created_logset=%t created_topic=%t index_fields_changed=%t",
			accountID, result.LogsetID, result.TopicID, result.LogsetCreated, result.TopicCreated, result.IndexFieldsChanged)
	}
	return p.remember(entry, settings{logsetID: result.LogsetID, topicID: result.TopicID}, nil)
}

// remember 缓存结果；失败时缓存一个空结果用于退避。
func (p *Provisioner) remember(entry *accountEntry, value settings, err error) (settings, error) {
	value.resolvedAt = p.now()
	if err != nil {
		// 退避：把时间戳提前，使空结果的有效期等于 failureBackoff。
		value.resolvedAt = p.now().Add(failureBackoff - cacheTTL)
	}
	entry.value, entry.set = value, true
	return value, err
}
