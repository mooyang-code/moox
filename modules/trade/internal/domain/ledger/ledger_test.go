package ledger

import (
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"testing"
)

func TestBalancedTransfer(t *testing.T) {
	amount := shared.MustDecimal("5")
	tx := Transaction{ID: "tx", BizType: "freeze", RefType: "order", RefID: "o", Entries: []Entry{
		{AccountID: "a", Asset: "USDT", Bucket: "available", Amount: amount.Neg()},
		{AccountID: "a", Asset: "USDT", Bucket: "frozen", Amount: amount},
	}}
	if err := tx.Validate(); err != nil {
		t.Fatal(err)
	}
	tx.Entries[1].Amount = shared.MustDecimal("4")
	if tx.Validate() == nil {
		t.Fatal("accepted imbalance")
	}
}
