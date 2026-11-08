// Package adaptertest 给测试替身补齐 ExecutionAdapter 里用例不关心的方法。
package adaptertest

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

var errUnsupported = errors.New("adaptertest: not implemented")

// Stub 被测试替身内嵌，替身自己实现的方法优先。
type Stub struct{}

func (Stub) LoadInstruments(context.Context) ([]exchange.Instrument, error) { return nil, nil }

func (Stub) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return execution.MarketQuote{}, errUnsupported
}

func (Stub) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{}, errUnsupported
}

func (Stub) GetOrderByExchangeID(context.Context, exchange.SymbolKey, string) (exchange.Order, error) {
	return exchange.Order{}, &exchange.Error{Kind: exchange.ErrorOrderNotFound, Err: errUnsupported}
}
