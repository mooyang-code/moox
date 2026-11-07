package store

import (
	"context"
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func executionFromRow(row executionRow) Execution {
	return Execution(row)
}

func executionToRow(execution Execution) executionRow {
	return executionRow(execution)
}

// Tx 是接收事件的事务：回执、新建或合并执行记录要么一起成功，要么一起失败，写库成功才确认消息。
type Tx struct {
	db *gorm.DB
}

// InTx 在一个事务里执行 fn。
func (s *Store) InTx(ctx context.Context, fn func(*Tx) error) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return fn(&Tx{db: tx}) })
}

// ClaimReceipt 记下“这条事件对这条规则已经处理”。返回 false 说明以前处理过，调用方直接跳过。
func (t *Tx) ClaimReceipt(eventID, ruleID string, now int64) (bool, error) {
	result := t.db.Exec(`INSERT INTO t_event_receipts (c_event_id, c_rule_id, c_created_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`, eventID, ruleID, now)
	return result.RowsAffected == 1, result.Error
}

// InsertExecution 新建一条执行记录。
func (t *Tx) InsertExecution(execution Execution) error {
	row := executionToRow(execution)
	return t.db.Create(&row).Error
}

// FindUnfinishedByDedupe 按合并键找还没结束的执行记录（系统动作：同一规则、同一标的只有一条）。
func (t *Tx) FindUnfinishedByDedupe(key string) (Execution, bool, error) {
	var row executionRow
	err := t.db.Where("c_dedupe_key = ? AND c_status IN ?", key, []string{StatusPending, StatusRunning}).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return Execution{}, false, nil
	}
	if err != nil {
		return Execution{}, false, err
	}
	return executionFromRow(row), true, nil
}

// ReplaceExecutionEvent 把合并进来的新事件（ID、内容、发生时间）写进已有的执行记录，并让工作线程立即处理它：
// 记录里的事件 ID 始终是最近合并的那条，详情和对账能对上实际生效的事件。
func (t *Tx) ReplaceExecutionEvent(executionID, eventID, payloadJSON string, occurredAt, now int64) error {
	return t.db.Model(&executionRow{}).Where("c_execution_id = ?", executionID).
		Updates(map[string]any{"c_event_id": eventID, "c_payload_json": payloadJSON, "c_occurred_at": occurredAt, "c_next_run_at": now, "c_updated_at": now}).Error
}

// ClaimDue 取出到期的待处理记录并置为执行中。systemActions 是已经注册了处理器的系统动作：
// 没有处理器的系统动作记录不取，免得每轮都到期却没人处理。
func (s *Store) ClaimDue(ctx context.Context, now int64, limit int, systemActions []string) ([]Execution, error) {
	return s.ClaimDueExcluding(ctx, now, limit, systemActions, nil)
}

// ClaimDueExcluding 与 ClaimDue 相同，但不取 excludeRules 里的规则的记录：某条规则已经占满了它的并发名额时，
// 不让它的慢接口占住全部处理能力。
func (s *Store) ClaimDueExcluding(ctx context.Context, now int64, limit int, systemActions, excludeRules []string) ([]Execution, error) {
	var claimed []Execution
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []executionRow
		query := tx.Table("t_event_executions AS e").Select("e.*").
			Joins("LEFT JOIN t_event_rules r ON r.c_rule_id = e.c_rule_id").
			Where("e.c_status = ? AND e.c_next_run_at > 0 AND e.c_next_run_at <= ?", StatusPending, now)
		if len(systemActions) == 0 {
			query = query.Where("e.c_action_type = ?", ActionNotifyURL)
		} else {
			query = query.Where("(e.c_action_type = ? OR r.c_system_action IN ?)", ActionNotifyURL, systemActions)
		}
		if len(excludeRules) > 0 {
			query = query.Where("e.c_rule_id NOT IN ?", excludeRules)
		}
		if err := query.Order("e.c_next_run_at ASC, e.c_execution_id ASC").Limit(limit).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Model(&executionRow{}).Where("c_execution_id = ?", row.ExecutionID).
				Updates(map[string]any{"c_status": StatusRunning, "c_updated_at": now}).Error; err != nil {
				return err
			}
			row.Status = StatusRunning
			claimed = append(claimed, executionFromRow(row))
		}
		return nil
	})
	return claimed, err
}

