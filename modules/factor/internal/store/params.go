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
//  3. 按标的合并（domain.MergeSnapshots）后，交给 check 校验合并结果；通过就写入 c_last_submitted_*。
//
// 提交只写运行状态列，不更新 c_mtime，也就不改变目录指纹。成员不存在、或不是可变成员时该成员被拒绝，不影响其他成员。
func (s *Store) SubmitMemberParams(ctx context.Context, taskID string, period int64, requestID string, submissions []ParamSubmission,
	check func(memberKey string, merged domain.ParamSnapshot) error) ([]ParamSubmitOutcome, error) {
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
			outcome, err := submitOne(tx, taskID, period, requestID, submission, check)
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

func submitOne(tx *gorm.DB, taskID string, period int64, requestID string, submission ParamSubmission,
	check func(string, domain.ParamSnapshot) error) (ParamSubmitOutcome, error) {
	outcome := ParamSubmitOutcome{MemberKey: submission.MemberKey}
	reject := func(format string, args ...any) (ParamSubmitOutcome, error) {
		outcome.Result, outcome.Reason = SubmitRejected, fmt.Sprintf(format, args...)
		return outcome, nil
	}
	var row memberRow
	result := tx.Raw(`SELECT `+memberColumns+` FROM t_factor_task_members WHERE c_task_id = ? AND c_member_key = ?`,
		taskID, submission.MemberKey).Scan(&row)
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
	case period < row.LastSubmittedPeriod:
		return reject("目标期 %s 早于已接受的目标期 %s", formatPeriod(period), formatPeriod(row.LastSubmittedPeriod))
	case period == row.LastSubmittedPeriod && row.LastRequestID == requestID:
		outcome.Result = SubmitDuplicate
		return outcome, nil
	case period == row.LastSubmittedPeriod:
		return reject("目标期 %s 已被另一次提交（request_id %s）接受", formatPeriod(period), row.LastRequestID)
	}
	previous, err := domain.ParseParamSnapshot(row.LastSubmittedParams)
	if err != nil {
		return outcome, err
	}
	merged := domain.MergeSnapshots(previous, submission.Snapshot)
	if check != nil {
		if err := check(submission.MemberKey, merged); err != nil {
			return reject("%v", err)
		}
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return outcome, err
	}
	// 只写 c_last_* 运行状态列：触发器按配置列限定，不会更新 c_mtime。
	if err := tx.Exec(`
		UPDATE t_factor_task_members
		SET c_last_submitted_params = ?, c_last_submitted_period = ?, c_last_request_id = ?, c_last_submitted_at = ?
		WHERE c_task_id = ? AND c_member_key = ?
	`, string(encoded), period, requestID, time.Now().UTC(), taskID, submission.MemberKey).Error; err != nil {
		return outcome, err
	}
	outcome.Result = SubmitAccepted
	return outcome, nil
}

func formatPeriod(period int64) string { return time.Unix(period, 0).UTC().Format(time.RFC3339) }

// ListMemberParamStates 返回任务里各可变成员最近一次被接受的提交（没有提交过的成员也在内，LastSubmittedPeriod 为 0），
// 按成员键排序。
func (s *Store) ListMemberParamStates(ctx context.Context, taskID string) ([]domain.FactorTaskMember, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []memberRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT `+memberColumns+` FROM t_factor_task_members WHERE c_task_id = ? AND c_kind = ? ORDER BY c_member_key
	`, strings.TrimSpace(taskID), domain.MemberKindVar).Scan(&rows).Error; err != nil {
		return nil, err
	}
	members := make([]domain.FactorTaskMember, 0, len(rows))
	for _, row := range rows {
		member, err := row.domain()
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}
