package store

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPendingOutboxStatsReportsCountAndOldest(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE t_strategy_outbox (
		c_message_id TEXT PRIMARY KEY, c_event_data BLOB NOT NULL,
		c_published INTEGER NOT NULL DEFAULT 0, c_claimed_until DATETIME,
		c_claim_token TEXT NOT NULL DEFAULT '', c_ctime DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	oldest := time.Unix(100, 0).UTC()
	if err := db.Exec("INSERT INTO t_strategy_outbox VALUES(?,?,?,?,?,?)", "one", []byte("event-data"), 0, nil, "", oldest).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_outbox VALUES(?,?,?,?,?,?)", "two", []byte("event-data"), 0, nil, "", oldest.Add(time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_outbox VALUES(?,?,?,?,?,?)", "done", []byte("event-data"), 1, nil, "", oldest.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	stats, err := New(db).PendingOutboxStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingCount != 2 || !stats.OldestPending.Equal(oldest) {
		t.Fatalf("stats=%+v", stats)
	}
}
