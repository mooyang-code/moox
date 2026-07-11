package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTransactionRollbackInboxAndOutbox(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "trade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	e = s.Transaction(ctx, func(tx *Tx) error {
		ok, e := tx.InsertInbox("c", "m", "t")
		if e != nil || !ok {
			t.Fatalf("insert: %v %v", ok, e)
		}
		if e = tx.AddOutbox("m", "t", []byte("x")); e != nil {
			t.Fatal(e)
		}
		return ErrConflict
	})
	if e != ErrConflict {
		t.Fatal(e)
	}
	var n int64
	s.db.Table("t_trade_inbox").Count(&n)
	if n != 0 {
		t.Fatalf("inbox rows=%d", n)
	}
}
func TestInboxAndFillIdempotency(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "trade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	if e = s.Transaction(ctx, func(tx *Tx) error {
		a, e := tx.InsertInbox("c", "m", "t")
		if e != nil || !a {
			return e
		}
		b, e := tx.InsertInbox("c", "m", "t")
		if e != nil || b {
			t.Fatal("duplicate inbox applied")
		}
		a, e = tx.InsertFill("s", "f", "ef", "a", "c", "BTCUSDT", "o", "1", "1", "0", "")
		if e != nil || !a {
			return e
		}
		b, e = tx.InsertFill("s", "f2", "ef", "a", "c", "BTCUSDT", "o", "1", "1", "0", "")
		if e != nil || b {
			t.Fatal("duplicate exchange fill applied")
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
