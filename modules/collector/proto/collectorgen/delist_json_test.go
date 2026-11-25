package collectorpb

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/codec"
)

// 手工登记下架的请求经网关的 JSON 往返：delist_at 是毫秒、在 JSON 里是数字字符串；请求里传 JSON 数字也能解出来。
func TestRegisterDelistNoticeReqTRPCJSONRoundTrip(t *testing.T) {
	message := &RegisterDelistNoticeReq{Exchange: "okx", Market: "swap", ExchangeSymbol: "FOO-USDT-SWAP", DelistAt: 1700000000123, PaperOnly: true}
	serializer := codec.GetSerializer(codec.SerializationTypeJSON)
	raw, err := serializer.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"delist_at":"1700000000123"`) || !strings.Contains(string(raw), `"market":"swap"`) {
		t.Fatalf("JSON 写法不对：%s", raw)
	}
	decoded := new(RegisterDelistNoticeReq)
	if err := serializer.Unmarshal(raw, decoded); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(message, decoded) {
		t.Fatalf("往返后不一致：%v", decoded)
	}
	number := new(RegisterDelistNoticeReq)
	if err := serializer.Unmarshal([]byte(`{"delist_at":1700000000123}`), number); err != nil || number.GetDelistAt() != 1700000000123 {
		t.Fatalf("JSON 数字解码：%v %v", number.GetDelistAt(), err)
	}
}
