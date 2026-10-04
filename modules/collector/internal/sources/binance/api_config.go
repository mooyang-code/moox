package binance

import (
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"trpc.group/trpc-go/trpc-go/log"
)

func newConfiguredClient() *binanceapi.Client {
	client := binanceapi.NewClient()

	cfg, err := ResolveAPIConfig()
	if err != nil {
		log.Warnf("[Binance] 加载 API 配置失败，使用默认域名: %v", err)
		return client
	}
	if len(cfg.SpotBaseURLs) > 0 {
		if err := client.SetSpotBaseURLs(cfg.SpotBaseURLs); err != nil {
			log.Warnf("[Binance] 现货 API 地址无效，使用默认域名: %v", err)
		}
	}
	if len(cfg.SwapBaseURLs) > 0 {
		if err := client.SetSwapBaseURLs(cfg.SwapBaseURLs); err != nil {
			log.Warnf("[Binance] 合约 API 地址无效，使用默认域名: %v", err)
		}
	}
	return client
}
