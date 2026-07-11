package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/repository"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestStrategyFrontendQueriesAndPerformanceE2E(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.StrategyDefinition{StrategyID: "momentum", Version: "1.0.0", API: "moox.strategy/v1", ManifestYAML: "id: momentum", SourceCode: "def run(): pass", SourceHash: "hash-momentum", Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.Binding{BindingID: "binding-paper", StrategyID: "momentum", StrategyVersion: "1.0.0", SpaceID: "space-1", ViewID: "view-1", Freq: "1h", GroupID: "group-1", Status: "enabled"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.State{BindingID: "binding-paper", StrategyVersion: "1.0.0", Revision: 2, StateJSON: "{}", LastRunID: "run-1"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&domain.BindingHealth{BindingID: "binding-paper", Status: "running", Mode: "paper", LastRunID: "run-1", LastDataRevision: "view:2", WorkerStatus: "ready", ObservedAt: time.Now().UTC()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_execution_bindings(c_execution_binding_id,c_group_id,c_account_id,c_mode,c_status) VALUES(?,?,?,?,?)", "exec-1", "group-1", "paper-account", "paper", "enabled").Error; err != nil {
		t.Fatal(err)
	}
	point := domain.PerformancePoint{BindingID: "binding-paper", Source: "paper", PointTime: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC), NAV: "1.02", CumulativeReturn: "0.02", Drawdown: "0", GrossExposure: "1", NetExposure: "1", Turnover: "0.1", Fees: "0.01", DataRevision: "paper:2"}
	if err := db.Create(&point).Error; err != nil {
		t.Fatal(err)
	}
	service := &rpc.Service{Repo: repository.New(db)}
	ctx := context.Background()
	list, err := service.ListRunningStrategies(ctx, &strategypb.ListRunningStrategiesReq{Page: &strategypb.PageReq{Page: 1, PageSize: 20}, SpaceId: "space-1"})
	if err != nil || list.GetRetInfo().GetCode() != 0 || len(list.GetItems()) != 1 {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	overview, err := service.GetStrategyOverview(ctx, &strategypb.GetStrategyOverviewReq{BindingId: "binding-paper"})
	if err != nil || overview.GetHealth().GetMode() != "paper" || overview.GetDefinition().GetSourceHash() != "hash-momentum" {
		t.Fatalf("overview=%+v err=%v", overview, err)
	}
	performance, err := service.GetStrategyPerformance(ctx, &strategypb.GetStrategyPerformanceReq{BindingId: "binding-paper", PerformanceSource: "paper"})
	if err != nil || len(performance.GetPoints()) != 1 || performance.GetPerformanceSource() != "paper" {
		t.Fatalf("performance=%+v err=%v", performance, err)
	}
	paused, err := service.PauseBinding(ctx, &strategypb.BindingOperationReq{BindingId: "binding-paper", Reason: "maintenance", OperationId: "op-pause-1"})
	if err != nil || paused.GetRetInfo().GetCode() != 0 || paused.GetStatus() != "disabled" {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	var audits int64
	db.Table("t_strategy_operation_audits").Where("c_operation_id=?", "op-pause-1").Count(&audits)
	if audits != 1 {
		t.Fatalf("audits=%d", audits)
	}
	mode, err := service.SetExecutionMode(ctx, &strategypb.SetExecutionModeReq{BindingId: "binding-paper", Mode: "observe", Reason: "monitor", OperationId: "op-mode-1"})
	if err != nil || mode.GetRetInfo().GetCode() != 0 || mode.GetStatus() != "observe" {
		t.Fatalf("mode=%+v err=%v", mode, err)
	}
}
