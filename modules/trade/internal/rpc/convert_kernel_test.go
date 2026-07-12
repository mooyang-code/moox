package rpc

import (
	"testing"

	domainorder "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	mooxpb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"github.com/stretchr/testify/assert"
)

func TestKernelStatusToPB_AllStates_ShouldMap(t *testing.T) {
	cases := []struct {
		state domainorder.State
		want  mooxpb.OrderStatus
	}{
		{domainorder.Open, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.Submitting, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.SubmitUnknown, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.Canceling, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.CancelUnknown, mooxpb.OrderStatus_ORDER_STATUS_SUBMITTED},
		{domainorder.PartiallyFilled, mooxpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED},
		{domainorder.Filled, mooxpb.OrderStatus_ORDER_STATUS_FILLED},
		{domainorder.Canceled, mooxpb.OrderStatus_ORDER_STATUS_CANCELED},
		{domainorder.PartiallyCanceled, mooxpb.OrderStatus_ORDER_STATUS_PARTIAL_CANCELED},
		{domainorder.Rejected, mooxpb.OrderStatus_ORDER_STATUS_REJECTED},
		{domainorder.Expired, mooxpb.OrderStatus_ORDER_STATUS_EXPIRED},
		{domainorder.State("unknown"), mooxpb.OrderStatus_ORDER_STATUS_PENDING},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, kernelStatusToPB(tc.state), "state=%s", tc.state)
	}
}

func TestKernelOrderToPB_BuyAndSell_ShouldPopulateFields(t *testing.T) {
	buy := kernelOrderToPB(store.OrderRecord{
		OrderID: "ord-1", ClientOrderID: "cli-1", ExchangeOrderID: "ex-1",
		AccountID: "acc-1", ChannelID: "ch-1", Symbol: "BTCUSDT",
		Side: "BUY", Price: "100", Quantity: "2", FilledQuantity: "1",
		State: string(domainorder.PartiallyFilled), ReduceOnly: true,
	})
	assert.Equal(t, mooxpb.OrderSide_ORDER_SIDE_BUY, buy.Side)
	assert.Equal(t, mooxpb.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED, buy.Status)
	assert.True(t, buy.ReduceOnly)

	sell := kernelOrderToPB(store.OrderRecord{OrderID: "ord-2", Side: "SELL", State: string(domainorder.Filled)})
	assert.Equal(t, mooxpb.OrderSide_ORDER_SIDE_SELL, sell.Side)
	assert.Equal(t, mooxpb.OrderStatus_ORDER_STATUS_FILLED, sell.Status)
}
