// Package config loads the shared deployment/runtime contract and private
// identity. There is no fallback to legacy HTTP or static verifier tables.
package config

import (
	"github.com/mooyang-code/moox/modules/hostgateway/internal/tlsconfig"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostgatewayconfig"
)

type Config = hostgatewayconfig.Config

func Load(path string) (Config, error) { return hostgatewayconfig.Load(path) }

func LoadIdentity(cfg Config) (*tlsconfig.Material, gatewayauth.Credentials, error) {
	if err := cfg.Validate(); err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	material, err := tlsconfig.Load(cfg.Host.ID, cfg.TLS)
	if err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	secret, err := gatewayauth.ReadSigningSecret(cfg.Control.KeyFile)
	if err != nil {
		return nil, gatewayauth.Credentials{}, err
	}
	return material, gatewayauth.Credentials{Caller: cfg.Control.Caller, KeyID: cfg.Control.KeyID, Secret: secret}, nil
}
