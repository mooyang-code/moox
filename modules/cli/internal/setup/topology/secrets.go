package topology

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

// Secrets 是 moox.secrets.toml 的内容：部署阶段所需的初始秘密（管理员、云账号与 CLS 写入密钥）。
// setup init 只把腾讯云密钥写入管理台（固定 ID tencent-default，只建不改，之后以管理台为准）；
// CLS 写入密钥不进管理台，每次部署从这里读出来渲染进各组件的环境文件。
// 交易所密钥与告警通知渠道不在这里配置，在安装完成后到管理台页面录入。
type Secrets struct {
	Admin        Admin          `toml:"admin"`
	TencentCloud TencentCloud   `toml:"tencent_cloud"`
	Logging      SecretsLogging `toml:"logging"`
}

// Admin 是 [admin]：管理台的管理员账号。
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

// SecretsLogging 是秘密文件里的 [logging]，只有 CLS 的写入密钥。
type SecretsLogging struct {
	CLS CLSWriter `toml:"cls"`
}

// CLSWriter 是只有服务日志主题上传权限的子账号密钥：它会下发到每台主机，不要填云账号的管理密钥。
type CLSWriter struct {
	WriterSecretID  string `toml:"writer_secret_id"`
	WriterSecretKey string `toml:"writer_secret_key"`
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
		if key[0] == "notification" {
			return Secrets{}, errors.New("config_invalid: [notification] 已从秘密文件移除：告警渠道请在安装完成后到管理台「资源与运维 › 监控告警」里配置")
		}
		if key[0] == "exchange_keys" {
			return Secrets{}, errors.New("config_invalid: [[exchange_keys]] 已从秘密文件移除：交易所密钥请在安装完成后到管理台「系统设置 › 密钥管理」里配置")
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
	s.Logging.CLS.WriterSecretID = strings.TrimSpace(s.Logging.CLS.WriterSecretID)
	s.Logging.CLS.WriterSecretKey = strings.TrimSpace(s.Logging.CLS.WriterSecretKey)
	return nil
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
	s.Logging.CLS = CLSWriter{}
}
