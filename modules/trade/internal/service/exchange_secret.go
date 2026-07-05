package service

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// ExchangeSecret 是从 admin 秘钥管理读取到的交易所凭证。
type ExchangeSecret struct {
	SecretID    string
	Name        string
	Description string
	Provider    string
	KeyID       string
	SecretValue string
	ExtraConfig string
}

// ExchangeSecretSource 抽象后台秘钥来源。
type ExchangeSecretSource interface {
	ListExchangeSecrets(ctx context.Context, provider string) ([]ExchangeSecret, error)
}

// SyncExchangeAccountsOptions 控制交易账户导入行为。
type SyncExchangeAccountsOptions struct {
	UserID     string
	Provider   string
	MarketType string
}

type exchangeSecretConfig struct {
	MarketType   string   `json:"market_type"`
	BaseCurrency string   `json:"base_currency"`
	Passphrase   string   `json:"passphrase"`
	Endpoint     string   `json:"endpoint"`
	Permissions  []string `json:"permissions"`
	RateLimit    int      `json:"rate_limit"`
}

func parseExchangeSecretConfig(raw string) exchangeSecretConfig {
	var cfg exchangeSecretConfig
	if strings.TrimSpace(raw) == "" {
		return cfg
	}
	_ = json.Unmarshal([]byte(raw), &cfg)
	if strings.TrimSpace(cfg.MarketType) != "" {
		cfg.MarketType = normalizeMarketType(cfg.MarketType)
	}
	return cfg
}

func secretAccountName(sec ExchangeSecret) string {
	if strings.TrimSpace(sec.Name) != "" {
		return strings.TrimSpace(sec.Name)
	}
	return strings.TrimSpace(sec.Provider) + "-" + sec.SecretID
}

func deterministicID(prefix, raw string) string {
	sum := sha1.Sum([]byte(raw))
	return prefix + "_" + hex.EncodeToString(sum[:])[:16]
}

func accountTypeForMarket(marketType string) AccountType {
	switch normalizeMarketType(marketType) {
	case "spot":
		return AccountSpot
	case "margin":
		return AccountMargin
	case "futures", "swap":
		return AccountSwap
	default:
		return AccountSwap
	}
}

func normalizeMarketType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "spot":
		return "spot"
	case "margin":
		return "margin"
	case "future", "futures", "delivery":
		return "futures"
	case "swap", "perp", "perpetual", "":
		return "swap"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
