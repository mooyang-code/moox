package bootstrap

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

type EventBusConfig struct {
	URLs              []string      `yaml:"urls"`
	CredentialFile    string        `yaml:"credential_file"`
	ConsumerName      string        `yaml:"consumer_name"`
	RelayInterval     time.Duration `yaml:"relay_interval"`
	RelayBatchSize    int           `yaml:"relay_batch_size"`
	ReconnectInterval time.Duration `yaml:"reconnect_interval"`
	ConnectTimeout    time.Duration `yaml:"connect_timeout"`
}

// StorageConfig carries role authentication independently of gateway signing.
type StorageConfig struct {
	AppID  string `yaml:"app_id"`
	AppKey string `yaml:"app_key"`
	// ViewAppKey authenticates DataView requests. Storage deliberately uses a
	// separate secret for the read/index service from the Primary/Metadata
	// caller secret, so Strategy must carry both identities explicitly.
	ViewAppKey string `yaml:"view_app_key"`
}

type TradeConfig struct {
	GatewayURL string        `yaml:"gateway_url"`
	TargetNode string        `yaml:"target_node"`
	CAFile     string        `yaml:"ca_file"`
	Timeout    time.Duration `yaml:"timeout"`
}

func (c TradeConfig) validate() error {
	if c.Timeout <= 0 {
		return fmt.Errorf("trade timeout must be positive")
	}
	if c.GatewayURL == "" && c.TargetNode == "" {
		return nil
	}
	if c.GatewayURL == "" || c.TargetNode == "" {
		return fmt.Errorf("trade gateway_url and target_node must be configured together")
	}
	// Match the Admin Gateway node registration contract, not merely the
	// broader set of identifiers that can be represented in HMAC headers.
	if strings.ContainsFunc(c.TargetNode, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) {
		return fmt.Errorf("trade target_node must use lowercase letters, digits, dash, or underscore")
	}
	u, err := url.Parse(c.GatewayURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("trade gateway_url must be an origin without credentials, path, query or fragment")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (!strings.EqualFold(u.Hostname(), "localhost") && (ip == nil || !ip.IsLoopback())) {
			return fmt.Errorf("trade gateway_url requires HTTPS except on loopback")
		}
	}
	return nil
}

type Config struct {
	GatewayClient gatewayclient.FileConfig `yaml:"gateway_client"`
	sourcePath    string                   `yaml:"-"`
	Database      string                   `yaml:"database"`
	Trade         TradeConfig              `yaml:"trade"`
	InstanceID    string                   `yaml:"instance_id"`
	EventBus      EventBusConfig           `yaml:"eventbus"`
	Storage       StorageConfig            `yaml:"storage"`
}

func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve strategy config: %w", err)
	}
	c := Config{sourcePath: absolute, GatewayClient: gatewayclient.FileConfig{Caller: "strategy", KeyFile: "../../secrets/caller-strategy.key"}}
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(true)
	if err = decoder.Decode(&c); err != nil {
		return Config{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, fmt.Errorf("strategy config must contain exactly one YAML document")
	}
	if c.GatewayClient.Caller != "strategy" {
		return Config{}, fmt.Errorf("gateway_client.caller must be strategy")
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(c.InstanceID) == "" {
		c.InstanceID = "strategy-1"
	}
	// Trade migrates with its service protocol in D2d.
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
		c.Trade.Timeout = defaultLogicalAccountTimeout
	}
	if err := c.Trade.validate(); err != nil {
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
		c.EventBus.ConsumerName = "strategy_view_data_ready_v1"
	}
	if c.Storage.AppID == "" {
		c.Storage.AppID = "strategy"
	}
	// Storage validates the service AppKey against the shared primary auth
	// secret. Keep the secret out of checked-in YAML while deriving the exact
	// per-caller key when the deployment injects it into the process.
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
	for _, rawURL := range c.EventBus.URLs {
		if strings.TrimSpace(rawURL) == "" {
			return Config{}, fmt.Errorf("strategy eventbus URLs must not be empty")
		}
	}
	if c.EventBus.RelayInterval <= 0 || c.EventBus.RelayBatchSize <= 0 || c.EventBus.ReconnectInterval <= 0 || c.EventBus.ConnectTimeout <= 0 {
		return Config{}, fmt.Errorf("strategy eventbus durations and batch size must be positive")
	}
	return c, nil
}

// OpenGateway requires the deployment identity and both Storage role secrets.
// Offline configuration loading does not open signing keys or the directory.
func (c Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c.sourcePath == "" {
		return nil, fmt.Errorf("strategy gateway client requires a loaded module configuration")
	}
	if c.GatewayClient.Caller != "strategy" {
		return nil, fmt.Errorf("gateway_client.caller must be strategy")
	}
	if strings.TrimSpace(c.Storage.AppKey) == "" {
		return nil, fmt.Errorf("storage app_key is required (set app_key or MOOX_STORAGE_PRIMARY_AUTH_SECRET)")
	}
	if strings.TrimSpace(c.Storage.ViewAppKey) == "" {
		return nil, fmt.Errorf("storage view_app_key is required (set view_app_key or MOOX_STORAGE_VIEW_AUTH_SECRET)")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.Database), onRefreshError)
}

func serviceAuthKey(secret, appID string) string {
	h := hmac.New(sha256.New, []byte(secret))
	_, _ = h.Write([]byte(appID))
	return hex.EncodeToString(h.Sum(nil))
}
