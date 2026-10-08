package bootstrap

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/tradeowner"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"gopkg.in/yaml.v3"
)

// EventBusConfig 是 EventBus 的连接与投递配置。
type EventBusConfig struct {
	URLs              []string      `yaml:"urls"`
	CredentialFile    string        `yaml:"credential_file"`
	ConsumerName      string        `yaml:"consumer_name"`
	RelayInterval     time.Duration `yaml:"relay_interval"`
	RelayBatchSize    int           `yaml:"relay_batch_size"`
	ReconnectInterval time.Duration `yaml:"reconnect_interval"`
	ConnectTimeout    time.Duration `yaml:"connect_timeout"`
}

// RPCConfig 是经节点网关访问的依赖服务。Target 为空表示未接线，此时只能管理定义，不能启用实例。
type RPCConfig struct {
	Target     string `yaml:"target"`
	TargetNode string `yaml:"target_node"`
	AppID      string `yaml:"app_id"`
	AppKey     string `yaml:"app_key"`
	// ViewAppKey 是 DataView 的调用密钥；Storage 对 Metadata 与 DataView 使用不同的鉴权密钥。
	ViewAppKey string        `yaml:"view_app_key"`
	Timeout    time.Duration `yaml:"timeout"`
}

// EvaluationConfig 是求值的重试参数。
type EvaluationConfig struct {
	// AttemptBudget 是同一事件对同一实例读输入的尝试次数，超过后记 skipped(infra_retry_exhausted)。
	AttemptBudget int `yaml:"attempt_budget"`
}

// RetentionConfig 是解释明细与回放的保留天数；结果主表、DSL 版本与会话永久保留。
type RetentionConfig struct {
	ResultItemsDays int `yaml:"result_items_days"`
	ReplaysDays     int `yaml:"replays_days"`
}

// ReplayConfig 是回放的读取与账本参数。
type ReplayConfig struct {
	ChunkBars                 int `yaml:"chunk_bars"`
	PageSize                  int `yaml:"page_size"`
	MissingPriceLiquidateBars int `yaml:"missing_price_liquidate_bars"`
}

// Config 是 config/app.yaml 的结构。
type Config struct {
	Database   string            `yaml:"database"`
	Trade      tradeowner.Config `yaml:"trade"`
	InstanceID string            `yaml:"instance_id"`
	EventBus   EventBusConfig    `yaml:"eventbus"`
	Factor     RPCConfig         `yaml:"factor"`
	Storage    RPCConfig         `yaml:"storage"`
	Evaluation EvaluationConfig  `yaml:"evaluation"`
	Retention  RetentionConfig   `yaml:"retention"`
	Replay     ReplayConfig      `yaml:"replay"`
}

// DependenciesConfigured 报告 Factor 与 Storage 是否都已接线。
func (c Config) DependenciesConfigured() bool {
	return strings.TrimSpace(c.Factor.Target) != "" && strings.TrimSpace(c.Storage.Target) != ""
}

