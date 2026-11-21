package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// 参数提交结果。
const (
	SubmitAccepted  = "accepted"
	SubmitDuplicate = "duplicate"
	SubmitRejected  = "rejected"
)

// ParamSubmission 是一个可变成员的一份参数提交（完整快照：默认值加按标的覆盖）。
type ParamSubmission struct {
	MemberKey string
	Snapshot  domain.ParamSnapshot
}

// ParamSubmitOutcome 是一个成员的提交结果；Result 为 rejected 时 Reason 说明原因。
type ParamSubmitOutcome struct {
	MemberKey string
	Result    string
	Reason    string
}

// SubmitMemberParams 在一个事务里处理一次提交（每个成员一份）。对每个成员：
//  1. 目标期不能倒退：小于该成员最近一次被接受的目标期则拒绝；
//  2. 同一目标期下，同一个 requestID 重复提交按幂等成功，不同 requestID 拒绝；
//  3. 本次提交是完整快照（调用方已校验），整份替换上一期写入 t_factor_member_params：没带覆盖的标的用默认值，与现算回放一致。
//
// 提交不碰成员行，也就不改变目录指纹。成员不存在、或不是可变成员时该成员被拒绝，不影响其他成员。
func (s *Store) SubmitMemberParams(ctx context.Context, taskID string, period int64, requestID string, submissions []ParamSubmission) ([]ParamSubmitOutcome, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	taskID, requestID = strings.TrimSpace(taskID), strings.TrimSpace(requestID)
	if taskID == "" || requestID == "" || period <= 0 {
		return nil, fmt.Errorf("task id, request id and a positive period are required")
	}
	outcomes := make([]ParamSubmitOutcome, 0, len(submissions))
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		outcomes = outcomes[:0]
		for _, submission := range submissions {
			outcome, err := submitOne(tx, taskID, period, requestID, submission)
			if err != nil {
				return err
			}
			outcomes = append(outcomes, outcome)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return outcomes, nil
}

func submitOne(tx *gorm.DB, taskID string, period int64, requestID string, submission ParamSubmission) (ParamSubmitOutcome, error) {
	outcome := ParamSubmitOutcome{MemberKey: submission.MemberKey}
	reject := func(format string, args ...any) (ParamSubmitOutcome, error) {
		outcome.Result, outcome.Reason = SubmitRejected, fmt.Sprintf(format, args...)
		return outcome, nil
	}
	// 没有提交过的成员在参数表里没有行，按目标期 0、空参数处理。
	var row memberParamRow
	result := tx.Raw(`
		SELECT `+memberParamColumns+`
		FROM t_factor_task_members m
		LEFT JOIN t_factor_member_params p ON p.c_task_id = m.c_task_id AND p.c_member_key = m.c_member_key
		WHERE m.c_task_id = ? AND m.c_member_key = ?
	`, taskID, submission.MemberKey).Scan(&row)
	if result.Error != nil {
		return outcome, result.Error
	}
	if result.RowsAffected == 0 {
		return reject("任务 %s 里没有成员 %s", taskID, submission.MemberKey)
	}
	if row.Kind != domain.MemberKindVar {
		return reject("成员 %s 是固定成员，不接受参数提交", submission.MemberKey)
	}
	switch {
	case period < row.Period:
		return reject("目标期 %s 早于已接受的目标期 %s", formatPeriod(period), formatPeriod(row.Period))
	case period == row.Period && row.RequestID == requestID:
		outcome.Result = SubmitDuplicate
		return outcome, nil
	case period == row.Period:
		return reject("目标期 %s 已被另一次提交（request_id %s）接受", formatPeriod(period), row.RequestID)
	}
	encoded, err := json.Marshal(submission.Snapshot)
	if err != nil {
		return outcome, err
	}
	if err := tx.Exec(`
		INSERT INTO t_factor_member_params (c_task_id, c_member_key, c_params, c_period, c_request_id, c_submitted_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (c_task_id, c_member_key) DO UPDATE SET
			c_params = excluded.c_params, c_period = excluded.c_period,
			c_request_id = excluded.c_request_id, c_submitted_at = excluded.c_submitted_at
	`, taskID, submission.MemberKey, string(encoded), period, requestID, time.Now().UTC()).Error; err != nil {
		return outcome, err
	}
	outcome.Result = SubmitAccepted
	return outcome, nil
}

func formatPeriod(period int64) string { return time.Unix(period, 0).UTC().Format(time.RFC3339) }

// memberParamRow 是成员行左连参数表的结果：没有提交过的成员参数列为空。
type memberParamRow struct {
	MemberKey   string     `gorm:"column:c_member_key"`
	Kind        string     `gorm:"column:c_kind"`
	Params      string     `gorm:"column:c_params"`
	Period      int64      `gorm:"column:c_period"`
	RequestID   string     `gorm:"column:c_request_id"`
	SubmittedAt *time.Time `gorm:"column:c_submitted_at"`
}

const memberParamColumns = `m.c_member_key, m.c_kind, COALESCE(p.c_params, '{}') AS c_params, COALESCE(p.c_period, 0) AS c_period,
	COALESCE(p.c_request_id, '') AS c_request_id, p.c_submitted_at`

// ListMemberParamStates 返回任务里各可变成员最近一次被接受的提交（没有提交过的成员也在内，LastSubmittedPeriod 为 0），
// 按成员键排序。
func (s *Store) ListMemberParamStates(ctx context.Context, taskID string) ([]domain.MemberParamState, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []memberParamRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT `+memberParamColumns+`
		FROM t_factor_task_members m
		LEFT JOIN t_factor_member_params p ON p.c_task_id = m.c_task_id AND p.c_member_key = m.c_member_key
		WHERE m.c_task_id = ? AND m.c_kind = ?
		ORDER BY m.c_member_key
	`, strings.TrimSpace(taskID), domain.MemberKindVar).Scan(&rows).Error; err != nil {
		return nil, err
	}
	states := make([]domain.MemberParamState, 0, len(rows))
	for _, row := range rows {
		params, err := domain.ParseParamSnapshot(row.Params)
		if err != nil {
			return nil, fmt.Errorf("member %s/%s: %w", taskID, row.MemberKey, err)
		}
		if len(params) == 0 {
			params = nil
		}
		state := domain.MemberParamState{MemberKey: row.MemberKey, Params: params, LastSubmittedPeriod: row.Period, LastRequestID: row.RequestID}
		if row.SubmittedAt != nil {
			state.LastSubmittedAt = *row.SubmittedAt
		}
		states = append(states, state)
	}
	return states, nil
}
