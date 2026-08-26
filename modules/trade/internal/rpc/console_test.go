package rpc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
)

func TestConsoleQueryEquityCurveReadsOnlyRequestedSpace(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tradeStore.Close()
	account := store.TradingAccountRecord{
		SpaceID: "space-1", TradingAccountID: "account-1", Name: "paper",
		Exchange: string(exchange.ExchangeBinance), MarketType: string(exchange.MarketTypeSpot),
		ExecutionMode: string(exchange.ExecutionModePaper), SettlementAsset: "USDT",
		Status: string(exchange.AccountStatusEnabled), PaperConfig: &store.PaperAccountConfigRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", InitialBalance: "1000",
			MakerFeeRate: "0", TakerFeeRate: "0", SlippageBPS: "0",
		},
	}
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(account); err != nil {
			return err
		}
		return tx.UpsertAccountEquityPoint(store.EquityPointRecord{
			SpaceID: "space-1", TradingAccountID: "account-1", BucketTime: 100,
			Equity: "1000", AvailableFunds: "1000", UsedMargin: "0", SourceTime: 100,
		})
	}); err != nil {
		t.Fatal(err)
	}

	server := &ConsoleServer{Store: tradeStore}
	response, err := server.QueryEquityCurve(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.QueryEquityCurveReq{TradingAccountId: "account-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || len(response.GetPoints()) != 1 || response.GetPoints()[0].GetEquity() != "1000" {
		t.Fatalf("query response = %+v", response)
	}

	other, err := server.QueryEquityCurve(
		spacecontext.WithSpaceID(context.Background(), "space-2"),
		&tradepb.QueryEquityCurveReq{TradingAccountId: "account-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if other.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || len(other.GetPoints()) != 0 {
		t.Fatalf("cross-space query response = %+v", other)
	}
}

func TestConsoleRejectsAmbiguousEquityCurveTarget(t *testing.T) {
	server := &ConsoleServer{Store: &store.Store{}}
	response, err := server.QueryEquityCurve(
		spacecontext.WithSpaceID(context.Background(), "space-1"),
		&tradepb.QueryEquityCurveReq{TradingAccountId: "account-1", LogicalAccountId: "logical-1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.GetRetInfo().GetCode() != tradepb.ErrorCode_INVALID_PARAM {
		t.Fatalf("ret_info = %+v", response.GetRetInfo())
	}
}
