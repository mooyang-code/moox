package store

import (
	"testing"

	orderdomain "github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/stretchr/testify/require"
)

// 未结束订单查询一律用 activeOrderStates：新增订单状态时必须同步，否则会被当成已结束。
func TestActiveOrderStatesMatchDomain(t *testing.T) {
	all := []orderdomain.State{
		orderdomain.Pending, orderdomain.Submitting, orderdomain.SubmitUnknown, orderdomain.Open,
		orderdomain.PartiallyFilled, orderdomain.Canceling, orderdomain.CancelUnknown,
		orderdomain.Filled, orderdomain.Canceled, orderdomain.PartiallyCanceled,
		orderdomain.Rejected, orderdomain.Expired,
	}
	var active []string
	for _, state := range all {
		if !state.Terminal() {
			active = append(active, string(state))
		}
	}
	require.ElementsMatch(t, active, activeOrderStates)
}
