package binance

import (
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"trpc.group/trpc-go/trpc-go/log"
)

// newConfiguredClient 按 API 配置创建币安客户端；httpClient 不为空时用它发请求（例如经出口代理的客户端）。
func newConfiguredClient(httpClient *httpclient.HTTPClient) *binanceapi.Client {
	client := binanceapi.NewClient()
	if httpClient != nil {
		client.HTTPClient = httpClient
	}

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
