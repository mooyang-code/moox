package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

func getRecentTrades(ctx context.Context, client *Client, domain, endpoint, label string, supportsFromID bool, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
	return getRecentTradesWithIPs(ctx, client, domain, nil, endpoint, label, supportsFromID, req)
}

func getRecentTradesWithIPs(ctx context.Context, client *Client, domain string, ips []string, endpoint, label string, supportsFromID bool, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
	if client == nil || req == nil || req.Symbol == "" {
		return nil, fmt.Errorf("%s trade request is invalid", label)
	}
	params := url.Values{"symbol": []string{FormatSymbol(req.Symbol)}}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	params.Set("limit", strconv.Itoa(limit))
	// Both public aggregate-trade endpoints support fromId, which keeps the
	// polling cursor contiguous without requiring an authenticated history API.
	if supportsFromID && req.FromID > 0 {
		params.Set("fromId", strconv.FormatInt(req.FromID, 10))
	}

	var raw []AggregateTrade
	err := retryBinance(ctx, func() error {
		return client.GetDirectWithIPs(ctx, domain, ips, endpoint, params, &raw)
	})
	if err != nil {
		return nil, fmt.Errorf("获取%s最近成交失败: %w", label, err)
	}
	out := make([]*exchange.Trade, 0, len(raw))
	for _, item := range raw {
		trade, err := item.ToTrade()
		if err != nil {
			return nil, fmt.Errorf("转换%s成交数据失败: %w", label, err)
		}
		out = append(out, trade)
	}
	return out, nil
}