// RecoverRunning 把进程退出时停在执行中的记录放回待处理：通知的投递是“至少一次”，重复发送由对方按事件 ID 去重。
func (s *Store) RecoverRunning(ctx context.Context, now int64) (int64, error) {
	result := s.db.WithContext(ctx).Model(&executionRow{}).Where("c_status = ?", StatusRunning).
		Updates(map[string]any{"c_status": StatusPending, "c_next_run_at": now, "c_updated_at": now})
	return result.RowsAffected, result.Error
}

// ReleaseClaim 把刚领取、还没开始处理的记录放回待处理（保持原来的到期时间）：领取后发现它所属的规则已经占满并发名额。
func (s *Store) ReleaseClaim(ctx context.Context, executionID string, now int64) error {
	return s.db.WithContext(ctx).Model(&executionRow{}).Where("c_execution_id = ? AND c_status = ?", executionID, StatusRunning).
		Updates(map[string]any{"c_status": StatusPending, "c_updated_at": now}).Error
}

// RecoverStaleRunning 把停在“执行中”超过 staleBefore（Unix 毫秒）没更新的记录放回待处理：
// 处理它的线程卡死或丢失结果时，记录不会永远停在执行中。返回放回的条数。
func (s *Store) RecoverStaleRunning(ctx context.Context, staleBefore, now int64) (int64, error) {
	result := s.db.WithContext(ctx).Model(&executionRow{}).Where("c_status = ? AND c_updated_at < ?", StatusRunning, staleBefore).
		Updates(map[string]any{"c_status": StatusPending, "c_next_run_at": now, "c_updated_at": now})
	return result.RowsAffected, result.Error
}

// Result 是处理一次之后写回的结果。
type Result struct {
	Status        string
	NextRunAt     int64
	LastError     string
	Attempts      int
	Failures      int
	FirstFailedAt int64
	DurationMS    int64
	FinishedAt    int64
	// Attempt 非空时同时记一条通知尝试。
	Attempt *Attempt
	// ProcessedPayload 是本次处理时依据的事件内容。处理期间如果有新的事件合并进来（下架时间改了），
	// 库里的内容就和它不同：这次的结果按旧内容算出，不能覆盖新安排，记录放回待处理并立即重新处理。
	ProcessedPayload string
}

// SaveResult 写回处理结果。只有记录仍是执行中才写（处理期间被取消的记录保持已取消）；返回是否写入。
// 处理期间事件内容被合并更新过（见 Result.ProcessedPayload）时，记录改为待处理并立即到期，其余计数照常写入。
func (s *Store) SaveResult(ctx context.Context, executionID string, result Result, now int64) (bool, error) {
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current executionRow
		if err := tx.Where("c_execution_id = ? AND c_status = ?", executionID, StatusRunning).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if result.ProcessedPayload != "" && current.PayloadJSON != result.ProcessedPayload && result.Status != StatusCanceled {
			result.Status, result.NextRunAt, result.FinishedAt = StatusPending, now, 0
		}
		update := tx.Model(&executionRow{}).Where("c_execution_id = ? AND c_status = ?", executionID, StatusRunning).
			Updates(map[string]any{
				"c_status": result.Status, "c_next_run_at": result.NextRunAt, "c_last_error": result.LastError,
				"c_attempts": result.Attempts, "c_failures": result.Failures, "c_first_failed_at": result.FirstFailedAt, "c_duration_ms": result.DurationMS,
				"c_finished_at": result.FinishedAt, "c_updated_at": now,
			})
		if update.Error != nil || update.RowsAffected == 0 {
			return update.Error
		}
		applied = true
		if result.Attempt != nil {
			row := attemptRow(*result.Attempt)
			row.ExecutionID = executionID
			return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
		}
		return nil
	})
	return applied, err
}

