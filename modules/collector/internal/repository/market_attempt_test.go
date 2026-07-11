package repository

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
)

func TestFinalizeMarketAttemptIsAtomicAndIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:attempt?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.TaskInstance{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	task := domain.TaskInstance{SpaceID: "crypto_binance", TaskID: "task", RuleID: "rule", LastExecStatus: domain.InstanceStatusPending, TaskParams: "{}", Result: "{}", CreateTime: now, ModifyTime: now}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewMarketAttemptRepository(db)
	request := FinalizeAttemptRequest{Attempt: domain.MarketAttempt{JobItemID: "job", AttemptNo: 1, SpaceID: "crypto_binance", MarketID: "crypto_binance", Status: "success", Summary: `{"rows":1}`}, Subjects: []domain.AttemptSubject{{TaskID: "task", SubjectID: "BTC-USDT", Status: "success", Rows: 1}}, Outbox: []domain.AttemptOutbox{{Kind: "resolve", Payload: `{"subject_id":"BTC-USDT"}`}}, Now: now}
	first, err := repo.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.AlreadyFinalized || len(first.Outbox) != 1 {
		t.Fatalf("first=%+v", first)
	}
	request.Attempt.Summary = `{"rows":999}`
	second, err := repo.Finalize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyFinalized || second.Attempt.Summary != `{"rows":1}` || len(second.Outbox) != 1 {
		t.Fatalf("second=%+v", second)
	}
	var updated domain.TaskInstance
	if err := db.Where("c_task_id=?", "task").Take(&updated).Error; err != nil {
		t.Fatal(err)
	}
	if updated.LastExecStatus != domain.InstanceStatusSuccess {
		t.Fatalf("task status=%d", updated.LastExecStatus)
	}
}
