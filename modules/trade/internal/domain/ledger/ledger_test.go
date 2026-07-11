package ledger

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestBalancedTransfer(t *testing.T) {
	tx, err := Transfer("tx", "freeze", "order", "o", "USDT", "a", "available", "a", "frozen", shared.MustDecimal("5"))
	if err != nil || len(tx.Entries) != 2 {
		t.Fatal(err)
	}
	tx.Entries[1].Amount = shared.MustDecimal("4")
	if tx.Validate() == nil {
		t.Fatal("accepted imbalance")
	}
}
