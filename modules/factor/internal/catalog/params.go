package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// SubjectSource 列出源数据集当前的标的：参数提交里按标的覆盖的标的必须在其中。
type SubjectSource interface {
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

// WithSubjectSource 设置参数提交时校验覆盖标的用的标的来源；不设置则不校验标的范围。
func WithSubjectSource(source SubjectSource) Option { return func(s *Service) { s.subjects = source } }

// MemberSubmission 是一个可变成员的一份参数提交（完整快照：默认值加按标的覆盖）。
type MemberSubmission struct {
	MemberKey string
	Snapshot  domain.ParamSnapshot
}

// SubmitParams 接受关联实例 writer 为任务的可变成员提交的参数（设计 6.2）。period 是参数从哪一期起生效的目标期。
// 整体先判定：写入方必须是任务关联的实例；目标期必须在网格上。之后每个成员独立判定并返回各自的结果：
// 成员必须是已启用的可变成员；快照必须合法（声明的参数、范围、整数、联合约束，覆盖的标的在源数据集里）；
// 目标期不能倒退；同期同 request_id 幂等，同期不同 request_id 拒绝；按标的合并后的结果再校验一次。
func (s *Service) SubmitParams(ctx context.Context, writer, taskID string, period time.Time, requestID string,
	submissions []MemberSubmission) ([]store.ParamSubmitOutcome, error) {
	writer, taskID, requestID = strings.TrimSpace(writer), strings.TrimSpace(taskID), strings.TrimSpace(requestID)
	if writer == "" {
		return nil, errors.New("参数写入方不能为空")
	}
	if requestID == "" {
		return nil, errors.New("request_id 不能为空")
	}
	task, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("读取因子任务 %s：%w", taskID, err)
	}
	if task.BoundInstanceID == "" || task.BoundInstanceID != writer {
		return nil, fmt.Errorf("%s 不是因子任务 %s 关联的策略实例，不能提交参数", writer, taskID)
	}
	if err := s.checkPeriod(task, period); err != nil {
		return nil, err
	}
	members, err := s.db.ListMembers(ctx, taskID, "")
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]domain.TaskMember, len(members))
	for _, member := range members {
		byKey[member.MemberKey] = member
	}
	inScope, err := s.subjectScope(ctx, task)
	if err != nil {
		return nil, err
	}
	// 先在存储之外挡掉成员不存在、未启用和快照本身不合法的提交，其余交给存储在事务里判定期次、幂等和合并。
	outcomes := make(map[string]store.ParamSubmitOutcome, len(submissions))
	var accepted []store.ParamSubmission
	for _, submission := range submissions {
		reject := func(reason string) {
			outcomes[submission.MemberKey] = store.ParamSubmitOutcome{MemberKey: submission.MemberKey, Result: store.SubmitRejected, Reason: reason}
		}
		member, ok := byKey[submission.MemberKey]
		switch {
		case !ok:
			reject(fmt.Sprintf("因子任务 %s 没有成员 %s", taskID, submission.MemberKey))
			continue
		case member.Kind != domain.MemberKindVar:
			reject(fmt.Sprintf("成员 %s 是固定成员，不接受参数提交", submission.MemberKey))
			continue
		case member.Status != domain.MemberStatusEnabled:
			reject(fmt.Sprintf("成员 %s 未启用", submission.MemberKey))
			continue
		}
		if err := domain.CheckSnapshotNumbers(submission.Snapshot); err != nil {
			reject(err.Error())
			continue
		}
		if err := domain.ValidateSnapshot(member.Def(), submission.Snapshot, inScope); err != nil {
			reject(err.Error())
			continue
		}
		accepted = append(accepted, store.ParamSubmission{MemberKey: submission.MemberKey, Snapshot: submission.Snapshot})
	}
	if len(accepted) > 0 {
		stored, err := s.db.SubmitMemberParams(ctx, taskID, period.UTC().Unix(), requestID, accepted,
			func(memberKey string, merged domain.ParamSnapshot) error {
				// 合并后保留下来的旧覆盖和本次的默认值可能组合出不合法的取值，所以对合并结果再校验一次。
				return domain.ValidateSnapshot(byKey[memberKey].Def(), merged, inScope)
			})
		if err != nil {
			return nil, err
		}
		for _, outcome := range stored {
			outcomes[outcome.MemberKey] = outcome
		}
	}
	results := make([]store.ParamSubmitOutcome, 0, len(submissions))
	for _, submission := range submissions {
		results = append(results, outcomes[submission.MemberKey])
	}
	return results, nil
}

// subjectScope 返回覆盖标的是否在源数据集里的判断函数；没有设置标的来源时返回 nil（不校验）。
func (s *Service) subjectScope(ctx context.Context, task domain.FactorTask) (func(string) bool, error) {
	if s.subjects == nil {
		return nil, nil
	}
	list, err := s.subjects.ListDatasetSubjects(ctx, task.SpaceID, task.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("读取源数据集 %s 的标的：%w", task.SourceDatasetID, err)
	}
	known := make(map[string]struct{}, len(list))
	for _, subject := range list {
		known[subject] = struct{}{}
	}
	return func(subject string) bool { _, ok := known[subject]; return ok }, nil
}

// GetParams 只读：返回任务里各可变成员最近一次被接受的提交（没有提交过的成员也在内，LastSubmittedPeriod 为 0）。
func (s *Service) GetParams(ctx context.Context, taskID string) ([]domain.FactorTaskMember, error) {
	taskID = strings.TrimSpace(taskID)
	if _, err := s.db.GetTask(ctx, taskID); err != nil {
		return nil, fmt.Errorf("读取因子任务 %s：%w", taskID, err)
	}
	return s.db.ListMemberParamStates(ctx, taskID)
}

func (s *Service) checkPeriod(task domain.FactorTask, period time.Time) error {
	if period.IsZero() {
		return errors.New("period_time 不能为空")
	}
	clock, err := periodclock.ForSpace(task.SpaceID)
	if err != nil {
		return err
	}
	aligned, err := clock.Align(period.UTC(), task.Freq)
	if err != nil {
		return err
	}
	if !aligned.Equal(period.UTC()) {
		return fmt.Errorf("period_time %s 不在 %s 的周期网格上", period.UTC().Format(time.RFC3339), task.Freq)
	}
	return nil
}
