package tradepb

import (
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-go/codec"
)

// 管理台按 JSON 读写订单保留天数：0（不清理）要原样带上，不能当成没传；请求里的 0 解出来也不能是“没传”。
func TestOrderRetentionDaysZeroSurvivesJSON(t *testing.T) {
	zero := int32(0)
	raw := marshalWithTRPCJSON(t, &TradingAccount{ExecutionConfig: &TradingAccount_Paper{Paper: &PaperConfig{OrderRetentionDays: &zero}}})
	if !strings.Contains(raw, `"order_retention_days":0`) {
		t.Fatalf("响应里缺 0 天：%s", raw)
	}
	var request UpdateTradingAccountReq
	if err := codec.GetSerializer(codec.SerializationTypeJSON).Unmarshal([]byte(`{"trading_account_id":"p1","order_retention_days":0}`), &request); err != nil {
		t.Fatal(err)
	}
	if request.OrderRetentionDays == nil || *request.OrderRetentionDays != 0 {
		t.Fatalf("请求里的 0 天解成了 %v", request.OrderRetentionDays)
	}
	var omitted UpdateTradingAccountReq
	if err := codec.GetSerializer(codec.SerializationTypeJSON).Unmarshal([]byte(`{"trading_account_id":"p1"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	if omitted.OrderRetentionDays != nil {
		t.Fatalf("没传天数应为 nil，得到 %v", *omitted.OrderRetentionDays)
	}
}
