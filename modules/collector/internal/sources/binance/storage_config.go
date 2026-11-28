package binance

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

// APIConfig 是按顺序故障转移的币安官方端点；没有写时沿用客户端内置的默认域名。
type APIConfig = markets.SourceAPIConfig

// InstTypeForMarket converts the scheduler's lower-case market label to the
// Binance API product type used by the existing collectors.
func InstTypeForMarket(market string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(market)) {
	case "spot", "现货":
		return InstTypeSPOT, nil
	case "swap", "futures", "future", "perpetual", "合约", "永续合约":
		return InstTypeSWAP, nil
	default:
		return "", fmt.Errorf("unsupported market type %q", market)
	}
}

// ResolveStreamConfig 读取 WS 推送的地址与限额（crypto/sources/binance.yaml 的 stream 段），没写的字段用内置默认。
func ResolveStreamConfig() (StreamConfig, error) {
	source, err := loadBinanceSourceConfig()
	if err != nil {
		return StreamConfig{}, err
	}
	if source.Stream == nil {
		return StreamConfig{}.withDefaults(), nil
	}
	return StreamConfig(*source.Stream).withDefaults(), nil
}

func ResolveAPIConfig() (APIConfig, error) {
	source, err := loadBinanceSourceConfig()
	if err != nil {
		return APIConfig{}, err
	}
	if source.API == nil {
		return APIConfig{}, nil
	}
	return *source.API, nil
}

func loadBinanceSourceConfig() (markets.SourceFile, error) {
	file, found, err := markets.LoadSourceFile("crypto", "binance")
	if err != nil {
		return markets.SourceFile{}, err
	}
	if !found {
		return markets.SourceFile{}, fmt.Errorf("未找到 crypto/sources/binance.yaml")
	}
	return file, nil
}
