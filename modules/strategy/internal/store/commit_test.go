package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestCommitLogicalRetryIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := New(db)
	if err := db.Create(&domain.State{BindingID: "b", StrategyVersion: "1.0.0", StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	base := domain.Task{RunID: "run-1", BindingID: "b", Version: "1.0.0", Namespace: "default", TriggerBarTime: "t", PreviousState: domain.State{Revision: 0}}
	out := domain.Output{Action: domain.ActionHold, NextState: map[string]any{}}
	if err := r.Commit(context.Background(), base, out, "same"); err != nil {
		t.Fatal(err)
	}
	base.RunID = "retry"
	if err := r.Commit(context.Background(), base, out, "same"); err != nil {
		t.Fatalf("retry should be idempotent: %v", err)
	}
	if err := r.Commit(context.Background(), base, out, "different"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestCommitRebalancePublishesPerExecutionBinding(t *testing.T) {
	tests := []struct {
		name       string
		action     string
		executions []domain.ExecutionBinding
		wantEvents int64
	}{
		{name: "hold", action: domain.ActionHold, wantEvents: 0},
		{name: "observe", action: domain.ActionRebalance, executions: []domain.ExecutionBinding{
			{ExecutionBindingID: "observe-1", Mode: "observe", Status: "enabled"},
		}, wantEvents: 0},
		{name: "paper", action: domain.ActionRebalance, executions: []domain.ExecutionBinding{
			validExecution("paper-1", "paper"),
		}, wantEvents: 1},
		{name: "paper and live", action: domain.ActionRebalance, executions: []domain.ExecutionBinding{
			validExecution("paper-1", "paper"), validExecution("live-1", "live"),
		}, wantEvents: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openCommitDB(t)
			repo := New(db)
			seedCommitBinding(t, db, tt.executions)
			task := commitTask("run-1")
			output := domain.Output{Action: tt.action, Targets: []domain.TargetPosition{{
				InstrumentID: "BTC-USDT", Symbol: "BTCUSDT", TargetQuantity: "0.5",
			}}, NextState: map[string]any{"runs": 1}}
			if err := repo.Commit(context.Background(), task, output, "hash-1"); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Table("t_strategy_outbox").Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != tt.wantEvents {
				t.Fatalf("outbox=%d, want %d", count, tt.wantEvents)
			}
			if count > 0 {
				var rows []struct {
					MessageID string `gorm:"column:c_message_id"`
					EventData []byte `gorm:"column:c_event_data"`
				}
				if err := db.Table("t_strategy_outbox").Order("c_message_id").Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				for _, row := range rows {
					message, err := events.DefaultRegistry()
					if err != nil {
						t.Fatal(err)
					}
					envelope, err := message.UnmarshalMessage(row.EventData)
					if err != nil {
						t.Fatal(err)
					}
					var payload tradeeventpb.TargetIntent
					if err := proto.Unmarshal(envelope.GetPayload(), &payload); err != nil {
						t.Fatal(err)
					}
					if payload.GetExecutionId() != row.MessageID || payload.GetStrategyRunId() != task.RunID ||
						payload.GetExecutionBindingId() != envelope.GetSubjectId() {
						t.Fatalf("invalid rebalance event: envelope=%+v payload=%+v", envelope, &payload)
					}
					if payload.GetCommandSequence() != uint64(task.PreviousState.Revision+1) {
						t.Fatalf("command sequence=%d, want %d", payload.GetCommandSequence(), task.PreviousState.Revision+1)
					}
					ttl := time.UnixMilli(payload.GetNotAfterUnixMs()).Sub(envelope.GetOccurredAt().AsTime())
					if ttl < executionTTL-time.Millisecond || ttl > executionTTL {
						t.Fatalf("target TTL=%s, want %s", ttl, executionTTL)
					}
					if payload.GetExchangeAccountId() != "account-1" || payload.GetDataRevision() != task.DataRevision ||
						len(payload.GetTargets()) != 1 || payload.GetTargets()[0].GetSymbol() != "BTCUSDT" ||
						payload.GetTargets()[0].GetTargetQuantity() != "0.5" {
						t.Fatalf("invalid targets: %+v", payload.GetTargets())
					}
				}
			}
		})
	}
}

func TestCommitRebalanceDuplicateDoesNotAddOutboxRows(t *testing.T) {
	db := openCommitDB(t)
	repo := New(db)
	seedCommitBinding(t, db, []domain.ExecutionBinding{validExecution("paper-1", "paper")})
	task := commitTask("run-1")
	output := domain.Output{Action: domain.ActionRebalance, Targets: []domain.TargetPosition{{
		InstrumentID: "BTC-USDT", Symbol: "BTCUSDT", TargetQuantity: "0.5",
	}}, NextState: map[string]any{"runs": 1}}
	if err := repo.Commit(context.Background(), task, output, "same"); err != nil {
		t.Fatal(err)
	}
	task.RunID = "retry-run"
	if err := repo.Commit(context.Background(), task, output, "same"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("t_strategy_outbox").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("outbox=%d", count)
	}
}

func TestCommitRebalanceSequenceIsMonotonicPerExecutionBindingAcrossStrategyBindings(t *testing.T) {
	db := openCommitDB(t)
	repo := New(db)
	seedCommitBinding(t, db, []domain.ExecutionBinding{validExecution("paper-1", "paper")})
	if err := db.Create(&domain.Binding{
		BindingID: "binding-2", StrategyID: "strategy-2", StrategyVersion: "1",
		SpaceID: "space", ViewID: "view-2", Freq: "1m", GroupID: "group-1", Status: "enabled",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.State{
		BindingID: "binding-2", StrategyVersion: "1", Revision: 0, StateJSON: "{}",
	}).Error; err != nil {
		t.Fatal(err)
	}
	output := domain.Output{
		Action: domain.ActionRebalance,
		Targets: []domain.TargetPosition{{
			InstrumentID: "BTC-USDT", Symbol: "BTCUSDT", TargetQuantity: "0.5",
		}},
		NextState: map[string]any{"runs": 1},
	}
	first := commitTask("run-1")
	if err := repo.Commit(context.Background(), first, output, "hash-1"); err != nil {
		t.Fatal(err)
	}
	second := commitTask("run-2")
	second.BindingID = "binding-2"
	second.StrategyID = "strategy-2"
	second.TriggerBarTime = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	second.PreviousState.BindingID = "binding-2"
	if err := repo.Commit(context.Background(), second, output, "hash-2"); err != nil {
		t.Fatal(err)
	}

	var rows []struct {
		EventData []byte `gorm:"column:c_event_data"`
	}
	if err := db.Table("t_strategy_outbox").Order("c_ctime, c_message_id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("outbox rows=%d, want 2", len(rows))
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for index, row := range rows {
		envelope, err := registry.UnmarshalMessage(row.EventData)
		if err != nil {
			t.Fatal(err)
		}
		var payload tradeeventpb.TargetIntent
		if err := proto.Unmarshal(envelope.GetPayload(), &payload); err != nil {
			t.Fatal(err)
		}
		want := uint64(index + 1)
		if payload.GetCommandSequence() != want {
			t.Fatalf("outbox[%d] command sequence=%d, want %d", index, payload.GetCommandSequence(), want)
		}
	}
}

func TestCommitInvalidExecutionBindingRollsBack(t *testing.T) {
	db := openCommitDB(t)
	repo := New(db)
	invalid := validExecution("paper-1", "paper")
	invalid.ExchangeAccountID = ""
	seedCommitBinding(t, db, []domain.ExecutionBinding{invalid})
	err := repo.Commit(context.Background(), commitTask("run-1"), domain.Output{
		Action:    domain.ActionRebalance,
		Targets:   []domain.TargetPosition{{InstrumentID: "BTC-USDT", Symbol: "BTCUSDT", TargetQuantity: "0.5"}},
		NextState: map[string]any{"runs": 1},
	}, "hash-1")
	if err == nil {
		t.Fatal("invalid execution binding was accepted")
	}
	for _, table := range []string{"t_strategy_runs", "t_strategy_outbox"} {
		var count int64
		if queryErr := db.Table(table).Count(&count).Error; queryErr != nil {
			t.Fatal(queryErr)
		}
		if count != 0 {
			t.Fatalf("%s rows=%d", table, count)
		}
	}
	var state domain.State
	if queryErr := db.First(&state, "c_binding_id=?", "binding-1").Error; queryErr != nil {
		t.Fatal(queryErr)
	}
	if state.Revision != 0 {
		t.Fatalf("state revision=%d", state.Revision)
	}
}

func openCommitDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func validExecution(id, mode string) domain.ExecutionBinding {
	return domain.ExecutionBinding{
		ExecutionBindingID: id, GroupID: "group-1", ExchangeAccountID: "account-1",
		Mode: mode, Status: "enabled",
	}
}

func seedCommitBinding(t *testing.T, db *gorm.DB, executions []domain.ExecutionBinding) {
	t.Helper()
	if err := db.Create(&domain.Binding{
		BindingID: "binding-1", StrategyID: "strategy-1", StrategyVersion: "1",
		SpaceID: "space", ViewID: "view-1", Freq: "1m", GroupID: "group-1", Status: "enabled",
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.State{BindingID: "binding-1", StrategyVersion: "1", Revision: 0, StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	for _, execution := range executions {
		execution.GroupID = "group-1"
		if err := db.Create(&execution).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func commitTask(runID string) domain.Task {
	return domain.Task{
		RunID: runID, BindingID: "binding-1", StrategyID: "strategy-1", Version: "1",
		Namespace: "default", TriggerBarTime: time.Now().UTC().Format(time.RFC3339Nano), DataRevision: "revision-1",
		SpaceID: "space", PreviousState: domain.State{BindingID: "binding-1", StrategyVersion: "1", Revision: 0},
	}
}

func TestCommitConcurrentSameLogicalKeyHasOneRun(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := New(db)
	if err := db.Create(&domain.State{BindingID: "b", StrategyVersion: "1.0.0", StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	out := domain.Output{Action: domain.ActionHold, NextState: map[string]any{}}
	base := domain.Task{BindingID: "b", Version: "1.0.0", Namespace: "default", TriggerBarTime: "t", PreviousState: domain.State{Revision: 0}}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(runID string) {
			defer wg.Done()
			task := base
			task.RunID = runID
			errs <- r.Commit(context.Background(), task, out, "same")
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent logical retry failed: %v", err)
		}
	}
	var count int64
	if err := db.Table("t_strategy_runs").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("runs=%d", count)
	}
}
