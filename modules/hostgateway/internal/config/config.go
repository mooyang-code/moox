// Package config loads the shared deployment/runtime contract and private
// identity.
package config

import (
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

type Config = hostgatewayconfig.Config

func Load(path string) (Config, error) { return hostgatewayconfig.Load(path) }

// LoadIdentity 读主机证书与主机签名私钥；网关以 host-gateway 的身份、用所在主机的密钥签名。
func LoadIdentity(cfg Config) (*tlsconfig.Material, gatewayauth.Credentials, error) {
	if err := cfg.Validate(); err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	material, err := tlsconfig.Load(cfg.Host.ID, cfg.TLS)
	if err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	key, err := gatewayauth.ReadSigningKey(cfg.Control.KeyFile)
	if err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	return material, gatewayauth.Credentials{Caller: "host-gateway", Host: cfg.Host.ID, Key: key}, nil
}
