package binance

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
)

// 域名常量
const (
	SpotDomain = "api.binance.com"  // 现货域名
	SwapDomain = "fapi.binance.com" // U本位永续合约域名
)

// API 端点
const (
	SpotKlineEndpoint        = "/api/v3/klines"        // 现货K线
	SpotTradesEndpoint       = "/api/v3/aggTrades"     // 现货聚合成交（支持 fromId）
	SpotExchangeInfoEndpoint = "/api/v3/exchangeInfo"  // 现货交易规则和交易对
	SwapKlineEndpoint        = "/fapi/v1/klines"       // 永续合约K线
	SwapTradesEndpoint       = "/fapi/v1/aggTrades"    // 永续合约聚合成交（支持 fromId）
	SwapExchangeInfoEndpoint = "/fapi/v1/exchangeInfo" // 永续合约交易规则和交易对
)

// Client 币安客户端
type Client struct {
	*httpclient.HTTPClient
	spotDomain string
	swapDomain string
}

// NewClient 创建币安客户端
func NewClient() *Client {
	return &Client{
		HTTPClient: httpclient.NewHTTPClient(),
		spotDomain: SpotDomain,
		swapDomain: SwapDomain,
	}
}

// SetSpotBaseURL 设置现货 API 基础地址。
func (c *Client) SetSpotBaseURL(rawURL string) error {
	domain, err := domainFromBaseURL(rawURL)
	if err != nil {
		return err
	}
	if domain != "" {
		c.spotDomain = domain
	}
	return nil
}

// SetSwapBaseURL 设置合约 API 基础地址。
func (c *Client) SetSwapBaseURL(rawURL string) error {
	domain, err := domainFromBaseURL(rawURL)
	if err != nil {
		return err
	}
	if domain != "" {
		c.swapDomain = domain
	}
	return nil
}

// SpotDomain 返回现货 API 域名。
func (c *Client) SpotDomain() string {
	if c.spotDomain == "" {
		return SpotDomain
	}
	return c.spotDomain
}

// SwapDomain 返回合约 API 域名。
func (c *Client) SwapDomain() string {
	if c.swapDomain == "" {
		return SwapDomain
	}
	return c.swapDomain
}

func domainFromBaseURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", nil
	}
	if !strings.Contains(rawURL, "://") {
		rawURL = "https://" + rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("解析 Binance API 地址失败: %w", err)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("Binance API 地址缺少 host: %s", rawURL)
	}
	return parsed.Host, nil
}

// GetDirect sends a normal HTTPS request using system DNS.
func (c *Client) GetDirect(ctx context.Context, domain, path string, query url.Values, result interface{}) error {
	return c.HTTPClient.Get(ctx, domain, path, query, result)
}

// GetDirectWithIPs tries the control-plane DNS snapshot before falling back
// to the normal hostname resolver. The URL remains the hostname so TLS SNI
// and the HTTP Host header are never replaced by an address literal.
func (c *Client) GetDirectWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, result interface{}) error {
	return c.HTTPClient.GetWithIPs(ctx, domain, ips, path, query, result)
}

// GetDirectStream is the streaming counterpart of GetDirect.
func (c *Client) GetDirectStream(ctx context.Context, domain, path string, query url.Values, consume func(io.Reader) error) error {
	return c.HTTPClient.GetStream(ctx, domain, path, query, consume)
}

func (c *Client) GetDirectStreamWithIPs(ctx context.Context, domain string, ips []string, path string, query url.Values, consume func(io.Reader) error) error {
	return c.HTTPClient.GetStreamWithIPs(ctx, domain, ips, path, query, consume)
}
