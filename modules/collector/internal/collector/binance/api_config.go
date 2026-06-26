package binance

import (
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/exchange/binance"
	"trpc.group/trpc-go/trpc-go/log"
)

func newConfiguredClient() *binanceapi.Client {
	client := binanceapi.NewClient()

	cfg, err := ResolveAPIConfig()
	if err != nil {
		log.Warnf("[Binance] 加载 API 配置失败，使用默认域名: %v", err)
		return client
	}
	if err := client.SetSpotBaseURL(cfg.SpotBaseURL); err != nil {
		log.Warnf("[Binance] 现货 API 地址无效，使用默认域名: %v", err)
	}
	if err := client.SetSwapBaseURL(cfg.SwapBaseURL); err != nil {
		log.Warnf("[Binance] 合约 API 地址无效，使用默认域名: %v", err)
	}
	return client
}
