package main

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// runFactsPhase 只读核对交易所接口：标的、报价、账户、持仓、挂单、成交。不写库，不下单。
func runFactsPhase(ctx context.Context, options smokeOptions, credential exchange.Credential) error {
	config := exchange.AccountConfig{
		TradingAccountID: "facts", Exchange: options.Exchange, Markets: []exchange.MarketType{options.MarketType},
		ExecutionMode: exchange.ExecutionModeLive, AccountMode: exchange.AccountMode(smokeAccountMode(options.Exchange)),
		Environment: options.Environment, SettlementAsset: "USDT",
	}
	if exchange.RuleFor(options.MarketType).Leverage {
		config.MarginMode = exchange.MarginModeCross
	}
	adapter, err := newSmokeRegistry().Bind(config, credential)
	if err != nil {
		return err
	}
	instruments, err := adapter.LoadInstruments(ctx)
	if err != nil {
		return fmt.Errorf("LoadInstruments: %w", err)
	}
	fmt.Printf("标的 %d 个\n", len(instruments))
	symbol := exchange.NewSymbolKey(options.MarketType, options.ExchangeSymbol)
	quote, err := adapter.GetQuote(ctx, symbol)
	if err != nil {
		return fmt.Errorf("GetQuote: %w", err)
	}
	fmt.Printf("报价 %s 买 %s 卖 %s 时间 %s\n", symbol, quote.Bid, quote.Ask, quote.SourceTime.UTC().Format("15:04:05"))
	snapshot, err := adapter.GetAccountSnapshot(ctx)
	if err != nil {
		return fmt.Errorf("GetAccountSnapshot: %w", err)
	}
	fmt.Printf("账户 余额 %d 项\n", len(snapshot.Balances))
	positions, err := adapter.ListPositionSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("ListPositionSnapshots: %w", err)
	}
	fmt.Printf("持仓 %d 个\n", len(positions))
	orders, err := adapter.ListOpenOrders(ctx)
	if err != nil {
		return fmt.Errorf("ListOpenOrders: %w", err)
	}
	fmt.Printf("挂单 %d 个\n", len(orders))
	fills, cursor, err := adapter.ListRecentFills(ctx, symbol, "")
	if err != nil {
		return fmt.Errorf("ListRecentFills: %w", err)
	}
	fmt.Printf("成交 %d 笔，游标 %s\n", len(fills), cursor)
	return nil
}
