package tradeowner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type proxyStub struct {
	getResponse    *tradepb.GetLogicalAccountRsp
	getErr         error
	closeResponse  *tradepb.CloseTargetSessionRsp
	closeErr       error
	closeRequests  []*tradepb.CloseTargetSessionReq
	waitForContext bool
}

func (s *proxyStub) GetLogicalAccount(ctx context.Context, _ *tradepb.GetLogicalAccountReq, _ ...client.Option) (*tradepb.GetLogicalAccountRsp, error) {
	if s.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.getResponse, s.getErr
}

func (s *proxyStub) CloseTargetSession(ctx context.Context, req *tradepb.CloseTargetSessionReq, _ ...client.Option) (*tradepb.CloseTargetSessionRsp, error) {
	s.closeRequests = append(s.closeRequests, req)
	if s.waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.closeResponse, s.closeErr
}

func strategyAccount() *tradepb.LogicalAccount {
	return &tradepb.LogicalAccount{LogicalAccountId: "logical-1", Markets: []string{"swap"}, SpaceId: "space-1", ControlMode: tradepb.ControlMode_CONTROL_MODE_STRATEGY}
}

// 启用前只读账户：策略控制、市场在组合可交易的市场里才放行；人工控制、市场不符、账户不匹配都拒绝。
func TestCheckAccountRequiresStrategyControlAndMarkets(t *testing.T) {
	stub := &proxyStub{getResponse: &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: strategyAccount()}}
	trade := &Client{proxy: stub, timeout: time.Second}
	if err := trade.CheckAccount(context.Background(), "space-1", "logical-1", []string{"swap"}); err != nil {
		t.Fatal(err)
	}
	if err := trade.CheckAccount(context.Background(), "space-1", "logical-1", []string{"spot"}); !errors.Is(err, ErrMarketMismatch) {
		t.Fatalf("市场不在组合里应拒绝：%v", err)
	}
	manual := strategyAccount()
	manual.ControlMode = tradepb.ControlMode_CONTROL_MODE_MANUAL
	stub.getResponse = &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: manual}
	if err := trade.CheckAccount(context.Background(), "space-1", "logical-1", []string{"swap"}); !errors.Is(err, ErrManualControl) {
		t.Fatalf("人工控制的组合应拒绝：%v", err)
	}
	stub.getResponse = &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{}, LogicalAccount: &tradepb.LogicalAccount{LogicalAccountId: "logical-other", SpaceId: "space-1"}}
	if err := trade.CheckAccount(context.Background(), "space-1", "logical-1", []string{"swap"}); err == nil || !strings.Contains(err.Error(), "不匹配的账户") {
		t.Fatalf("账户不匹配应报错：%v", err)
	}
	stub.getResponse = &tradepb.GetLogicalAccountRsp{}
	if err := trade.CheckAccount(context.Background(), "space-1", "logical-1", []string{"swap"}); err == nil || !strings.Contains(err.Error(), "没有返回状态") {
		t.Fatalf("空响应应报错：%v", err)
	}
}

// Trade 的业务错误保留错误码，原始说明只留在字段里给日志。
func TestCheckAccountKeepsTradeCode(t *testing.T) {
	stub := &proxyStub{getResponse: &tradepb.GetLogicalAccountRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_NOT_FOUND, Msg: "logical account missing"}}}
	trade := &Client{proxy: stub, timeout: time.Second}
	err := trade.CheckAccount(context.Background(), "space-1", "logical-1", []string{"swap"})
	var coded *ResponseError
	if !errors.As(err, &coded) || coded.Code != tradepb.ErrorCode_NOT_FOUND || !strings.Contains(err.Error(), "账户不存在") || strings.Contains(err.Error(), "logical account missing") || Detail(err) != "logical account missing" {
		t.Fatalf("错误应携带 Trade 错误码与中文说明，原始消息只留在字段里：%v", err)
	}
}

