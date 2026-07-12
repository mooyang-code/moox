package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestRunOnceEvaluatesAndOptionallyCommits(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	source := `def run(context, data, params, state): return {"action":"rebalance","targets":[{"instrument_id":"BTC","target_weight":"0.25"}],"next_state":{"revision":context["data_revision"],"close":data.iloc[0]["close"]}}`
	h := sha256.Sum256([]byte(source))
	d := domain.StrategyDefinition{StrategyID: "demo", Version: "1.0.0", API: "moox.strategy/v1", SourceCode: source, SourceHash: hex.EncodeToString(h[:]), Status: "enabled"}
	r := store.New(db)
	if err := r.SaveDefinition(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.Binding{BindingID: "b1", StrategyID: d.StrategyID, StrategyVersion: d.Version, ParamsJSON: "{}", Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.State{BindingID: "b1", StrategyVersion: d.Version, StateJSON: "{}"}).Error; err != nil {
		t.Fatal(err)
	}
	eng, err := engine.New(context.Background(), "python3", "../../pyworker/worker.py")
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	svc := &Service{Repo: r, Registry: &registry.Service{Repo: r}, Engine: eng}
	observed, err := svc.RunOnce(context.Background(), &strategypb.RunOnceReq{BindingId: "b1", TriggerBarTime: "2026-07-11T10:00:00Z", DataJson: `[{"close":42}]`, DataRevision: "rev-1"})
	if err != nil || observed.GetRetInfo().GetCode() != 0 || observed.GetRun().GetStatus() != "observed" {
		t.Fatalf("observed=%+v err=%v", observed, err)
	}
	if !strings.Contains(observed.GetRun().GetOutputJson(), `"rev-1"`) {
		t.Fatalf("snapshot revision missing from output: %s", observed.GetRun().GetOutputJson())
	}
	committed, err := svc.RunOnce(context.Background(), &strategypb.RunOnceReq{BindingId: "b1", TriggerBarTime: "2026-07-11T10:01:00Z", Commit: true, DataJson: `[{"close":43}]`, DataRevision: "rev-2"})
	if err != nil || committed.GetRetInfo().GetCode() != 0 || committed.GetRun().GetStatus() != "accepted" {
		t.Fatalf("committed=%+v err=%v", committed, err)
	}
	var state domain.State
	if err := db.First(&state, "c_binding_id=?", "b1").Error; err != nil || state.Revision != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
