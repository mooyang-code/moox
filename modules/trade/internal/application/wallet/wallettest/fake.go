// Package wallettest 是钱包划转的假交易所：余额、划转记录都由测试控制，Transfer 的行为可以脚本化
// （成功、超时但已到账、被拒绝、限频），划转服务的单测和执行器的集成测试共用。
package wallettest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// Call 是一次 Transfer 调用。
type Call struct {
	From, To  exchange.Wallet
	Asset     string
	Amount    shared.Decimal
	ClientRef string
}

// Exchange 是假的普通账户交易所（现货、合约两个钱包）。
type Exchange struct {
	execution.ExecutionAdapter

	mu sync.Mutex
	// Spot、Futures 是两个钱包的 USDT 可用余额；FuturesTransferable 为空时等于 Futures，否则是合约钱包最多能转出的数量。
	Spot, Futures, FuturesTransferable string
	// BNB 是现货钱包里的 BNB 数量和 BNB 价格（USDT）。
	BNB, BNBPrice string
	Ask           string
	// Clock 给划转记录的时间；为空用当前时间。
	Clock func() time.Time
	// TransferErr 非空时 Transfer 返回它；TransferDelivers 为真表示请求报错但交易所其实已经执行了划转。
	TransferErr      error
	TransferDelivers bool
	// QueryErr 非空时 GetAccountSnapshot 失败；ListErr 非空时 ListTransfers 失败。
	QueryErr, ListErr error

	calls   []Call
	queries int
	records []execution.TransferRecord
	nextID  int
}

func (f *Exchange) now() time.Time {
	if f.Clock != nil {
		return f.Clock()
	}
	return time.Now()
}

func dec(value string) shared.Decimal {
	if value == "" {
		return shared.Zero()
	}
	return shared.MustDecimal(value)
}

func (f *Exchange) GetQuote(context.Context, exchange.SymbolKey) (execution.MarketQuote, error) {
	return execution.MarketQuote{Ask: dec(f.Ask), Last: dec(f.Ask)}, nil
}

func (f *Exchange) GetReferencePrice(context.Context, exchange.SymbolKey) (exchange.ReferencePrice, error) {
	return exchange.ReferencePrice{Price: dec(f.BNBPrice), UpdatedAt: f.now()}, nil
}

func (f *Exchange) GetAccountSnapshot(context.Context) (exchange.AccountSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if f.QueryErr != nil {
		return exchange.AccountSnapshot{}, f.QueryErr
	}
	transferable := f.FuturesTransferable
	if transferable == "" {
		transferable = f.Futures
	}
	balances := []exchange.AssetBalance{
		{Wallet: exchange.WalletSpot, Asset: "USDT", Available: dec(f.Spot), Total: dec(f.Spot)},
		{Wallet: exchange.WalletFutures, Asset: "USDT", Available: dec(f.Futures), Total: dec(f.Futures), Transferable: dec(transferable)},
	}
	if f.BNB != "" {
		balances = append(balances, exchange.AssetBalance{Wallet: exchange.WalletSpot, Asset: "BNB", Available: dec(f.BNB), Total: dec(f.BNB)})
	}
	return exchange.AccountSnapshot{Balances: balances}, nil
}

func (f *Exchange) Transfer(_ context.Context, from, to exchange.Wallet, asset string, amount shared.Decimal, clientRef string) (execution.TransferReceipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{from, to, asset, amount, clientRef})
	f.nextID++
	id := fmt.Sprintf("tran-%d", f.nextID)
	if f.TransferErr != nil && !f.TransferDelivers {
		return execution.TransferReceipt{}, f.TransferErr
	}
	f.records = append(f.records, execution.TransferRecord{
		ExchangeTransferID: id, Asset: asset, Amount: amount, From: from, To: to, Status: execution.TransferPending, At: f.now(),
	})
	if f.TransferErr != nil {
		return execution.TransferReceipt{}, f.TransferErr
	}
	return execution.TransferReceipt{ExchangeTransferID: id}, nil
}

func (f *Exchange) ListTransfers(context.Context, time.Time) ([]execution.TransferRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return append([]execution.TransferRecord(nil), f.records...), nil
}

// ConfirmAll 让交易所把所有划转记录置为已到账，并按金额移动余额。
func (f *Exchange) ConfirmAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.records {
		if f.records[i].Status == execution.TransferConfirmed {
			continue
		}
		f.records[i].Status = execution.TransferConfirmed
		spot, futures := dec(f.Spot), dec(f.Futures)
		if f.records[i].To == exchange.WalletSpot {
			spot, futures = spot.Add(f.records[i].Amount), futures.Sub(f.records[i].Amount)
		} else {
			spot, futures = spot.Sub(f.records[i].Amount), futures.Add(f.records[i].Amount)
		}
		f.Spot, f.Futures = spot.String(), futures.String()
		f.FuturesTransferable = ""
	}
}

// Calls 返回目前为止的 Transfer 调用。
func (f *Exchange) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Queries 返回目前为止查余额、查划转记录的调用次数（退避期内不应增加）。
func (f *Exchange) Queries() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.queries
}

// Set 在持锁的情况下修改假交易所的状态。
func (f *Exchange) Set(change func(*Exchange)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// Adapters 按执行账户 ID 给适配器。
type Adapters map[string]execution.ExecutionAdapter

func (a Adapters) Adapter(accountID string) (execution.ExecutionAdapter, error) {
	adapter, ok := a[accountID]
	if !ok {
		return nil, errors.New("没有这个账户的适配器")
	}
	return adapter, nil
}
