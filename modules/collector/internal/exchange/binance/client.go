package binance

import (
	"context"
	"net/url"

	"github.com/mooyang-code/moox/modules/collector/pkg/httpclient"
)

// 域名常量
const (
	SpotDomain = "api.binance.com"  // 现货域名
	SwapDomain = "fapi.binance.com" // U本位永续合约域名
)

// API 端点
const (
	SpotKlineEndpoint       = "/api/v3/klines"        // 现货K线
	SpotExchangeInfoEndpoint = "/api/v3/exchangeInfo" // 现货交易规则和交易对
	SwapKlineEndpoint       = "/fapi/v1/klines"       // 永续合约K线
	SwapExchangeInfoEndpoint = "/fapi/v1/exchangeInfo" // 永续合约交易规则和交易对
)

// Client 币安客户端
type Client struct {
	*httpclient.HTTPClient
}

// NewClient 创建币安客户端
func NewClient() *Client {
	return &Client{
		HTTPClient: httpclient.NewHTTPClient(),
	}
}

// GetWithIP 发送 GET 请求（使用指定的 IP）
// 这是对 HTTPClient.GetWithIP 的简单封装，方便在重试逻辑中使用
func (c *Client) GetWithIP(ctx context.Context, domain, path string, query url.Values, result interface{}, specifiedIP string) error {
	return c.HTTPClient.GetWithIP(ctx, domain, path, query, result, specifiedIP)
}
