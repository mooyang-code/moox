package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"gorm.io/gorm"
)

type recordingPublisher struct {
	mu   sync.Mutex
	rows []domain.OutboxMessage
	err  error
}

func (p *recordingPublisher) Publish(_ context.Context, row domain.OutboxMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows = append(p.rows, row)
	return p.err
}

func openOutboxTestStore(t *testing.T) (*gorm.DB, *store.Store) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE t_strategy_outbox (
		c_id INTEGER PRIMARY KEY AUTOINCREMENT,
		c_message_id TEXT NOT NULL UNIQUE,
		c_event_data BLOB NOT NULL,
		c_ctime DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db, store.New(db)
}

func TestRelayPublishesClaimedOutboxWithStableMessageID(t *testing.T) {
	db, repo := openOutboxTestStore(t)
	if err := db.Exec("INSERT INTO t_strategy_outbox(c_message_id,c_event_data) VALUES(?,?)", "run-1", []byte("event-data")).Error; err != nil {
		t.Fatal(err)
	}
	publisher := &recordingPublisher{}
	relay := &Relay{Store: repo, Publisher: publisher}
	if err := relay.PublishPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(publisher.rows) != 1 || publisher.rows[0].MessageID != "run-1" || len(publisher.rows[0].EventData) == 0 {
		t.Fatalf("published rows=%+v", publisher.rows)
	}
	var count int64
	if err := db.Table("t_strategy_outbox").Where("c_message_id=?", "run-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("remaining=%d", count)
	}
}

func TestRelayReleasesFailedPublishAndRetries(t *testing.T) {
	db, repo := openOutboxTestStore(t)
	if err := db.Exec("INSERT INTO t_strategy_outbox(c_message_id,c_event_data) VALUES(?,?)", "run-1", []byte("event-data")).Error; err != nil {
		t.Fatal(err)
	}
	want := errors.New("broker unavailable")
	publisher := &recordingPublisher{err: want}
	relay := &Relay{Store: repo, Publisher: publisher}
	if err := relay.PublishPending(context.Background(), 1); !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
	var count int64
	if err := db.Table("t_strategy_outbox").Where("c_message_id=?", "run-1").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed row was removed")
	}
	publisher.err = nil
	if err := relay.PublishPending(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(publisher.rows) != 2 {
		t.Fatalf("publish attempts=%d", len(publisher.rows))
	}
}

type expiredFirstPublisher struct {
	published []string
}

func (p *expiredFirstPublisher) Publish(_ context.Context, row domain.OutboxMessage) error {
	if row.MessageID == "expired" {
		return ErrExpiredOutboxMessage
	}
	p.published = append(p.published, row.MessageID)
	return nil
}

func TestRelayDeletesExpiredTargetAndContinues(t *testing.T) {
	db, repo := openOutboxTestStore(t)
	for _, id := range []string{"expired", "current"} {
		if err := db.Exec(
			"INSERT INTO t_strategy_outbox(c_message_id,c_event_data) VALUES(?,?)",
			id,
			[]byte("event-data"),
		).Error; err != nil {
			t.Fatal(err)
		}
	}
	publisher := &expiredFirstPublisher{}
	relay := &Relay{Store: repo, Publisher: publisher}
	if err := relay.PublishPending(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if len(publisher.published) != 1 || publisher.published[0] != "current" {
		t.Fatalf("published=%v", publisher.published)
	}
	var remaining int64
	if err := db.Table("t_strategy_outbox").Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("remaining=%d", remaining)
	}
}
