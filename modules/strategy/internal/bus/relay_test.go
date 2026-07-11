package bus

import (
	"context"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"testing"
)

type p struct{ n int }

func (x *p) Publish(context.Context, string, []byte) error { x.n++; return nil }
func TestAcceptOnce(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	db.Exec("create table t_strategy_inbox(c_consumer text,c_message_id text,unique(c_consumer,c_message_id))")
	calls := 0
	f := func(tx *gorm.DB) error { calls++; return nil }
	if err := AcceptOnce(context.Background(), db, "c", "m", f); err != nil {
		t.Fatal(err)
	}
	if err := AcceptOnce(context.Background(), db, "c", "m", f); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}
