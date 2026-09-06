package tradepb

import (
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestSubmitOrderPublicContract(t *testing.T) {
	service := File_trade_service_proto.Services().ByName("TradeConsoleService")
	method := service.Methods().ByName("SubmitOrder")
	if method == nil {
		t.Fatal("ordinary SubmitOrder is missing from the console service")
	}
	fields := method.Input().Fields()
	for _, name := range []protoreflect.Name{"logical_account_id", "action_id", "client_order_id", "trading_account_id", "position_side", "deadline_at"} {
		if fields.ByName(name) == nil {
			t.Errorf("SubmitOrder is missing %s", name)
		}
	}
	for _, name := range []protoreflect.Name{"space_id", "owner", "owner_id", "owner_type", "instance_id", "session_id", "reduce_position_only", "reference_price"} {
		if fields.ByName(name) != nil {
			t.Errorf("SubmitOrder exposes trusted field %s", name)
		}
	}
	if (&PlaceManualOrderReq{}).ProtoReflect().Descriptor().Fields().ByName("deadline_at") == nil {
		t.Error("takeover request must support the same absolute deadline")
	}
}

func TestSubmitOrderValidation(t *testing.T) {
	valid := &SubmitOrderReq{
		ActionId: "action-1", LogicalAccountId: "logical-1", TradingAccountId: "account-1",
		ClientOrderId: "client-1", InstrumentId: "BTCUSDT", Quantity: "1",
		OrderType: OrderType_ORDER_TYPE_MARKET, Side: OrderSide_ORDER_SIDE_BUY, Reason: "manual",
	}
	cases := []struct {
		name      string
		mutate    func(*SubmitOrderReq)
		wantError bool
	}{
		{"market", func(*SubmitOrderReq) {}, false},
		{"explicit_deadline", func(r *SubmitOrderReq) { r.DeadlineAt = 2000000000000 }, false},
		{"missing_logical", func(r *SubmitOrderReq) { r.LogicalAccountId = " " }, true},
		{"missing_action", func(r *SubmitOrderReq) { r.ActionId = "" }, true},
		{"missing_client", func(r *SubmitOrderReq) { r.ClientOrderId = "" }, true},
		{"negative_deadline", func(r *SubmitOrderReq) { r.DeadlineAt = -1 }, true},
		{"invalid_quantity", func(r *SubmitOrderReq) { r.Quantity = "NaN" }, true},
		{"invalid_side", func(r *SubmitOrderReq) { r.Side = OrderSide(99) }, true},
		{"invalid_position", func(r *SubmitOrderReq) { r.PositionSide = PositionSide(99) }, true},
		{"market_with_limit", func(r *SubmitOrderReq) { r.LimitPrice = proto.String("100") }, true},
		{"limit_without_price", func(r *SubmitOrderReq) { r.OrderType = OrderType_ORDER_TYPE_LIMIT }, true},
		{"limit", func(r *SubmitOrderReq) {
			r.OrderType = OrderType_ORDER_TYPE_LIMIT
			r.LimitPrice = proto.String("100")
			r.FillPolicy = FillPolicy_FILL_POLICY_GTC
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := proto.Clone(valid).(*SubmitOrderReq)
			tc.mutate(req)
			validator, ok := any(req).(interface{ Validate() error })
			if !ok {
				t.Fatal("SubmitOrder request has no validator")
			}
			if err := validator.Validate(); (err != nil) != tc.wantError {
				t.Fatalf("Validate = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
}

func TestManualOrderRejectsNegativeDeadline(t *testing.T) {
	req := &PlaceManualOrderReq{
		ActionId: "action-1", TradingAccountId: "account-1", ClientOrderId: "client-1",
		InstrumentId: "BTCUSDT", Quantity: "1", OrderType: OrderType_ORDER_TYPE_MARKET,
		Side: OrderSide_ORDER_SIDE_BUY, Reason: "takeover", DeadlineAt: -1,
	}
	if err := req.Validate(); err == nil {
		t.Fatal("negative deadline accepted")
	}
}
