package topology

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/packages/notification"
)

// Secrets 是 moox.secrets.toml 的内容：秘密的初始值。setup init 把管理员、云密钥、交易所密钥和告警渠道写进管理台一次，
// 只建不改，之后以管理台为准，管理台里后来改的、新加的不会回到这个文件。
// CLS 写入密钥例外：它是部署参数，不进管理台，每次部署从这里读出来渲染进各组件的环境文件。
type Secrets struct {
	Admin        Admin          `toml:"admin"`
	TencentCloud TencentCloud   `toml:"tencent_cloud"`
	Notification Notification   `toml:"notification"`
	Logging      SecretsLogging `toml:"logging"`
	ExchangeKeys []ExchangeKey  `toml:"exchange_keys"`
}

// Admin 是 [admin]：管理台的管理员账号，设定后不能修改。
type Admin struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// Validate 规整并检查管理员账号。
func (a *Admin) Validate() error {
	a.Username = strings.TrimSpace(a.Username)
	if a.Username == "" {
		return errors.New("config_invalid: admin.username is required")
	}
	if a.Password == "" {
		return errors.New("config_invalid: admin.password is required")
	}
	if len([]byte(a.Password)) > 72 {
		return errors.New("config_invalid: admin.password must not exceed 72 bytes")
	}
	return nil
}

// TencentCloud 是 [tencent_cloud]：腾讯云账号的管理密钥。默认地域取控制机的 region（Config.CloudRegion）。
type TencentCloud struct {
	SecretID  string `toml:"secret_id"`
	SecretKey string `toml:"secret_key"`
}

// Notification 是 [notification]：告警渠道的初始值。Monitor 没有任何渠道时由 setup init 写入一次，之后在管理台改。
type Notification struct {
	ChannelType string `toml:"channel_type"`
	WebhookURL  string `toml:"webhook_url"`
	// Secret 选填：飞书机器人的签名校验密钥、钉钉机器人的加签密钥，或通用 Webhook 的签名密钥。
	Secret string `toml:"secret"`
}

// Validate 规整并检查告警渠道；webhook_url 可以留空（部署后在管理台添加）。
func (n *Notification) Validate() error {
	n.ChannelType = strings.TrimSpace(n.ChannelType)
	if n.ChannelType == "" {
		n.ChannelType = "wecom"
	}
	if !notification.ValidChannelType(n.ChannelType) {
		return errors.New("config_invalid: notification.channel_type must be wecom, feishu, dingtalk or webhook")
	}
	n.WebhookURL = strings.TrimSpace(n.WebhookURL)
	n.Secret = strings.TrimSpace(n.Secret)
	channel := notification.ChannelConfig{Type: notification.ChannelType(n.ChannelType), WebhookURL: n.WebhookURL, Secret: n.Secret}
	if err := channel.Validate(); err != nil {
		return fmt.Errorf("config_invalid: notification.webhook_url 必须是%s的 HTTPS 地址（通用 Webhook 不限域名）；notification.secret 企业微信不能填: %v", notification.ChannelLabel(n.ChannelType), err)
	}
	return nil
}

// SecretsLogging 是秘密文件里的 [logging]，只有 CLS 的写入密钥。
type SecretsLogging struct {
	CLS CLSWriter `toml:"cls"`
}

// CLSWriter 是只有服务日志主题上传权限的子账号密钥：它会下发到每台主机，不要填云账号的管理密钥。
type CLSWriter struct {
	WriterSecretID  string `toml:"writer_secret_id"`
	WriterSecretKey string `toml:"writer_secret_key"`
}

// ExchangeKey 是一条 [[exchange_keys]]：交易所 API 密钥。setup init 把它写进密钥管理（只建不改，见 ID）。
// 字段名必须是 api_key、secret_key、passphrase：config get 按这些名字自动打码。
type ExchangeKey struct {
	// Name 只允许 [a-z0-9-]，是固定 ID 的最后一段。
	Name string `toml:"name"`
	// Exchange 是小写交易所名：binance、okx。
	Exchange string `toml:"exchange"`
	// Environment 是 production 或 testnet。
	Environment string `toml:"environment"`
	APIKey      string `toml:"api_key"`
	SecretKey   string `toml:"secret_key"`
	// Passphrase 只有 OKX 用。
	Passphrase string `toml:"passphrase"`
	// ReadOnly 为 true 表示只读密钥（例如 Collector 检测币安现货下架用的那把）：不能用来建执行账户。
	ReadOnly bool `toml:"readonly"`
}

// ID 是密钥在密钥管理里的固定 ID：exchange-<交易所>-<环境>-<名称>，三段一律小写，与 Admin 的规则一致。
func (k ExchangeKey) ID() string {
	return strings.ToLower("exchange-" + k.Exchange + "-" + k.Environment + "-" + k.Name)
}

var exchangeKeyNamePattern = regexp.MustCompile(`^[a-z0-9-]{1,40}$`)

