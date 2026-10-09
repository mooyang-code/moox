package store

import (
	"context"
	"database/sql"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const completionRetryBatchSize = 8

var completionRetryColumns = [...]string{
	"c_attempt",
	"c_batch_kind",
	"c_ctime",
	"c_failure_targets_json",
	"c_frequency",
	"c_instance_id",
	"c_last_error_summary",
	"c_last_error_type",
	"c_mtime",
	"c_next_retry_at",
	"c_period_deadline_at",
	"c_period_failure_deadline_exceeded_at",
	"c_period_failure_last_error",
	"c_period_failure_report_state",
	"c_period_failure_results_json",
	"c_period_time",
	"c_retry_key",
	"c_retry_scope",
	"c_source_batch_id",
	"c_space_id",
	"c_status",
	"c_subject_id",
	"c_target_data_time",
	"c_task_json",
	"c_write_target_id",
}

// Prepare on the SQL pool before opening the completion transaction. A statement
// prepared on a transaction is closed at commit and cannot be reused by the next
// batch. database/sql closes the pool statement when Store.Close closes the DB.
func (r *FetchBatchRepository) prepareCompletionRetries(ctx context.Context) ([completionRetryBatchSize]*sql.Stmt, error) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	pool, err := r.db.DB()
	if err != nil {
		return r.retryStatements, err
	}
	for count := 1; count <= completionRetryBatchSize; count++ {
		if r.retryStatements[count-1] != nil {
			continue
		}
		values := make(map[string]any, len(completionRetryColumns))
		for _, column := range completionRetryColumns {
			values[column] = nil
		}
		rows := make([]map[string]any, count)
		for index := range rows {
			rows[index] = values
		}
		generated := r.db.Session(&gorm.Session{DryRun: true, SkipDefaultTransaction: true}).Table((&domain.RetryItem{}).TableName()).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_retry_key"}},
			DoUpdates: clause.Assignments(map[string]any{
				"c_source_batch_id": clause.Expr{SQL: "CASE WHEN c_source_batch_id <> '' THEN c_source_batch_id ELSE excluded.c_source_batch_id END"}, "c_batch_kind": clause.Expr{SQL: "excluded.c_batch_kind"}, "c_attempt": clause.Expr{SQL: "excluded.c_attempt"},
				"c_instance_id": clause.Expr{SQL: "excluded.c_instance_id"}, "c_write_target_id": clause.Expr{SQL: "excluded.c_write_target_id"}, "c_retry_scope": clause.Expr{SQL: "excluded.c_retry_scope"},
				"c_subject_id": clause.Expr{SQL: "excluded.c_subject_id"}, "c_frequency": clause.Expr{SQL: "excluded.c_frequency"}, "c_target_data_time": clause.Expr{SQL: "excluded.c_target_data_time"},
				"c_period_time":        clause.Expr{SQL: "CASE WHEN c_period_time IS NOT NULL THEN c_period_time ELSE excluded.c_period_time END"},
				"c_period_deadline_at": clause.Expr{SQL: "CASE WHEN c_period_deadline_at IS NOT NULL THEN c_period_deadline_at ELSE excluded.c_period_deadline_at END"},
				"c_task_json":          clause.Expr{SQL: "excluded.c_task_json"},
				"c_status":             clause.Expr{SQL: "CASE WHEN c_status IN ('succeeded', 'permanent_failed', 'superseded') THEN c_status ELSE excluded.c_status END"}, "c_next_retry_at": clause.Expr{SQL: "excluded.c_next_retry_at"},
				"c_last_error_type": clause.Expr{SQL: "excluded.c_last_error_type"}, "c_last_error_summary": clause.Expr{SQL: "excluded.c_last_error_summary"},
				"c_failure_targets_json": clause.Expr{SQL: "CASE WHEN c_failure_targets_json <> '' AND c_failure_targets_json <> '[]' THEN c_failure_targets_json ELSE excluded.c_failure_targets_json END"},
				"c_mtime":                clause.Expr{SQL: "excluded.c_mtime"},
			}),
		}).Create(rows)
		if generated.Error != nil {
			return r.retryStatements, generated.Error
		}
		r.retryStatements[count-1], err = pool.PrepareContext(ctx, generated.Statement.SQL.String())
		if err != nil {
			return r.retryStatements, err
		}
	}
	return r.retryStatements, nil
}