// 关闭会话带上会话的身份与开始时刻；账户不存在视为已关闭，其余失败原样返回。
func TestCloseSessionCarriesStartedAt(t *testing.T) {
	stub := &proxyStub{closeResponse: &tradepb.CloseTargetSessionRsp{RetInfo: &tradepb.RetInfo{}, ClosedThrough: 1700}}
	trade := &Client{proxy: stub, timeout: time.Second}
	if err := trade.CloseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", 1700); err != nil {
		t.Fatal(err)
	}
	got := stub.closeRequests[0]
	if got.GetLogicalAccountId() != "logical-1" || got.GetInstanceId() != "instance-1" || got.GetSessionId() != "session-1" || got.GetSessionStartedAt() != 1700 {
		t.Fatalf("关闭请求不符：%+v", got)
	}
	stub.closeResponse = &tradepb.CloseTargetSessionRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_NOT_FOUND, Msg: "missing"}}
	if err := trade.CloseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", 1700); err != nil {
		t.Fatalf("账户不存在应视为已关闭：%v", err)
	}
	stub.closeResponse = &tradepb.CloseTargetSessionRsp{RetInfo: &tradepb.RetInfo{Code: tradepb.ErrorCode_INNER_ERR, Msg: "database locked"}}
	if err := trade.CloseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", 1700); err == nil || !strings.Contains(err.Error(), "关闭会话失败") {
		t.Fatalf("Trade 内部错误应返回：%v", err)
	}
	stub.closeResponse, stub.closeErr = nil, errors.New("dial tcp 10.0.0.1:443: connect refused")
	err := trade.CloseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", 1700)
	var transport *TransportError
	if !errors.As(err, &transport) || strings.Contains(err.Error(), "10.0.0.1") {
		t.Fatalf("传输失败应是结果未知，且不暴露网关地址：%v", err)
	}
}

func TestClientEnforcesTimeoutAndIdentity(t *testing.T) {
	trade := &Client{proxy: &proxyStub{waitForContext: true}, timeout: 20 * time.Millisecond}
	started := time.Now()
	if err := trade.CloseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", 1); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("应超时：%v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("超时耗时过长")
	}
	if err := trade.CheckAccount(context.Background(), "", "logical-1", []string{"swap"}); err == nil || !strings.Contains(err.Error(), "space_id") {
		t.Fatalf("缺少 space_id 应在传输前报错：%v", err)
	}
	if err := trade.CloseSession(context.Background(), "space-1", "logical-1", "instance-1", "session-1", 0); err == nil || !strings.Contains(err.Error(), "开始时刻") {
		t.Fatalf("缺少开始时刻应在传输前报错：%v", err)
	}
	unwired := New(Config{Timeout: time.Second}, nil)
	if err := unwired.CheckAccount(context.Background(), "space-1", "logical-1", []string{"swap"}); err == nil || !strings.Contains(err.Error(), "不可用") {
		t.Fatalf("未接线时应报配置错误：%v", err)
	}
}

func TestConfigValidate(t *testing.T) {
	if err := (Config{Timeout: time.Second}).Validate(); err != nil {
		t.Fatalf("有效配置被拒绝：%v", err)
	}
	if err := (Config{Timeout: -time.Second}).Validate(); err == nil {
		t.Fatal("负超时应被拒绝")
	}
}

func TestCheckMarketsRequiresEveryMarketInLogicalAccountMarkets(t *testing.T) {
	swap, spot, stock := "swap", "spot", "equity"
	if err := checkMarkets([]string{"swap"}, []string{swap}); err != nil {
		t.Fatalf("相同市场不应报错：%v", err)
	}
	// 组合同时可交易现货和合约时，单市场策略和混合市场策略都能绑。
	for _, markets := range [][]string{{"spot"}, {"swap"}, {"spot", "swap"}} {
		if err := checkMarkets(markets, []string{spot, swap}); err != nil {
			t.Fatalf("市场 %v 在组合可交易的市场里，不应报错：%v", markets, err)
		}
	}
	if err := checkMarkets([]string{"equity"}, []string{stock}); err != nil {
		t.Fatalf("股票策略绑股票组合不应报错：%v", err)
	}
	// 混合市场策略只要有一个市场不在组合里就拒绝。
	if err := checkMarkets([]string{"spot", "swap"}, []string{spot}); !errors.Is(err, ErrMarketMismatch) {
		t.Fatalf("有市场不在组合可交易的市场里应拒绝：%v", err)
	}
	if err := checkMarkets([]string{"swap"}, nil); !errors.Is(err, ErrMarketMismatch) || !strings.Contains(err.Error(), "还没有启用的成员") {
		t.Fatalf("组合没有启用的成员应有专门提示：%v", err)
	}
	if err := checkMarkets([]string{""}, []string{swap}); !errors.Is(err, ErrMarketMismatch) {
		t.Fatalf("未知市场应被拒绝：%v", err)
	}
}
