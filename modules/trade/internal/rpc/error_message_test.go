package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	operatorapp "github.com/mooyang-code/moox/modules/trade/internal/application/operator"
	orderapp "github.com/mooyang-code/moox/modules/trade/internal/application/order"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/tradingaccount"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/spacecontext"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/log"
)

// captureLogger 记下 Errorf 写的日志，其余方法沿用原来的默认日志器。
type captureLogger struct {
	log.Logger
	errors []string
}

func (c *captureLogger) Errorf(format string, args ...any) {
	c.errors = append(c.errors, fmt.Sprintf(format, args...))
}

func captureErrorLogs(t *testing.T) *captureLogger {
	t.Helper()
	original := log.GetDefaultLogger()
	capture := &captureLogger{Logger: original}
	log.Register("default", capture)
	t.Cleanup(func() { log.Register("default", original) })
	return capture
}

// 兜底分支：对外只回固定文案，原始错误写进日志。
func TestErrorInfoHidesRawInternalError(t *testing.T) {
	capture := captureErrorLogs(t)
	raw := errors.New("UNIQUE constraint failed: t_orders.c_order_id at /data/trade.db")
	info := errorInfo(raw)
	if info.GetCode() != tradepb.ErrorCode_INNER_ERR || info.GetMsg() != "交易服务内部错误，请查看日志" {
		t.Fatalf("info = %+v", info)
	}
	if len(capture.errors) != 1 || !strings.Contains(capture.errors[0], raw.Error()) {
		t.Fatalf("日志里要有原文：%v", capture.errors)
	}
}

// 交易所错误：回中文类别、HTTP 状态和错误码，不带交易所原文；原文仍写日志。
func TestErrorInfoSummarizesExchangeError(t *testing.T) {
	capture := captureErrorLogs(t)
	raw := &exchange.Error{Kind: exchange.ErrorRateLimited, HTTPStatus: 429, Code: "-1003", Err: errors.New("body: signature=abc123")}
	info := errorInfo(fmt.Errorf("加入组合失败：%w", raw))
	if info.GetCode() != tradepb.ErrorCode_INNER_ERR {
		t.Fatalf("code = %v", info.GetCode())
	}
	for _, want := range []string{"限频", "HTTP 429", "-1003"} {
		if !strings.Contains(info.GetMsg(), want) {
			t.Fatalf("msg = %q，缺少 %q", info.GetMsg(), want)
		}
	}
	if strings.Contains(info.GetMsg(), "abc123") {
		t.Fatalf("不能带交易所原文：%q", info.GetMsg())
	}
	if len(capture.errors) != 1 || !strings.Contains(capture.errors[0], "abc123") {
		t.Fatalf("日志里要有原文：%v", capture.errors)
	}
}

// 文案本来给用户看的错误（含从库里还原的撤单失败）照原样回，不走兜底。
func TestErrorInfoKeepsUserFacingMessages(t *testing.T) {
	capture := captureErrorLogs(t)
	cases := []error{
		fmt.Errorf("%w：交易账户被停用", tradingaccount.ErrAccountNotExecutable),
		fmt.Errorf("%w：BTC-USDT", orderapp.ErrInstrumentHalted),
		fmt.Errorf("%w：委托价 1", orderapp.ErrPriceLimit),
		operatorapp.ErrCancelUnconfirmed,
		operatorapp.RecordedFailure("撤单还没有得到交易所确认：订单 o1"),
	}
	for _, err := range cases {
		if info := errorInfo(err); info.GetCode() != tradepb.ErrorCode_INNER_ERR || info.GetMsg() != err.Error() {
			t.Fatalf("%v：info = %+v", err, info)
		}
	}
	if len(capture.errors) != 0 {
		t.Fatalf("不该写错误日志：%v", capture.errors)
	}
}

// 有明确含义的错误照常回原文：参数错误带具体原因，不走兜底。
func TestErrorInfoKeepsMappedMessages(t *testing.T) {
	capture := captureErrorLogs(t)
	info := errorInfo(fmt.Errorf("%w: 数量太小", store.ErrInvalidRecord))
	if info.GetCode() != tradepb.ErrorCode_INVALID_PARAM || !strings.Contains(info.GetMsg(), "数量太小") {
		t.Fatalf("info = %+v", info)
	}
	if len(capture.errors) != 0 {
		t.Fatalf("不该写错误日志：%v", capture.errors)
	}
}

// 传了不属于该组合账户的交易账户：参数错误和中文提示。
func TestListPositionsRejectsNonMemberAccount(t *testing.T) {
	tradeStore := openRPCStore(t)
	seedRemovalExposure(t, tradeStore)
	if err := tradeStore.Transaction(context.Background(), func(tx *store.Tx) error {
		return tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "space-1", TradingAccountID: "account-2", Name: "account-2",
			Exchange: "BINANCE", Markets: []string{"SWAP"}, ExecutionMode: "PAPER",
			SettlementAsset: "USDT", Status: "ENABLED",
		})
	}); err != nil {
		t.Fatal(err)
	}
	server := &ExecutionServer{Store: tradeStore}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := server.ListPositions(ctx, &tradepb.ListPositionsReq{LogicalAccountId: "logical-1", TradingAccountId: "account-2"})
	if err != nil {
		t.Fatal(err)
	}
	info := rsp.GetRetInfo()
	if info.GetCode() != tradepb.ErrorCode_INVALID_PARAM || !strings.Contains(info.GetMsg(), "不是该组合账户的成员") {
		t.Fatalf("info = %+v", info)
	}
	rsp, err = server.ListPositions(ctx, &tradepb.ListPositionsReq{LogicalAccountId: "logical-1", TradingAccountId: "account-1"})
	if err != nil || rsp.GetRetInfo().GetCode() != tradepb.ErrorCode_SUCCESS || len(rsp.GetPositions()) != 1 {
		t.Fatalf("成员账户应能查到持仓：%+v err=%v", rsp, err)
	}
}

// 存储出错：回固定文案，日志里有原文。
func TestListPositionsStorageErrorUsesFixedMessage(t *testing.T) {
	capture := captureErrorLogs(t)
	tradeStore := openRPCStore(t)
	seedRemovalExposure(t, tradeStore)
	server := &ExecutionServer{Store: tradeStore}
	if err := tradeStore.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := spacecontext.WithSpaceID(context.Background(), "space-1")
	rsp, err := server.ListPositions(ctx, &tradepb.ListPositionsReq{TradingAccountId: "account-1"})
	if err != nil {
		t.Fatal(err)
	}
	info := rsp.GetRetInfo()
	if info.GetCode() != tradepb.ErrorCode_INNER_ERR || info.GetMsg() != "交易服务内部错误，请查看日志" {
		t.Fatalf("info = %+v", info)
	}
	if len(capture.errors) != 1 || !strings.Contains(capture.errors[0], "closed") {
		t.Fatalf("日志里要有原文：%v", capture.errors)
	}
}
