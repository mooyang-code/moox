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
		c_id INTEGER PRIMARY KEY AUTOINCREMENT, c_message_id TEXT NOT NULL UNIQUE,
		c_event_data BLOB NOT NULL, c_ctime DATETIME NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	oldest := time.Unix(100, 0).UTC()
	if err := db.Exec("INSERT INTO t_strategy_outbox(c_message_id,c_event_data,c_ctime) VALUES(?,?,?)", "one", []byte("event-data"), oldest).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_outbox(c_message_id,c_event_data,c_ctime) VALUES(?,?,?)", "two", []byte("event-data"), oldest.Add(time.Minute)).Error; err != nil {
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
