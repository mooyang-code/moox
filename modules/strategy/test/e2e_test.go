package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestStrategyRunOnceCommitsStateAndOutbox(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	source := `def run(context, data, params, state): return {"action":"rebalance","targets":[{"instrument_id":"BTC-USDT","symbol":"BTCUSDT","target_quantity":"0.5"}],"next_state":{"runs":1}}`
	sum := sha256.Sum256([]byte(source))
	d := domain.StrategyDefinition{StrategyID: "demo", Version: "1.0.0", API: "moox.strategy/v1", SourceCode: source, SourceHash: hex.EncodeToString(sum[:]), Status: "enabled"}
	r := store.New(db)
	if err := r.SaveDefinition(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateBinding(context.Background(), domain.Binding{
		BindingID: "b1", StrategyID: d.StrategyID, StrategyVersion: d.Version,
		SpaceID: "space", ViewID: "view-1", Freq: "1m", GroupID: "group-1", Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateExecutionBinding(context.Background(), domain.ExecutionBinding{
		ExecutionBindingID: "execution-1", GroupID: "group-1", ExchangeAccountID: "account-1",
		Mode: "paper", Status: "enabled",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.State{BindingID: "b1", StrategyVersion: d.Version, Revision: 0, StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "pyworker", "worker.py")
	e, err := engine.New(context.Background(), "python3", root)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	svc := &action.Service{Repo: r, Engine: e}
	task := domain.Task{
		RunID: "run-1", BindingID: "b1", StrategyID: d.StrategyID, Version: d.Version,
		TriggerBarTime: time.Now().UTC().Format(time.RFC3339Nano), DataRevision: "revision-1",
		PreviousState: domain.State{BindingID: "b1", Revision: 0, StateJSON: "{}"},
	}
	out, inputHash, err := svc.Evaluate(context.Background(), task, d)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Commit(context.Background(), task, out, inputHash); err != nil {
		t.Fatal(err)
	}
	if out.Action != domain.ActionRebalance {
		t.Fatal(out)
	}
	var state domain.State
	if err := db.First(&state, "c_binding_id=?", "b1").Error; err != nil || state.Revision != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	var count int64
	db.Table("t_strategy_outbox").Count(&count)
	if count != 1 {
		t.Fatalf("outbox=%d", count)
	}
}
