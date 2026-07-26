package ledger

import (
	"errors"
	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

var ErrUnbalanced = errors.New("trade: ledger transaction is not balanced")

type Entry struct {
	AccountID, Asset, Bucket string
	Amount                   shared.Decimal
}
type Transaction struct {
	ID                      shared.LedgerTransactionID
	BizType, RefType, RefID string
	Entries                 []Entry
}

func (t Transaction) Validate() error {
	if t.ID == "" || t.RefType == "" || t.RefID == "" || len(t.Entries) < 2 {
		return ErrUnbalanced
	}
	totals := map[string]shared.Decimal{}
	for _, e := range t.Entries {
		totals[e.Asset] = totals[e.Asset].Add(e.Amount)
	}
	for _, v := range totals {
		if !v.IsZero() {
			return ErrUnbalanced
		}
	}
	return nil
}