// Load 读取配置、套用环境变量覆盖与默认值并校验。
func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("解析策略配置失败：%w", err)
	}
	if strings.TrimSpace(c.InstanceID) == "" {
		c.InstanceID = "strategy-1"
	}
	// Trade 可能在另一个节点上，使用专用的环境变量，不复用 Factor/Storage 的本机网关覆盖。
	if value := strings.TrimSpace(os.Getenv("MOOX_TRADE_GATEWAY_URL")); value != "" {
		c.Trade.GatewayURL = value
	}
	if value := strings.TrimSpace(os.Getenv("MOOX_TRADE_GATEWAY_NODE_ID")); value != "" {
		c.Trade.TargetNode = value
	}
	c.Trade.GatewayURL = strings.TrimSpace(c.Trade.GatewayURL)
	c.Trade.TargetNode = strings.TrimSpace(c.Trade.TargetNode)
	if c.Trade.CAFile == "" {
		c.Trade.CAFile = strings.TrimSpace(os.Getenv("MOOX_TRADE_GATEWAY_CA_FILE"))
		if c.Trade.CAFile == "" {
			c.Trade.CAFile = strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_FILE"))
		}
	}
	if c.Trade.Timeout == 0 {
		c.Trade.Timeout = tradeowner.DefaultTimeout
	}
	if err := c.Trade.Validate(); err != nil {
		return Config{}, err
	}
	if len(c.EventBus.URLs) == 0 {
		c.EventBus.URLs = []string{"nats://127.0.0.1:4222"}
	}
	if c.EventBus.RelayInterval == 0 {
		c.EventBus.RelayInterval = time.Second
	}
	if c.EventBus.RelayBatchSize == 0 {
		c.EventBus.RelayBatchSize = 100
	}
	if c.EventBus.ReconnectInterval == 0 {
		c.EventBus.ReconnectInterval = time.Second
	}
	if c.EventBus.ConnectTimeout == 0 {
		c.EventBus.ConnectTimeout = 3 * time.Second
	}
	if c.EventBus.ConsumerName == "" {
		c.EventBus.ConsumerName = trigger.ViewDataReadyConsumerName
	}
	if c.Factor.AppID == "" {
		c.Factor.AppID = "strategy"
	}
	if c.Storage.AppID == "" {
		c.Storage.AppID = "strategy"
	}
	// Storage 按共享密钥校验调用方的 AppKey；密钥不进仓库，由部署注入环境变量后派生。
	if c.Storage.AppKey == "" {
		if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
			c.Storage.AppKey = serviceAuthKey(secret, c.Storage.AppID)
		}
	}
	if c.Storage.ViewAppKey == "" {
		if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")); secret != "" {
			c.Storage.ViewAppKey = serviceAuthKey(secret, c.Storage.AppID)
		}
	}
	if strings.TrimSpace(c.Storage.Target) != "" {
		if strings.TrimSpace(c.Storage.AppKey) == "" {
			return Config{}, errors.New("配置了 storage target 时必须提供 app_key（或设置 MOOX_STORAGE_PRIMARY_AUTH_SECRET）")
		}
		if strings.TrimSpace(c.Storage.ViewAppKey) == "" {
			return Config{}, errors.New("配置了 storage target 时必须提供 view_app_key（或设置 MOOX_STORAGE_VIEW_AUTH_SECRET）")
		}
	}
	if c.Factor.Timeout == 0 {
		c.Factor.Timeout = 5 * time.Second
	}
	if c.Storage.Timeout == 0 {
		c.Storage.Timeout = 5 * time.Second
	}
	if c.Evaluation.AttemptBudget == 0 {
		c.Evaluation.AttemptBudget = trigger.DefaultAttemptBudget
	}
	if c.Retention.ResultItemsDays == 0 {
		c.Retention.ResultItemsDays = 90
	}
	if c.Retention.ReplaysDays == 0 {
		c.Retention.ReplaysDays = 90
	}
	if c.Replay.ChunkBars == 0 {
		c.Replay.ChunkBars = 50
	}
	if c.Replay.PageSize == 0 {
		c.Replay.PageSize = 2000
	}
	if c.Replay.MissingPriceLiquidateBars == 0 {
		c.Replay.MissingPriceLiquidateBars = 3
	}
	for _, rawURL := range c.EventBus.URLs {
		if strings.TrimSpace(rawURL) == "" {
			return Config{}, errors.New("eventbus.urls 不能包含空地址")
		}
	}
	if c.EventBus.RelayInterval <= 0 || c.EventBus.RelayBatchSize <= 0 || c.EventBus.ReconnectInterval <= 0 || c.EventBus.ConnectTimeout <= 0 {
		return Config{}, errors.New("eventbus 的间隔、批量与超时必须大于 0")
	}
	if c.Factor.Timeout <= 0 || c.Storage.Timeout <= 0 {
		return Config{}, errors.New("factor 与 storage 的超时必须大于 0")
	}
	if c.Evaluation.AttemptBudget <= 0 {
		return Config{}, errors.New("evaluation.attempt_budget 必须大于 0")
	}
	if c.Retention.ResultItemsDays <= 0 || c.Retention.ReplaysDays <= 0 {
		return Config{}, errors.New("retention 的保留天数必须大于 0")
	}
	if c.Replay.ChunkBars <= 0 || c.Replay.PageSize <= 0 || c.Replay.MissingPriceLiquidateBars <= 0 {
		return Config{}, errors.New("replay 的 chunk_bars、page_size 与 missing_price_liquidate_bars 必须大于 0")
	}
	return c, nil
}

func serviceAuthKey(secret, appID string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(appID))
	return hex.EncodeToString(h.Sum(nil))
}
