package reconciliation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/trade/internal/application/command"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/order"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconciler_Order_OpenState_ShouldReturnWithoutResolve(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	defer s.Close()

	ctx := context.Background()
	rec := store.OrderRecord{
		SpaceID:        "space-1",
		OrderID:        "order-1",
		ClientOrderID:  "client-1",
		AccountID:      "acct-1",
		ChannelID:      "chan-1",
		Symbol:         "BTC-USDT",
		BaseAsset:      "BTC",
		QuoteAsset:     "USDT",
		Side:           "BUY",
		Quantity:       "1",
		Price:          "100",
		FilledQuantity: "0",
		State:          string(order.Open),
		Version:        1,
	}
	err = s.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateOrder(&rec)
	})
	require.NoError(t, err)

	reconciler := Reconciler{Store: s, Engine: &command.Engine{Store: s}}
	got, err := reconciler.Order(ctx, "space-1", "order-1")
	require.NoError(t, err)
	assert.Equal(t, string(order.Open), got.State)
}
