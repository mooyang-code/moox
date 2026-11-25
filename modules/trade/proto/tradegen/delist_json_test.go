package tradepb

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 下架记录经网关的 JSON 序列化往返不变：时刻是毫秒、在 JSON 里是数字字符串，交易所与市场是小写文本，
// 交易所代码保持交易所原文。
func TestDelistTRPCJSONRoundTrip(t *testing.T) {
	const at = int64(1700000000123)
	message := &Delist{
		DelistId: "d1", Exchange: "binance", MarketType: "swap", ExchangeSymbol: "FOOUSDT", Scope: "ALL",
		DelistAt: at + 86400000, CreatedAt: at, FinishedAt: at + 1,
		Flattens: []*DelistFlatten{{Round: 1, SpaceId: "crypto", LogicalAccountId: "la", CreatedAt: at + 2}},
	}
	serializer := codec.GetSerializer(codec.SerializationTypeJSON)
	raw, err := serializer.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(Delist)
	if err := serializer.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(message, decoded) {
		t.Fatalf("往返后不一致：%v", decoded)
	}
	var tree struct {
		Exchange       string `json:"exchange"`
		MarketType     string `json:"market_type"`
		ExchangeSymbol string `json:"exchange_symbol"`
		DelistAt       any    `json:"delist_at"`
		CreatedAt      any    `json:"created_at"`
		FinishedAt     any    `json:"finished_at"`
		Flattens       []struct {
			CreatedAt any `json:"created_at"`
		} `json:"flattens"`
	}
	if err := json.Unmarshal(raw, &tree); err != nil {
		t.Fatal(err)
	}
	if tree.Exchange != "binance" || tree.MarketType != "swap" || tree.ExchangeSymbol != "FOOUSDT" {
		t.Fatalf("交易所、市场、代码写法不对：%s", raw)
	}
	for name, value := range map[string]any{"delist_at": tree.DelistAt, "created_at": tree.CreatedAt, "finished_at": tree.FinishedAt, "flattens.0.created_at": tree.Flattens[0].CreatedAt} {
		if _, ok := value.(string); !ok {
			t.Errorf("%s 应是数字字符串：%s", name, raw)
		}
	}
	if tree.DelistAt != "1700086400123" {
		t.Errorf("delist_at = %v", tree.DelistAt)
	}
}
