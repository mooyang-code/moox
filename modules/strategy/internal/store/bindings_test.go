package store

import (
	"context"
	"encoding/json"
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
	if err := db.Exec("INSERT INTO t_strategy_execution_bindings(c_execution_binding_id,c_group_id,c_exchange_account_id,c_mode) VALUES(?,?,?,?)", "execution-1", binding.GroupID, "account-1", "observe").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO t_strategy_execution_bindings(c_execution_binding_id,c_group_id,c_exchange_account_id,c_mode) VALUES(?,?,?,?)", "execution-2", binding.GroupID, "account-live", "live").Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.SetExecutionMode(ctx, binding, "execution-1", "paper", "account-2", domain.OperationAudit{OperationID: "op-mode", Operator: "admin", Action: "set_mode", BindingID: binding.BindingID, Reason: "test", RequestID: "op-mode"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBindingStatus(ctx, binding, "disabled", domain.OperationAudit{OperationID: "op-status", Operator: "admin", Action: "pause", BindingID: binding.BindingID, Reason: "test", RequestID: "op-status"}); err != nil {
		t.Fatal(err)
	}
	var execution struct {
		Mode              string `gorm:"column:c_mode"`
		ExchangeAccountID string `gorm:"column:c_exchange_account_id"`
	}
	if err := db.Table("t_strategy_execution_bindings").Where("c_execution_binding_id=?", "execution-1").Scan(&execution).Error; err != nil {
		t.Fatal(err)
	}
	if execution.Mode != "paper" || execution.ExchangeAccountID != "account-2" {
		t.Fatalf("execution=%+v", execution)
	}
	var untouched struct {
		Mode              string `gorm:"column:c_mode"`
		ExchangeAccountID string `gorm:"column:c_exchange_account_id"`
	}
	if err := db.Table("t_strategy_execution_bindings").Where("c_execution_binding_id=?", "execution-2").Scan(&untouched).Error; err != nil {
		t.Fatal(err)
	}
	if untouched.Mode != "live" || untouched.ExchangeAccountID != "account-live" {
		t.Fatalf("untouched execution=%+v", untouched)
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
	var audit domain.OperationAudit
	if err := db.First(&audit, "c_operation_id=?", "op-mode").Error; err != nil {
		t.Fatal(err)
	}
	var oldValue, newValue domain.ExecutionModeAuditValue
	if err := json.Unmarshal([]byte(audit.OldValue), &oldValue); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(audit.NewValue), &newValue); err != nil {
		t.Fatal(err)
	}
	if oldValue.ExecutionBindingID != "execution-1" ||
		oldValue.Mode != "observe" || oldValue.ExchangeAccountID != "account-1" ||
		newValue.ExecutionBindingID != "execution-1" ||
		newValue.Mode != "paper" || newValue.ExchangeAccountID != "account-2" {
		t.Fatalf("audit old=%+v new=%+v", oldValue, newValue)
	}
}
