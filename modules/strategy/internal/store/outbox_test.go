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
		message_id TEXT PRIMARY KEY,
		event_data BLOB NOT NULL,
		created_at INTEGER NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	oldest := time.Unix(100, 0).UTC()
	if err := db.Exec("INSERT INTO t_strategy_outbox(message_id,event_data,created_at) VALUES(?,?,?)", "one", []byte("event-data"), oldest.UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_outbox(message_id,event_data,created_at) VALUES(?,?,?)", "two", []byte("event-data"), oldest.Add(time.Minute).UnixMilli()).Error; err != nil {
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
