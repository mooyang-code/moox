package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/schema"
)

func TestInitializeDoesNotCreateSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	var count int64
	if err := mgr.db.Raw(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name LIKE 't_collector_%'`).Scan(&count).Error; err != nil {
		t.Fatalf("query table count: %v", err)
	}
	if count != 0 {
		t.Fatalf("Open() created %d collector tables, want 0", count)
	}
}

func TestApplySchemaCreatesCurrentTaskAndInstanceTables(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatalf("ApplySchema() error = %v", err)
	}
	for _, table := range []string{"t_collector_tasks", "t_collector_task_instances"} {
		var count int64
		if err := mgr.db.Raw("SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&count).Error; err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
	for table, columns := range map[string][]string{
		"t_collector_tasks":          {"c_task_name", "c_description", "c_result_dataset_id", "c_result_view_id", "c_coverage_start_time"},
		"t_collector_task_instances": {"c_instance_id", "c_task_id", "c_function_name", "c_source_id"},
	} {
		for _, column := range columns {
			var count int64
			if err := mgr.db.Raw("SELECT count(*) FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&count).Error; err != nil {
				t.Fatalf("query column %s.%s: %v", table, column, err)
			}
			if count != 1 {
				t.Fatalf("column %s.%s count = %d, want 1", table, column, count)
			}
		}
	}
	if _, _, err := mgr.TaskInstances().List(context.Background(), TaskInstanceFilter{Page: 1, PageSize: 1}); err != nil {
		t.Fatalf("query current task instances: %v", err)
	}
}

func TestApplySchemaRejectsLegacyRuleTable(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "collector.db")
	mgr, err := Open(&Options{Path: dbPath})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.db.Exec(`CREATE TABLE t_collector_task_rules (c_id INTEGER PRIMARY KEY)`).Error; err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := mgr.ApplySchema(schema.AllSQL()); err == nil {
		t.Fatal("ApplySchema() succeeded with a legacy Collector table")
	} else if !strings.Contains(err.Error(), "t_collector_task_rules") {
		t.Fatalf("ApplySchema() error = %v, want legacy table diagnostic", err)
	}
}

func TestDeleteTaskRuntimeRemovesEmptyReadinessParents(t *testing.T) {
	mgr, err := Open(&Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	_, err = mgr.PeriodReadiness().EnsurePeriod(context.Background(), domain.PeriodSeed{
		PeriodKey:  domain.PeriodKey{SpaceID: "crypto", DatasetID: "bars", Frequency: "1m", PeriodTime: time.Now().UTC()},
		DeadlineAt: time.Now().UTC().Add(time.Minute),
		Tasks:      []domain.PeriodTaskSeed{{TaskID: "task-runtime", SubjectID: "BTC-USDT"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mgr.TaskInstances().UpsertMany(context.Background(), []domain.TaskInstance{{SpaceID: "crypto", TaskID: "task-runtime", CollectionTaskID: "task-runtime", DatasetID: "bars", SubjectID: "BTC-USDT", Frequency: "1m"}}); err != nil {
		t.Fatal(err)
	}
	if err := mgr.DeleteTaskRuntime(context.Background(), "crypto", "task-runtime"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := mgr.db.Raw(`SELECT count(*) FROM t_period_readiness`).Scan(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("empty readiness parents = %d, want 0", count)
	}
}
