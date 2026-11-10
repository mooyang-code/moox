package tradepb

import (
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/commonpb"
)

func TestDelistRequestsRequireExchangeMarketAndSymbol(t *testing.T) {
	valid := func() (Exchange, MarketType, string) {
		return Exchange_EXCHANGE_BINANCE, MarketType_MARKET_TYPE_SPOT, "FOOUSDT"
	}
	exchange, market, symbol := valid()
	if err := (&ListInstrumentHoldersReq{Exchange: exchange, MarketType: market, ExchangeSymbol: symbol}).Validate(); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
	for name, req := range map[string]interface{ Validate() error }{
		"缺交易所":     &ListInstrumentHoldersReq{MarketType: market, ExchangeSymbol: symbol},
		"市场不是现货永续": &ListInstrumentHoldersReq{Exchange: exchange, MarketType: MarketType_MARKET_TYPE_STOCK, ExchangeSymbol: symbol},
		"缺代码":      &UnblockInstrumentReq{Exchange: exchange, MarketType: market},
		"代码带空白":    &UnblockInstrumentReq{Exchange: exchange, MarketType: market, ExchangeSymbol: " FOOUSDT"},
		"封禁缺原因":    &BlockInstrumentReq{Exchange: exchange, MarketType: market, ExchangeSymbol: symbol},
		"清仓缺动作号":   &FlattenInstrumentReq{LogicalAccountId: "l", Exchange: exchange, MarketType: market, ExchangeSymbol: symbol, Reason: "r"},
		"清仓缺组合":    &FlattenInstrumentReq{ActionId: "a", Exchange: exchange, MarketType: market, ExchangeSymbol: symbol, Reason: "r"},
		"清仓缺原因":    &FlattenInstrumentReq{ActionId: "a", LogicalAccountId: "l", Exchange: exchange, MarketType: market, ExchangeSymbol: symbol},
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s：无效请求被接受", name)
		}
	}
	if err := (&ListBlockedInstrumentsReq{}).Validate(); err != nil {
		t.Fatalf("空请求应当有效：%v", err)
	}
}

func TestListOrdersOwnerTypeAndOperatorActionTypeAreChecked(t *testing.T) {
	if err := (&ListOrdersReq{LogicalAccountId: "l", OwnerType: "SYSTEM"}).Validate(); err != nil {
		t.Fatalf("SYSTEM 来源应当有效：%v", err)
	}
	if err := (&ListOrdersReq{LogicalAccountId: "l", OwnerType: "BOGUS"}).Validate(); err == nil {
		t.Fatal("未知来源被接受")
	}
	if err := (&ListOperatorActionsReq{LogicalAccountId: "l", ActionType: "REDUCE"}).Validate(); err != nil {
		t.Fatalf("REDUCE 应当有效：%v", err)
	}
	if err := (&ListOperatorActionsReq{LogicalAccountId: "l", ActionType: "BOGUS"}).Validate(); err == nil {
		t.Fatal("未知动作类型被接受")
	}
	if err := (&ListOperatorActionsReq{ActionType: "REDUCE"}).Validate(); err == nil {
		t.Fatal("缺组合被接受")
	}
	if err := (&ListOperatorActionsReq{LogicalAccountId: "l", Page: &commonpb.Page{Size: 1001}}).Validate(); err == nil {
		t.Fatal("过大的页被接受")
	}
	now := time.Now().UnixMilli()
	day := int64(24 * 60 * 60 * 1000)
	if err := (&SummarizeAccountPnlReq{LogicalAccountId: "l", StartTime: now - 91*day, EndTime: now}).Validate(); err == nil {
		t.Fatal("超过 90 天的时间范围被接受")
	}
	if err := (&SummarizeAccountPnlReq{LogicalAccountId: "l", StartTime: now - 89*day, EndTime: now}).Validate(); err != nil {
		t.Fatalf("89 天应当有效：%v", err)
	}
	if err := (&SummarizeAccountPnlReq{StartTime: 1}).Validate(); err == nil {
		t.Fatal("缺组合和执行账户被接受")
	}
}

// 事件中心的前端按“没有持有者也有 holders 字段”判断“确认没人持有”：经网关的 JSON 序列化必须带上空数组。
func TestListInstrumentHoldersResponseKeepsEmptyHoldersInJSON(t *testing.T) {
	raw := marshalWithTRPCJSON(t, &ListInstrumentHoldersRsp{RetInfo: &RetInfo{}})
	if !strings.Contains(raw, `"holders":[]`) {
		t.Fatalf("没有持有者时必须返回 holders: []，实际 %s", raw)
	}
	if !strings.Contains(raw, `"instrument_unknown":false`) {
		t.Fatalf("响应里必须带 instrument_unknown，实际 %s", raw)
	}
}
