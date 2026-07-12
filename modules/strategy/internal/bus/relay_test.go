package bus

import (
	"context"
	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"gorm.io/gorm"
	"testing"
)

type idempotentPublisher struct{ ids []string }

func (p *idempotentPublisher) Publish(context.Context, string, []byte) error { return nil }
func (p *idempotentPublisher) PublishWithID(_ context.Context, id, _ string, _ []byte) error {
	p.ids = append(p.ids, id)
	return nil
}

func TestRelayPublishesClaimedOutboxWithStableMessageID(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE t_strategy_outbox (
		c_message_id TEXT PRIMARY KEY,
		c_topic TEXT NOT NULL,
		c_payload BLOB NOT NULL,
		c_published INTEGER NOT NULL DEFAULT 0,
		c_claimed_until DATETIME,
		c_claim_token TEXT NOT NULL DEFAULT '',
		c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_outbox(c_message_id,c_topic,c_payload) VALUES(?,?,?)", "run-1", "topic", []byte(`{"ok":true}`)).Error; err != nil {
		t.Fatal(err)
	}
	p := &idempotentPublisher{}
	if err := (&Relay{Store: store.New(db), Publisher: p}).PublishPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(p.ids) != 1 || p.ids[0] != "run-1" {
		t.Fatalf("published ids=%v", p.ids)
	}
	var published int
	if err := db.Table("t_strategy_outbox").Select("c_published").Where("c_message_id=?", "run-1").Scan(&published).Error; err != nil {
		t.Fatal(err)
	}
	if published != 1 {
		t.Fatalf("published=%d", published)
	}
}
