package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"trpc.group/trpc-go/trpc-go/log"
)

func getRecentTrades(ctx context.Context, client *Client, domain, endpoint, label string, supportsFromID bool, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
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
	var triedIPs []string
	err := retry.Do(func() error {
		currentIP := httpclient.GetNextAvailableIP(domain, triedIPs)
		if currentIP == "" {
			log.WarnContextf(ctx, "[%s] 无可用DNS优选IP，降级为域名直连, symbol=%s", label, req.Symbol)
		}
		if err := client.GetWithIP(ctx, domain, endpoint, params, &raw, currentIP); err != nil {
			if currentIP != "" {
				triedIPs = append(triedIPs, currentIP)
			}
			return err
		}
		return nil
	}, retry.Attempts(3), retry.Delay(time.Second), retry.LastErrorOnly(true), retry.Context(ctx))
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
