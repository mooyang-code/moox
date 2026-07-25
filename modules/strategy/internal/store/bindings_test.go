package store

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestBindingOperationsUpdateAndAuditAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strategy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	s := New(db)
	binding := domain.Binding{BindingID: "binding-1", StrategyID: "strategy-1", StrategyVersion: "1.0.0", SpaceID: "space-1", ViewID: "view-1", Freq: "1m", GroupID: "group-1", Status: "enabled"}
	if err := db.Create(&binding).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_execution_bindings(c_execution_binding_id,c_group_id,c_account_id,c_mode) VALUES(?,?,?,?)", "execution-1", binding.GroupID, "account-1", "observe").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SetExecutionMode(ctx, binding, "paper", "channel-1", "100", "USDT", domain.OperationAudit{OperationID: "op-mode", Operator: "admin", Action: "set_mode", BindingID: binding.BindingID, Reason: "test", RequestID: "op-mode"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBindingStatus(ctx, binding, "disabled", domain.OperationAudit{OperationID: "op-status", Operator: "admin", Action: "pause", BindingID: binding.BindingID, Reason: "test", RequestID: "op-status"}); err != nil {
		t.Fatal(err)
	}
	var execution struct {
		Mode          string `gorm:"column:c_mode"`
		ChannelID     string `gorm:"column:c_channel_id"`
		CapitalAmount string `gorm:"column:c_capital_amount"`
		QuoteAsset    string `gorm:"column:c_quote_asset"`
	}
	if err := db.Table("t_strategy_execution_bindings").Where("c_group_id=?", binding.GroupID).Scan(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if execution.Mode != "paper" || execution.ChannelID != "channel-1" || execution.CapitalAmount != "100" || execution.QuoteAsset != "USDT" {
		t.Fatalf("execution=%+v", execution)
	}
	var status string
	if err := db.Table("t_strategy_bindings").Select("c_status").Where("c_binding_id=?", binding.BindingID).Scan(&status).Error; err != nil {
		t.Fatal(err)
	}
	if status != "disabled" {
		t.Fatalf("status=%q", status)
	}
	var audits int64
	if err := db.Table("t_strategy_operation_audits").Where("c_binding_id=?", binding.BindingID).Count(&audits).Error; err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("audits=%d", audits)
	}
}
