package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

type ledgerAdapter struct {
	execution.ExecutionAdapter
	supported bool
	query     exchange.LedgerQuery
}

func (a *ledgerAdapter) LedgerSupported() bool { return a.supported }
func (a *ledgerAdapter) ListLedger(_ context.Context, query exchange.LedgerQuery) (exchange.LedgerPage, error) {
	a.query = query
	return exchange.LedgerPage{HasMore: true, Entries: []exchange.LedgerEntry{{
		ID: "1:FUNDING_FEE", Time: time.UnixMilli(1_000), Type: exchange.LedgerFundingFee, ExchangeType: "FUNDING_FEE",
		Asset: "USDT", Amount: shared.MustDecimal("-0.3"), ExchangeSymbol: "BTCUSDT",
	}}}, nil
}

type adapterMap map[string]execution.ExecutionAdapter

func (m adapterMap) Adapter(id string) (execution.ExecutionAdapter, error) {
	if adapter, ok := m[id]; ok {
		return adapter, nil
	}
	return nil, errors.New("no session")
}

func ledgerServer(t *testing.T, adapters adapterMap) *ConsoleServer {
	t.Helper()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tradeStore.Close() })
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", Name: "paper", Exchange: "BINANCE", Markets: []string{"SWAP"},
			ExecutionMode: "PAPER", Environment: "PAPER", SettlementAsset: "USDT", MarginMode: "CROSS", Status: "ENABLED",
		})
	}); err != nil {
		t.Fatal(err)
	}
	return &ConsoleServer{Store: tradeStore, Adapters: adapters}
}

func TestListAccountLedgerDefaultsAndMapping(t *testing.T) {
	adapter := &ledgerAdapter{supported: true}
	server := ledgerServer(t, adapterMap{"account-1": adapter})
	rsp, err := server.ListAccountLedger(spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.ListAccountLedgerReq{TradingAccountId: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || !rsp.GetHasMore() || len(rsp.GetEntries()) != 1 {
		t.Fatalf("rsp = %+v", rsp)
	}
	entry := rsp.GetEntries()[0]
	if entry.GetType() != "FUNDING_FEE" || entry.GetAmount() != "-0.3" || entry.GetTime() != 1_000 {
		t.Fatalf("entry = %+v", entry)
	}
	if adapter.query.Limit != defaultLedgerLimit || adapter.query.End.Sub(adapter.query.Start) != defaultLedgerWindow {
		t.Fatalf("query = %+v", adapter.query)
	}
}

func TestListAccountLedgerUnsupportedAndUnavailable(t *testing.T) {
	for name, adapters := range map[string]adapterMap{
		"unsupported": {"account-1": &ledgerAdapter{supported: false}},
		"no session":  {},
	} {
		server := ledgerServer(t, adapters)
		rsp, _ := server.ListAccountLedger(spacecontext.WithSpaceID(context.Background(), "space-1"),
			&tradepb.ListAccountLedgerReq{TradingAccountId: "account-1"})
		if rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_INVALID_PARAM {
			t.Fatalf("%s: rsp = %+v", name, rsp)
		}
	}
	// 其他空间的账户查不到。
	server := ledgerServer(t, adapterMap{"account-1": &ledgerAdapter{supported: true}})
	rsp, _ := server.ListAccountLedger(spacecontext.WithSpaceID(context.Background(), "space-2"),
		&tradepb.ListAccountLedgerReq{TradingAccountId: "account-1"})
	if rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_NOT_FOUND {
		t.Fatalf("other space rsp = %+v", rsp)
	}
}

func TestLedgerCapabilityOnlyFalseWhenExchangeHasNoLedger(t *testing.T) {
	if ledgerCapability(nil) != true || ledgerCapability(errLedgerUnavailable) != true || ledgerCapability(errServiceUnavailable) != true {
		t.Fatal("没有错误、会话未就绪、服务不可用都不算不支持流水")
	}
	if ledgerCapability(errLedgerUnsupported) {
		t.Fatal("交易所没有流水接口时应该是不支持")
	}
}

func TestGetExecutionCapabilitiesLedgerFlag(t *testing.T) {
	for name, tc := range map[string]struct {
		adapters adapterMap
		want     bool
	}{
		"支持":          {adapterMap{"account-1": &ledgerAdapter{supported: true}}, true},
		"交易所没有流水接口":   {adapterMap{"account-1": &ledgerAdapter{supported: false}}, false},
		"会话还没建立不算不支持": {adapterMap{}, true},
	} {
		server := ledgerServer(t, tc.adapters)
		registry := execution.NewRegistry()
		registry.Register(execution.ExchangeDescriptor{Exchange: exchange.ExchangeBinance, OrderTypes: testDescriptor.OrderTypes}, func(exchange.AccountConfig, exchange.Credential) (execution.ExecutionAdapter, error) {
			return nil, nil
		})
		server.Exchanges = registry
		rsp, err := server.GetExecutionCapabilities(spacecontext.WithSpaceID(context.Background(), "space-1"),
			&tradepb.GetExecutionCapabilitiesReq{TradingAccountId: "account-1"})
		if err != nil {
			t.Fatal(err)
		}
		if got := rsp.GetCapabilities().GetLedger(); got != tc.want {
			t.Fatalf("%s: ledger = %v, want %v", name, got, tc.want)
		}
	}
}
