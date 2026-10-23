package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// ParamSubmission 是写入方为一期提交的一个成员的快照。
type ParamSubmission struct {
	RequestID     string
	SetID         string
	MemberAlias   string
	PeriodTime    time.Time
	BasedOnPeriod time.Time
	Snapshot      domain.ParamSnapshot
}

// ParamSubmitResult 是一个成员提交的结果：accepted 带入库后的请求；too_late 与 rejected 带原因。
type ParamSubmitResult struct {
	MemberAlias string
	Result      string
	Reason      string
	Request     domain.ParamRequest
}

// 提交结果。
const (
	SubmitAccepted = "accepted"
	SubmitTooLate  = "too_late"
	SubmitRejected = "rejected"
)

// SubmitParams 接受写入方 writer 的一批参数请求（同一期、同一集合的若干成员）。每个成员独立判定：
// 写入方必须是集合的拥有者；成员必须存在且已启用；快照必须合法（声明的参数、范围、整数、联合约束、覆盖的标的在范围内）；
// 期次必须在网格上且晚于集合已冻结的期次。“已接受”不等于“已生效”，某期是否采用以冻结记录为准。
func (s *Service) SubmitParams(ctx context.Context, writer string, submissions []ParamSubmission) ([]ParamSubmitResult, error) {
	writer = strings.TrimSpace(writer)
	if writer == "" {
		return nil, errors.New("参数写入方不能为空")
	}
	results := make([]ParamSubmitResult, 0, len(submissions))
	sets := map[string]domain.FactorSet{}
	members := map[string]map[string]domain.SetMember{}
	for _, submission := range submissions {
		result := ParamSubmitResult{MemberAlias: submission.MemberAlias}
		set, ok := sets[submission.SetID]
		if !ok {
			loaded, err := s.db.GetSet(ctx, submission.SetID)
			if err != nil {
				return nil, fmt.Errorf("读取因子集 %s：%w", submission.SetID, err)
			}
			list, err := s.db.ListMembers(ctx, loaded.SetID, "")
			if err != nil {
				return nil, err
			}
			byAlias := make(map[string]domain.SetMember, len(list))
			for _, member := range list {
				byAlias[member.MemberAlias] = member
			}
			set, sets[submission.SetID], members[submission.SetID] = loaded, loaded, byAlias
		}
		reject := func(reason string) {
			result.Result, result.Reason = SubmitRejected, reason
			results = append(results, result)
		}
		if set.OwnerInstanceID == "" || set.OwnerInstanceID != writer {
			reject(fmt.Sprintf("%s 不是因子集 %s 的参数写入方", writer, set.SetID))
			continue
		}
		member, ok := members[set.SetID][submission.MemberAlias]
		if !ok {
			reject(fmt.Sprintf("因子集 %s 没有成员 %s", set.SetID, submission.MemberAlias))
			continue
		}
		if member.Status != domain.MemberStatusEnabled {
			reject(fmt.Sprintf("成员 %s 未启用", submission.MemberAlias))
			continue
		}
		if err := s.checkPeriod(set, submission.PeriodTime); err != nil {
			reject(err.Error())
			continue
		}
		if err := domain.CheckSnapshotNumbers(submission.Snapshot); err != nil {
			reject(err.Error())
			continue
		}
		if err := domain.ValidateSnapshot(member.Def(), submission.Snapshot, set.InScope); err != nil {
			reject(err.Error())
			continue
		}
		stored, err := s.db.SubmitParamRequest(ctx, domain.ParamRequest{
			RequestID: submission.RequestID, SetID: set.SetID, MemberAlias: submission.MemberAlias,
			PeriodTime: submission.PeriodTime.UTC(), BasedOnPeriod: submission.BasedOnPeriod, Snapshot: submission.Snapshot, Writer: writer,
		})
		switch {
		case store.IsTooLate(err):
			result.Result, result.Reason = SubmitTooLate, err.Error()
		case errors.Is(err, store.ErrConflict):
			result.Result, result.Reason = SubmitRejected, err.Error()
		case err != nil:
			return nil, err
		default:
			result.Result, result.Request = SubmitAccepted, stored
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) checkPeriod(set domain.FactorSet, period time.Time) error {
	if period.IsZero() {
		return errors.New("period_time 不能为空")
	}
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return err
	}
	aligned, err := clock.Align(period.UTC(), set.Freq)
	if err != nil {
		return err
	}
	if !aligned.Equal(period.UTC()) {
		return fmt.Errorf("period_time %s 不在 %s 的周期网格上", period.UTC().Format(time.RFC3339), set.Freq)
	}
	return nil
}

// previousPeriod 返回网格上的上一期。
func previousPeriod(set domain.FactorSet, period time.Time) (time.Time, error) {
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return time.Time{}, err
	}
	duration, err := clock.Duration(set.Freq)
	if err != nil {
		return time.Time{}, err
	}
	return period.Add(-duration), nil
}

