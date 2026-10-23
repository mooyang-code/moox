package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 参数提交状态。
const (
	ParamPending   = "pending"
	ParamAccepted  = "accepted"
	ParamTooLate   = "too_late"
	ParamRejected  = "rejected"
	ParamAbandoned = "abandoned"
)

// ParamSubmission 是策略每期求出的下一期因子参数：与产出它的结果在同一个事务里落库，之后异步提交。
type ParamSubmission struct {
	RequestID     string
	InstanceID    string
	SessionID     string
	ResultID      string
	SetID         string
	PeriodTime    time.Time
	BasedOnPeriod time.Time
	// ParamsJSON 是各成员的完整快照：{别名: {参数名: {default, overrides}}}。
	ParamsJSON json.RawMessage
	// Notes 记录求值时的说明（求值失败的成员、被限幅的参数）。
	Notes       string
	Status      string
	Attempts    int
	NextAttempt time.Time
	// OutcomeJSON 是终态的逐成员结果：[{member, result, reason}]。
	OutcomeJSON json.RawMessage
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type paramSubmissionRow struct {
	RequestID     string    `gorm:"column:c_request_id"`
	InstanceID    string    `gorm:"column:c_instance_id"`
	SessionID     string    `gorm:"column:c_session_id"`
	ResultID      string    `gorm:"column:c_result_id"`
	SetID         string    `gorm:"column:c_set_id"`
	PeriodTime    int64     `gorm:"column:c_period_time"`
	BasedOnPeriod int64     `gorm:"column:c_based_on_period"`
	ParamsJSON    string    `gorm:"column:c_params_json"`
	Notes         string    `gorm:"column:c_notes"`
	Status        string    `gorm:"column:c_status"`
	Attempts      int       `gorm:"column:c_attempts"`
	NextAttempt   time.Time `gorm:"column:c_next_attempt"`
	OutcomeJSON   string    `gorm:"column:c_outcome_json"`
	CreatedAt     time.Time `gorm:"column:c_ctime"`
	UpdatedAt     time.Time `gorm:"column:c_mtime"`
}

const paramSubmissionColumns = "c_request_id, c_instance_id, c_session_id, c_result_id, c_set_id, c_period_time, c_based_on_period, c_params_json, c_notes, c_status, c_attempts, c_next_attempt, c_outcome_json, c_ctime, c_mtime"

func (r paramSubmissionRow) submission() ParamSubmission {
	return ParamSubmission{
		RequestID: r.RequestID, InstanceID: r.InstanceID, SessionID: r.SessionID, ResultID: r.ResultID, SetID: r.SetID,
		PeriodTime: fromMillis(r.PeriodTime), BasedOnPeriod: fromMillis(r.BasedOnPeriod), ParamsJSON: json.RawMessage(r.ParamsJSON),
		Notes: r.Notes, Status: r.Status, Attempts: r.Attempts, NextAttempt: r.NextAttempt.UTC(), OutcomeJSON: json.RawMessage(r.OutcomeJSON),
		CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(),
	}
}

func validateParamSubmission(submission ParamSubmission) error {
	if strings.TrimSpace(submission.RequestID) == "" || strings.TrimSpace(submission.SetID) == "" {
		return errors.New("参数提交缺少 request_id 或 set_id")
	}
	if submission.PeriodTime.IsZero() {
		return errors.New("参数提交缺少目标期次")
	}
	var members map[string]map[string]json.RawMessage
	if err := json.Unmarshal(submission.ParamsJSON, &members); err != nil || len(members) == 0 {
		return errors.New("参数提交的 params_json 必须是非空的成员快照对象")
	}
	return nil
}

// insertParamSubmission 在结果提交的事务里写入待提交的参数。
func insertParamSubmission(tx *gorm.DB, result Result, submission ParamSubmission, now time.Time) error {
	if err := validateParamSubmission(submission); err != nil {
		return err
	}
	return tx.Exec(`
		INSERT INTO t_strategy_param_submissions (`+paramSubmissionColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, '[]', ?, ?)
	`, submission.RequestID, result.InstanceID, result.SessionID, result.ResultID, submission.SetID, millis(submission.PeriodTime),
		millis(submission.BasedOnPeriod), string(submission.ParamsJSON), submission.Notes, now, now, now).Error
}

// DuePendingParamSubmissions 返回到了重试时间的待提交参数（按目标期次升序，最多 limit 条）。
// 只返回实例仍启用、会话仍是当前会话的记录：实例停用或换了会话后，旧参数不再有意义。
func (s *Store) DuePendingParamSubmissions(ctx context.Context, now time.Time, limit int) ([]ParamSubmission, error) {
	if limit <= 0 {
		limit = 20
	}
	var rows []paramSubmissionRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT `+paramSubmissionColumns+` FROM t_strategy_param_submissions
		WHERE c_status = 'pending' AND c_next_attempt <= ?
		ORDER BY c_period_time, c_request_id LIMIT ?
	`, now.UTC(), limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ParamSubmission, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.submission())
	}
	return out, nil
}

// FinishParamSubmission 记录终态（accepted、too_late、rejected、abandoned）与逐成员结果。
func (s *Store) FinishParamSubmission(ctx context.Context, requestID, status string, outcome json.RawMessage, at time.Time) error {
	switch status {
	case ParamAccepted, ParamTooLate, ParamRejected, ParamAbandoned:
	default:
		return fmt.Errorf("参数提交的终态 %q 无效", status)
	}
	if len(outcome) == 0 {
		outcome = json.RawMessage(`[]`)
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_param_submissions SET c_status = ?, c_outcome_json = ?, c_attempts = c_attempts + 1, c_mtime = ?
		WHERE c_request_id = ? AND c_status = 'pending'
	`, status, string(outcome), at.UTC(), requestID)
	return result.Error
}

// RetryParamSubmission 记一次失败的尝试并推迟下次重试。
func (s *Store) RetryParamSubmission(ctx context.Context, requestID string, next, at time.Time) error {
	return s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_param_submissions SET c_attempts = c_attempts + 1, c_next_attempt = ?, c_mtime = ?
		WHERE c_request_id = ? AND c_status = 'pending'
	`, next.UTC(), at.UTC(), requestID).Error
}

// DeferParamSubmission 把一条待提交参数推迟到 next 再看，不计一次尝试（模块或实例暂停时用）。
func (s *Store) DeferParamSubmission(ctx context.Context, requestID string, next, at time.Time) error {
	return s.db.WithContext(ctx).Exec(`
		UPDATE t_strategy_param_submissions SET c_next_attempt = ?, c_mtime = ?
		WHERE c_request_id = ? AND c_status = 'pending'
	`, next.UTC(), at.UTC(), requestID).Error
}

// ListParamSubmissions 返回实例最近的参数提交记录（按目标期次倒序）。
func (s *Store) ListParamSubmissions(ctx context.Context, instanceID string, limit int) ([]ParamSubmission, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []paramSubmissionRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT `+paramSubmissionColumns+` FROM t_strategy_param_submissions
		WHERE c_instance_id = ? ORDER BY c_period_time DESC, c_request_id LIMIT ?
	`, instanceID, limit).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ParamSubmission, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.submission())
	}
	return out, nil
}

// PendingParamSubmissionCount 返回待提交的参数数（指标与排查）。
func (s *Store) PendingParamSubmissionCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM t_strategy_param_submissions WHERE c_status = 'pending'`).Scan(&count).Error
	return count, err
}