// GetExecution 按 ID 取执行记录。
func (s *Store) GetExecution(ctx context.Context, executionID string) (Execution, error) {
	var row executionRow
	if err := s.db.WithContext(ctx).Where("c_execution_id = ?", executionID).Take(&row).Error; err != nil {
		return Execution{}, notFound(err)
	}
	return executionFromRow(row), nil
}

// ExecutionFilter 是列表的筛选条件，空值表示不限。
type ExecutionFilter struct {
	RuleID string
	Status string
}

// ListExecutions 按创建时间倒序列出执行记录，返回总数。
func (s *Store) ListExecutions(ctx context.Context, filter ExecutionFilter, offset, limit int) ([]Execution, int64, error) {
	query := s.db.WithContext(ctx).Model(&executionRow{})
	if filter.RuleID != "" {
		query = query.Where("c_rule_id = ?", filter.RuleID)
	}
	if filter.Status != "" {
		query = query.Where("c_status = ?", filter.Status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []executionRow
	if err := query.Order("c_created_at DESC, c_execution_id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	executions := make([]Execution, 0, len(rows))
	for _, row := range rows {
		executions = append(executions, executionFromRow(row))
	}
	return executions, total, nil
}

// ListAttempts 返回一条执行记录的全部通知尝试。
func (s *Store) ListAttempts(ctx context.Context, executionID string) ([]Attempt, error) {
	var rows []attemptRow
	if err := s.db.WithContext(ctx).Where("c_execution_id = ?", executionID).Order("c_attempt ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	attempts := make([]Attempt, 0, len(rows))
	for _, row := range rows {
		attempts = append(attempts, Attempt(row))
	}
	return attempts, nil
}

// RetryExecution 把失败的记录放回待处理，立即重新执行；已经确认过的标记清除。
// 系统动作的记录要先确认同一标的没有别的未结束记录，否则返回 ErrConflict。
func (s *Store) RetryExecution(ctx context.Context, executionID string, now int64) (Execution, error) {
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row executionRow
		if err := tx.Where("c_execution_id = ?", executionID).Take(&row).Error; err != nil {
			return notFound(err)
		}
		if row.Status != StatusFailed {
			return ErrConflict
		}
		if row.DedupeKey != "" {
			var others int64
			if err := tx.Model(&executionRow{}).Where("c_dedupe_key = ? AND c_status IN ? AND c_execution_id <> ?", row.DedupeKey, []string{StatusPending, StatusRunning}, executionID).Count(&others).Error; err != nil {
				return err
			}
			if others != 0 {
				return ErrConflict
			}
		}
		updates := map[string]any{
			"c_status": StatusPending, "c_next_run_at": now, "c_failures": 0, "c_first_failed_at": 0, "c_acknowledged": false,
			"c_finished_at": 0, "c_updated_at": now,
		}
		return tx.Model(&executionRow{}).Where("c_execution_id = ?", executionID).Updates(updates).Error
	})
	if err != nil {
		return Execution{}, err
	}
	return s.GetExecution(ctx, executionID)
}

// CancelExecution 把还没结束的记录置为已取消。第二个返回值是取消前的记录，调用方据此做善后（下架：解除禁止）。
func (s *Store) CancelExecution(ctx context.Context, executionID string, now int64) (before Execution, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row executionRow
		if err := tx.Where("c_execution_id = ?", executionID).Take(&row).Error; err != nil {
			return notFound(err)
		}
		if row.Status != StatusPending && row.Status != StatusRunning {
			return ErrConflict
		}
		before = executionFromRow(row)
		// 系统动作的记录在同一条 UPDATE 里记下“善后未完成”：之后无论请求超时还是进程重启，都还能再取消一次重做善后。
		return tx.Model(&executionRow{}).Where("c_execution_id = ?", executionID).
			Updates(map[string]any{
				"c_status": StatusCanceled, "c_next_run_at": 0, "c_finished_at": now, "c_updated_at": now,
				"c_cleanup_pending": row.ActionType == ActionSystem,
			}).Error
	})
	return before, err
}