// FreezeParams 冻结一组集合在 period 这一期的参数（引擎开算之前调用）：每个集合的每个启用成员各一份冻结记录。
// 同一期重复调用返回同一份结果。
func (s *Service) FreezeParams(ctx context.Context, setIDs []string, period time.Time) (map[string][]domain.ParamFreeze, error) {
	out := make(map[string][]domain.ParamFreeze, len(setIDs))
	for _, setID := range setIDs {
		set, err := s.db.GetSet(ctx, setID)
		if err != nil {
			return nil, fmt.Errorf("读取因子集 %s：%w", setID, err)
		}
		if err := s.checkPeriod(set, period); err != nil {
			return nil, err
		}
		previous, err := previousPeriod(set, period.UTC())
		if err != nil {
			return nil, err
		}
		members, err := s.db.ListMembers(ctx, set.SetID, domain.MemberStatusEnabled)
		if err != nil {
			return nil, err
		}
		freezeMembers := make([]store.FreezeMember, 0, len(members))
		for _, member := range members {
			freezeMembers = append(freezeMembers, store.FreezeMember{Alias: member.MemberAlias, Defaults: domain.DefaultSnapshot(member.Factor)})
		}
		freezes, err := s.db.FreezeParams(ctx, set.SetID, period.UTC(), previous, freezeMembers)
		if err != nil {
			return nil, fmt.Errorf("冻结因子集 %s 的参数：%w", setID, err)
		}
		out[set.SetID] = freezes
	}
	return out, nil
}

// ApplyOfflineFreezes 合并引擎离线冻结的期次，返回管理端已有不同记录的期次（引擎据此重算）。
func (s *Service) ApplyOfflineFreezes(ctx context.Context, freezes []domain.ParamFreeze) ([]domain.ParamFreeze, error) {
	sets := map[string]domain.FactorSet{}
	gone := map[string]bool{}
	for _, freeze := range freezes {
		if _, ok := sets[freeze.SetID]; ok || gone[freeze.SetID] {
			continue
		}
		set, err := s.db.GetSet(ctx, freeze.SetID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 补报前集合已被删除或释放：这些期次不会再有人用，丢弃，不能让它们卡住其他集合的补报。
			gone[freeze.SetID] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		sets[freeze.SetID] = set
	}
	if len(gone) > 0 {
		kept := make([]domain.ParamFreeze, 0, len(freezes))
		for _, freeze := range freezes {
			if !gone[freeze.SetID] {
				kept = append(kept, freeze)
			}
		}
		freezes = kept
	}
	var previousErr error
	conflicts, err := s.db.ApplyOfflineFreezes(ctx, freezes, func(period time.Time) time.Time {
		for _, freeze := range freezes {
			if freeze.PeriodTime.Equal(period) {
				previous, err := previousPeriod(sets[freeze.SetID], period)
				if err != nil {
					previousErr = err
				}
				return previous
			}
		}
		return period.Add(-time.Second)
	})
	if err != nil {
		return nil, err
	}
	return conflicts, previousErr
}

// ListParamIntervals 返回冻结区间（补算与回放读取已应用参数）。
func (s *Service) ListParamIntervals(ctx context.Context, setID, alias string, from, to time.Time) ([]domain.ParamInterval, error) {
	return s.db.ListParamIntervals(ctx, setID, alias, from, to)
}

// ListParamRequests 返回参数请求（审计与排查）。
func (s *Service) ListParamRequests(ctx context.Context, setID, alias string, from, to time.Time, limit int) ([]domain.ParamRequest, error) {
	return s.db.ListParamRequests(ctx, setID, alias, from, to, limit)
}

// ParamRequestRetention 是参数请求原文的保留期。
const ParamRequestRetention = 30 * 24 * time.Hour

// CleanupParams 删除过期的参数请求、各集合结果数据集保留期（再加一个最大回看）之外的冻结区间与不再被引用的快照。
// 保留期取不到或无限的集合不删冻结区间。
func (s *Service) CleanupParams(ctx context.Context) (int64, error) {
	now := s.now().UTC()
	sets, err := s.db.ListSets(ctx)
	if err != nil {
		return 0, err
	}
	before := map[string]time.Time{}
	for _, set := range sets {
		cutoff, ok := s.freezeCutoff(ctx, set, now)
		if ok {
			before[set.SetID] = cutoff
		}
	}
	return s.db.CleanupParams(ctx, now.Add(-ParamRequestRetention), before)
}

// FrozenThrough 返回集合已冻结到的最新一期。
func (s *Service) FrozenThrough(ctx context.Context, setID string) (time.Time, bool, error) {
	return s.db.FrozenThrough(ctx, setID)
}