// ValidateExchangeKeys 规整并校验 [[exchange_keys]]：交易所、环境小写化；名称只允许 [a-z0-9-]（不替用户改大小写）；
// 固定 ID 不能重复；OKX 必须有 passphrase（Trade 遇到缺 passphrase 的 OKX 密钥会拒绝启动）。
func ValidateExchangeKeys(keys []ExchangeKey) error {
	seen := map[string]int{}
	for i := range keys {
		key := &keys[i]
		key.Name = strings.TrimSpace(key.Name)
		key.Exchange = strings.ToLower(strings.TrimSpace(key.Exchange))
		key.Environment = strings.ToLower(strings.TrimSpace(key.Environment))
		key.APIKey = strings.TrimSpace(key.APIKey)
		where := fmt.Sprintf("exchange_keys[%d]", i)
		if !exchangeKeyNamePattern.MatchString(key.Name) {
			return fmt.Errorf("config_invalid: %s.name 只允许小写字母、数字和连字符（1～40 个字符）", where)
		}
		if key.Exchange != "binance" && key.Exchange != "okx" {
			return fmt.Errorf("config_invalid: %s.exchange 只能是 binance 或 okx", where)
		}
		if key.Environment != "production" && key.Environment != "testnet" {
			return fmt.Errorf("config_invalid: %s.environment 只能是 production 或 testnet", where)
		}
		if key.APIKey == "" || key.SecretKey == "" {
			return fmt.Errorf("config_invalid: %s 的 api_key 与 secret_key 都必须填写", where)
		}
		if key.Exchange == "okx" && key.Passphrase == "" {
			return fmt.Errorf("config_invalid: %s 是 OKX 密钥，必须填写 passphrase", where)
		}
		for _, field := range []struct{ name, value string }{{"api_key", key.APIKey}, {"secret_key", key.SecretKey}, {"passphrase", key.Passphrase}} {
			if len(field.value) > 512 || strings.ContainsAny(field.value, "\x00\r\n") {
				return fmt.Errorf("config_invalid: %s.%s 含有换行或超过 512 字节", where, field.name)
			}
		}
		if previous, dup := seen[key.ID()]; dup {
			return fmt.Errorf("config_invalid: %s 与 exchange_keys[%d] 重复：同一交易所、环境下的 name 不能相同（固定 ID 是 %s）", where, previous, key.ID())
		}
		seen[key.ID()] = i
	}
	return nil
}

// LoadSecrets 读取 moox.toml 同目录的 moox.secrets.toml：要求 0600、属于当前用户；加密保存的用本机密钥解开。
func LoadSecrets(configPath string) (*Secrets, error) {
	path := SecretsPathFor(configPath)
	raw, err := readRegularFile(path, true)
	if err != nil {
		return nil, err
	}
	plain := raw
	if configcrypt.IsEncrypted(raw) {
		if plain, err = configcrypt.Decrypt(raw, configcrypt.Default()); err != nil {
			return nil, fmt.Errorf("config_invalid: 解不开 %s：%w", SecretsFileName, err)
		}
	}
	secrets, err := ParseSecrets(plain)
	clear(plain)
	if err != nil {
		return nil, err
	}
	return &secrets, nil
}

// ParseSecrets 严格解码并校验秘密文件的明文。错误信息只有键名和行号，不含任何值。
func ParseSecrets(plain []byte) (Secrets, error) {
	if len(plain) > maxFileSize {
		return Secrets{}, errors.New("config_invalid: moox.secrets.toml 超过 1 MiB")
	}
	var secrets Secrets
	md, err := toml.NewDecoder(bytes.NewReader(plain)).Decode(&secrets)
	if err != nil {
		return Secrets{}, decodeError(SecretsFileName, err)
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		key := keys[0]
		if len(key) == 2 && key[0] == "tencent_cloud" && key[1] == "region" {
			return Secrets{}, errors.New("config_invalid: tencent_cloud.region 已删除：腾讯云的默认地域取 moox.toml 里控制机的 region")
		}
		return Secrets{}, fmt.Errorf("config_invalid: %s 里有不认识的键 %s（拓扑与部署参数写在 moox.toml 里）", SecretsFileName, key.String())
	}
	if err := secrets.validate(); err != nil {
		return Secrets{}, err
	}
	return secrets, nil
}

func (s *Secrets) validate() error {
	if err := s.Admin.Validate(); err != nil {
		return err
	}
	s.TencentCloud.SecretID = strings.TrimSpace(s.TencentCloud.SecretID)
	if s.TencentCloud.SecretID == "" {
		return errors.New("config_invalid: tencent_cloud.secret_id is required")
	}
	if s.TencentCloud.SecretKey == "" {
		return errors.New("config_invalid: tencent_cloud.secret_key is required")
	}
	if err := s.Notification.Validate(); err != nil {
		return err
	}
	s.Logging.CLS.WriterSecretID = strings.TrimSpace(s.Logging.CLS.WriterSecretID)
	s.Logging.CLS.WriterSecretKey = strings.TrimSpace(s.Logging.CLS.WriterSecretKey)
	return ValidateExchangeKeys(s.ExchangeKeys)
}

// CheckFor 核对两份文件之间的关联：moox.toml 打开了 CLS 上报时，秘密文件里要有写入密钥。
func (s *Secrets) CheckFor(cfg *Config) error {
	if cfg == nil || !cfg.Logging.CLS.Enabled {
		return nil
	}
	if s.Logging.CLS.WriterSecretID == "" || s.Logging.CLS.WriterSecretKey == "" {
		return errors.New("config_invalid: moox.toml 打开了 logging.cls.enabled，moox.secrets.toml 的 [logging.cls] 要填 writer_secret_id 与 writer_secret_key")
	}
	return nil
}

// Clear 清掉内存里的秘密值，用完就调。
func (s *Secrets) Clear() {
	if s == nil {
		return
	}
	s.Admin.Password = ""
	s.TencentCloud = TencentCloud{}
	s.Notification.WebhookURL, s.Notification.Secret = "", ""
	s.Logging.CLS = CLSWriter{}
	for i := range s.ExchangeKeys {
		s.ExchangeKeys[i].APIKey, s.ExchangeKeys[i].SecretKey, s.ExchangeKeys[i].Passphrase = "", "", ""
	}
}
