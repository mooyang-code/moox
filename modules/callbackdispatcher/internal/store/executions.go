package store

import (
	"context"
	"errors"
	"sort"

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

// ClaimDue 取出到期的待处理记录并置为执行中，不限制每条规则的数量。systemActions 是已经注册了处理器的系统动作：
// 没有处理器的系统动作记录不取，免得每轮都到期却没人处理。
func (s *Store) ClaimDue(ctx context.Context, now int64, limit int, systemActions []string) ([]Execution, error) {
	return s.ClaimDueLimited(ctx, now, limit, systemActions, 0, nil)
}

// ClaimDueLimited 与 ClaimDue 相同，但每条规则这一轮最多取 perRule 条（0 表示不限）；remaining 里列出的规则按它的剩余名额取
// （0 就是这一轮不取）。在 SQL 里按规则限额，一条规则积压再多，也不会占满每一轮的领取名额，别的规则照常被领到。
func (s *Store) ClaimDueLimited(ctx context.Context, now int64, limit int, systemActions []string, perRule int, remaining map[string]int) ([]Execution, error) {
	var claimed []Execution
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		where := "e.c_status = ? AND e.c_next_run_at > 0 AND e.c_next_run_at <= ? AND "
		args := []any{StatusPending, now}
		if len(systemActions) == 0 {
			where += "e.c_action_type = ?"
			args = append(args, ActionNotifyURL)
		} else {
			where += "(e.c_action_type = ? OR r.c_system_action IN ?)"
			args = append(args, ActionNotifyURL, systemActions)
		}
		outer := ""
		if perRule > 0 {
			outer = " WHERE rn <= CASE c_rule_id"
			var ruleIDs []string
			for rule := range remaining {
				ruleIDs = append(ruleIDs, rule)
			}
			sort.Strings(ruleIDs)
			var caseArgs []any
			for _, rule := range ruleIDs {
				outer += " WHEN ? THEN ?"
				caseArgs = append(caseArgs, rule, remaining[rule])
			}
			if len(ruleIDs) == 0 {
				outer = " WHERE rn <= ?"
			} else {
				outer += " ELSE ? END"
			}
			caseArgs = append(caseArgs, perRule)
			args = append(args, caseArgs...)
		}
		query := `SELECT * FROM (
    SELECT e.*, ROW_NUMBER() OVER (PARTITION BY e.c_rule_id ORDER BY e.c_next_run_at ASC, e.c_execution_id ASC) AS rn
    FROM t_event_executions AS e
    LEFT JOIN t_event_rules AS r ON r.c_rule_id = e.c_rule_id
    WHERE ` + where + `
)` + outer + ` ORDER BY c_next_run_at ASC, c_execution_id ASC LIMIT ?`
		args = append(args, limit)
		var rows []executionRow
		if err := tx.Raw(query, args...).Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			// 领取时间同时是凭证，必须和这条记录上一次的不同。
			token := max(now, row.ClaimedAt+1)
			if err := tx.Model(&executionRow{}).Where("c_execution_id = ?", row.ExecutionID).
				Updates(map[string]any{"c_status": StatusRunning, "c_claimed_at": token, "c_updated_at": now}).Error; err != nil {
				return err
			}
			row.Status, row.ClaimedAt = StatusRunning, token
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

// RecoverStaleRunning 把领取时间早于 staleBefore（Unix 毫秒）、还停在“执行中”的记录放回待处理：
// 处理它的线程卡死或丢失结果时，记录不会永远停在执行中。返回放回的条数。
func (s *Store) RecoverStaleRunning(ctx context.Context, staleBefore, now int64) (int64, error) {
	result := s.db.WithContext(ctx).Model(&executionRow{}).Where("c_status = ? AND c_claimed_at < ?", StatusRunning, staleBefore).
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

// SaveResult 写回处理结果。只有记录仍是执行中、并且领取凭证 claimedAt 还对得上才写：处理期间被取消的记录保持已取消，
// 卡住后被放回又被重新领取的记录，旧线程迟到的结果不能覆盖新线程的；返回是否写入。
// 处理期间事件内容被合并更新过（见 Result.ProcessedPayload）时，记录改为待处理并立即到期，其余计数照常写入。
func (s *Store) SaveResult(ctx context.Context, executionID string, claimedAt int64, result Result, now int64) (bool, error) {
	applied := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current executionRow
		if err := tx.Where("c_execution_id = ? AND c_status = ? AND c_claimed_at = ?", executionID, StatusRunning, claimedAt).Take(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if result.ProcessedPayload != "" && current.PayloadJSON != result.ProcessedPayload && result.Status != StatusCanceled {
			result.Status, result.NextRunAt, result.FinishedAt = StatusPending, now, 0
		}
		update := tx.Model(&executionRow{}).Where("c_execution_id = ? AND c_status = ? AND c_claimed_at = ?", executionID, StatusRunning, claimedAt).
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

// SetCleanupError 记下取消后的善后失败的原因。只在善后仍未完成时才写：两次取消交错、其中一次已经做完善后时，
// 迟到的失败说明不能盖住“已做完”的状态（页面会显示失败却没有“重试善后”按钮）。
func (s *Store) SetCleanupError(ctx context.Context, executionID, message string, now int64) error {
	return s.db.WithContext(ctx).Model(&executionRow{}).Where("c_execution_id = ? AND c_cleanup_pending = 1", executionID).
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

// MarkCleanupPendingIfCanceled 在记录已是已取消时，把“善后未完成”标记重新置 1 并写失败说明；记录不是已取消就什么也不做。
// 处理器在 Step 写名单后复查发现已取消、补解除又失败时用它：这时记录不是执行中，SaveResult 会丢掉结果，
// 只有靠它用户才能看到“重试善后”，关注项也才会提醒。返回是否写入。
func (s *Store) MarkCleanupPendingIfCanceled(ctx context.Context, executionID, message string, now int64) (bool, error) {
	result := s.db.WithContext(ctx).Model(&executionRow{}).Where("c_execution_id = ? AND c_status = ?", executionID, StatusCanceled).
		Updates(map[string]any{"c_cleanup_pending": true, "c_last_error": message, "c_updated_at": now})
	return result.RowsAffected > 0, result.Error
}

// countNonCanceledAllBySubject 统计同一标的上除自己以外、范围不是 PAPER_ONLY（真实下架）、没有被取消的系统动作记录，已结束的也算：
// 真实下架结束后（成功或失败）禁止名单按设计保留，演练的结束或取消不能把它解除。
const countNonCanceledAllBySubject = `c_action_type = 'SYSTEM' AND c_subject_id = ? AND c_status <> 'CANCELED' AND c_execution_id <> ?
AND COALESCE(json_extract(c_payload_json, '$.scope'), '') <> 'PAPER_ONLY'`

// CountNonCanceledAllBySubject 见 countNonCanceledAllBySubject。subjectID 是统一后的标的名（交易所:市场:代码）。
func (s *Store) CountNonCanceledAllBySubject(ctx context.Context, subjectID, exceptExecutionID string) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&executionRow{}).Where(countNonCanceledAllBySubject, subjectID, exceptExecutionID).Count(&count).Error
	return count, err
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

// listUnfinishedWithFailedSteps 查还没结束、但已有下架步骤失败的系统动作记录。整条记录的成败只在下架后核对时决定，
// 某个组合的某一轮平仓用完重试不会让记录失败（后面的轮次还要继续），所以单独靠它提醒用户。
const listUnfinishedWithFailedSteps = `SELECT * FROM t_event_executions AS e
WHERE e.c_action_type = 'SYSTEM' AND e.c_status IN ('PENDING', 'RUNNING')
AND EXISTS (
    SELECT 1 FROM t_delist_actions AS a
    WHERE a.c_execution_id = e.c_execution_id AND a.c_status IN ('FAILED', 'PARTIAL')
)
ORDER BY e.c_created_at ASC`

// ListUnfinishedWithFailedSteps 返回还没结束、但已有下架步骤失败（用完重试、已到下架时间仍没平完，或平仓只完成了一部分）的系统动作记录，用于计算告警关注项。
func (s *Store) ListUnfinishedWithFailedSteps(ctx context.Context) ([]Execution, error) {
	var rows []executionRow
	if err := s.db.WithContext(ctx).Raw(listUnfinishedWithFailedSteps).Scan(&rows).Error; err != nil {
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
