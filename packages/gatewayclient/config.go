package gatewayclient

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// Mode 是 gatewayclient 的使用方式（设计文档 3.6）。
type Mode string

const (
	// ModeLocal 供 MooX 主机上的组件使用：从本机主机网关取服务目录，本机目标走 127.0.0.1:11002，
	// 其他主机走 <主机>:11003（TLS，校验 MooX 私有 CA）。
	ModeLocal Mode = "local"
	// ModeAccess 供外部调用方使用：所有请求发往固定的外部接入，明文 + 签名，没有服务目录。
	ModeAccess Mode = "access"
	// ModeTunnel 供 moox-cli 使用：经 SSH 隧道从 control 取服务目录，再经隧道连目标主机的 127.0.0.1:11002。
	ModeTunnel Mode = "tunnel"
)

const (
	// DefaultLocalAddress 是本机主机网关的本机入口。
	DefaultLocalAddress = "127.0.0.1:11002"
	// RemotePort 是主机网关的跨主机入口端口。
	RemotePort = "11003"
)

// 外部调用方（SCF）的环境变量，由 Collector 经 CloudNode 写入函数配置。
const (
	EnvAccessAddress = "MOOX_ACCESS_ADDRESS"
	EnvAccessID      = "MOOX_ACCESS_ID"
	EnvCaller        = "MOOX_CALLER"
	EnvCallerKey     = "MOOX_CALLER_KEY"
)

// Config 是各模块 config/app.yaml 中的 gateway_client 段。
type Config struct {
	Mode   Mode   `yaml:"mode"`
	Caller string `yaml:"caller"`
	// KeyFile 是调用方签名密钥文件（JSON，0600）。
	KeyFile string `yaml:"key_file"`
	// CAFile 是 MooX 私有 CA 证书，内部方式跨主机调用时校验主机网关。
	CAFile string `yaml:"ca_file"`
	// CacheDir 是服务目录的落盘缓存目录，内部方式使用。
	CacheDir string `yaml:"cache_dir"`
	// LocalAddress 是本机主机网关的本机入口，默认 127.0.0.1:11002。
	LocalAddress string `yaml:"local_address"`
	// AccessAddress、AccessID 是外部方式固定使用的外部接入地址和实例 ID（access@<主机>）。
	AccessAddress string `yaml:"access_address"`
	AccessID      string `yaml:"access_id"`
}

// Validate 按使用方式检查必填项。
func (c Config) Validate() error {
	if strings.TrimSpace(c.Caller) == "" {
		return errors.New("gateway_client.caller 不能为空")
	}
	switch c.Mode {
	case ModeLocal:
		if strings.TrimSpace(c.CAFile) == "" {
			return errors.New("内部方式必须配置 gateway_client.ca_file")
		}
		if strings.TrimSpace(c.CacheDir) == "" {
			return errors.New("内部方式必须配置 gateway_client.cache_dir")
		}
		if c.LocalAddress != "" {
			if err := validateLoopbackAddress(c.LocalAddress); err != nil {
				return fmt.Errorf("gateway_client.local_address: %w", err)
			}
		}
		if c.AccessAddress != "" || c.AccessID != "" {
			return errors.New("内部方式不能配置 access_address、access_id")
		}
	case ModeAccess:
		if _, _, err := net.SplitHostPort(strings.TrimSpace(c.AccessAddress)); err != nil {
			return fmt.Errorf("外部方式必须配置合法的 gateway_client.access_address: %w", err)
		}
		if err := ValidateAccessID(c.AccessID); err != nil {
			return err
		}
	case ModeTunnel:
		if c.AccessAddress != "" || c.AccessID != "" {
			return errors.New("隧道方式不能配置 access_address、access_id")
		}
	default:
		return fmt.Errorf("gateway_client.mode %q 无效，可选 local、access、tunnel", c.Mode)
	}
	return nil
}

// ValidateAccessID 检查外部接入实例 ID 的格式：access@<主机 ID>。
func ValidateAccessID(id string) error {
	host, ok := strings.CutPrefix(strings.TrimSpace(id), "access@")
	if !ok || host == "" || strings.ContainsAny(host, "@ \t") {
		return fmt.Errorf("外部接入实例 ID %q 必须是 access@<主机 ID>", id)
	}
	return nil
}

// AccessConfigFromEnv 从 SCF 环境变量组装外部方式的配置和签名凭据。
func AccessConfigFromEnv() (Config, gatewayauth.Credentials, error) {
	config := Config{
		Mode:          ModeAccess,
		Caller:        strings.TrimSpace(os.Getenv(EnvCaller)),
		AccessAddress: strings.TrimSpace(os.Getenv(EnvAccessAddress)),
		AccessID:      strings.TrimSpace(os.Getenv(EnvAccessID)),
	}
	if err := config.Validate(); err != nil {
		return Config{}, gatewayauth.Credentials{}, fmt.Errorf("外部接入环境变量不完整: %w", err)
	}
	credentials, err := gatewayauth.ParseCallerKeyValue(config.Caller, os.Getenv(EnvCallerKey))
	if err != nil {
		return Config{}, gatewayauth.Credentials{}, fmt.Errorf("%s: %w", EnvCallerKey, err)
	}
	return config, credentials, nil
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if port == "" {
		return errors.New("缺少端口")
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%q 不是本机回环地址", host)
	}
	return nil
}