// FinishCleanup 记下取消后的善后已经做完：清除“善后未完成”标记和失败说明。
func (s *Store) FinishCleanup(ctx context.Context, executionID string, now int64) error {
	return s.db.WithContext(ctx).Model(&executionRow{}).Where("c_execution_id = ?", executionID).
		Updates(map[string]any{"c_cleanup_pending": false, "c_last_error": "", "c_updated_at": now}).Error
}

// SetLastError 改写执行记录的“最近一次错误”说明（不改状态）。
func (s *Store) SetLastError(ctx context.Context, executionID, message string, now int64) error {
	return s.db.WithContext(ctx).Model(&executionRow{}).Where("c_execution_id = ?", executionID).
		Updates(map[string]any{"c_last_error": message, "c_updated_at": now}).Error
}

// ListPendingCleanups 返回已取消但善后还没做成的系统动作记录，用于计算告警关注项。
func (s *Store) ListPendingCleanups(ctx context.Context) ([]Execution, error) {
	var rows []executionRow
	if err := s.db.WithContext(ctx).Where("c_status = ? AND c_cleanup_pending = 1", StatusCanceled).Order("c_created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	executions := make([]Execution, 0, len(rows))
	for _, row := range rows {
		executions = append(executions, executionFromRow(row))
	}
	return executions, nil
}

// AcknowledgeExecution 确认失败的记录已处理，清除它引起的告警。
func (s *Store) AcknowledgeExecution(ctx context.Context, executionID string, now int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row executionRow
		if err := tx.Where("c_execution_id = ?", executionID).Take(&row).Error; err != nil {
			return notFound(err)
		}
		if row.Status != StatusFailed {
			return ErrConflict
		}
		return tx.Model(&executionRow{}).Where("c_execution_id = ?", executionID).
			Updates(map[string]any{"c_acknowledged": true, "c_updated_at": now}).Error
	})
}

// ListUnacknowledgedFailures 返回失败且还没确认的执行记录，用于计算告警关注项。
func (s *Store) ListUnacknowledgedFailures(ctx context.Context) ([]Execution, error) {
	var rows []executionRow
	if err := s.db.WithContext(ctx).Where("c_status = ? AND c_acknowledged = 0", StatusFailed).Order("c_created_at ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	executions := make([]Execution, 0, len(rows))
	for _, row := range rows {
		executions = append(executions, executionFromRow(row))
	}
	return executions, nil
}

// PurgeBefore 删除 cutoff 之前创建且已结束的执行记录（连同尝试与下架步骤），以及 cutoff 之前的事件回执。
// 善后还没做成的已取消记录不删：用户还要靠它再取消一次。
func (s *Store) PurgeBefore(ctx context.Context, cutoff int64) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("c_created_at < ? AND c_status IN ? AND c_cleanup_pending = 0", cutoff, []string{StatusSucceeded, StatusFailed, StatusCanceled}).Delete(&executionRow{}).Error; err != nil {
			return err
		}
		return tx.Exec(`DELETE FROM t_event_receipts WHERE c_created_at < ?`, cutoff).Error
	})
}

// UpsertDelistAction 写入或更新下架处理的一步。
func (s *Store) UpsertDelistAction(ctx context.Context, action DelistAction) error {
	row := delistRow(action)
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{UpdateAll: true}).Create(&row).Error
}

// ListDelistActions 返回一条执行记录的全部下架步骤。
func (s *Store) ListDelistActions(ctx context.Context, executionID string) ([]DelistAction, error) {
	var rows []delistRow
	if err := s.db.WithContext(ctx).Where("c_execution_id = ?", executionID).Order("c_round ASC, c_kind ASC, c_space_id ASC, c_logical_account_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	actions := make([]DelistAction, 0, len(rows))
	for _, row := range rows {
		actions = append(actions, DelistAction(row))
	}
	return actions, nil
}
