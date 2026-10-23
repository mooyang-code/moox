package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

// InstanceMember 是策略定义声明的一个因子成员：别名与定义 ID。
type InstanceMember struct {
	Alias    string
	FactorID string
}

// InstanceSetRequest 描述策略实例的私有因子集（因子动态调参执行计划 2.3）：源数据集、频率、标的范围与声明的成员。
type InstanceSetRequest struct {
	InstanceID      string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	SubjectMode     string
	Subjects        []string
	Members         []InstanceMember
}

// InstanceSet 是 EnsureInstanceSet 的结果：集合、成员与这次启用提交的回填任务。
type InstanceSet struct {
	Set          domain.FactorSet
	Members      []domain.SetMember
	BackfillJobs []string
}

// EnsureInstanceSet 建立或对齐策略实例的私有因子集，可重复调用：集合不存在时创建（实例是唯一的参数写入方），
// 已存在时核对源数据集与频率；声明的成员都加入并启用（启用会补齐结果列并提交回填，同一集合排队中的回填合并成一个），
// 不再声明的成员停用并移出（已写入的列与值保留）；集合处于停用时重新启用。
func (s *Service) EnsureInstanceSet(ctx context.Context, request InstanceSetRequest) (InstanceSet, error) {
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	if request.InstanceID == "" {
		return InstanceSet{}, errors.New("instance_id is required")
	}
	if len(request.Members) == 0 {
		return InstanceSet{}, errors.New("私有因子集至少需要一个成员")
	}
	declared := make(map[string]InstanceMember, len(request.Members))
	for _, member := range request.Members {
		member.Alias, member.FactorID = strings.TrimSpace(member.Alias), strings.TrimSpace(member.FactorID)
		if member.Alias == "" {
			member.Alias = member.FactorID
		}
		if err := domain.ValidateAlias(member.Alias); err != nil {
			return InstanceSet{}, err
		}
		if _, duplicate := declared[member.Alias]; duplicate {
			return InstanceSet{}, fmt.Errorf("成员别名 %s 重复", member.Alias)
		}
		declared[member.Alias] = member
	}
	set, err := s.db.GetSetByOwner(ctx, request.InstanceID)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		set, err = s.CreateSet(ctx, domain.FactorSet{
			SpaceID: request.SpaceID, SourceDatasetID: request.SourceDatasetID, Freq: request.Freq,
			SubjectMode: request.SubjectMode, Subjects: request.Subjects, OwnerInstanceID: request.InstanceID,
		})
		if err != nil {
			return InstanceSet{}, fmt.Errorf("创建私有因子集：%w", err)
		}
	case err != nil:
		return InstanceSet{}, err
	default:
		if set.SpaceID != strings.TrimSpace(request.SpaceID) || set.SourceDatasetID != strings.TrimSpace(request.SourceDatasetID) ||
			set.Freq != strings.ToLower(strings.TrimSpace(request.Freq)) {
			return InstanceSet{}, fmt.Errorf("实例 %s 的私有因子集绑定的是 %s/%s，不能改成 %s/%s；先删除实例的私有因子集",
				request.InstanceID, set.SourceDatasetID, set.Freq, request.SourceDatasetID, request.Freq)
		}
		if set.Status == domain.SetStatusPending {
			if set, err = s.CreateSet(ctx, set); err != nil {
				return InstanceSet{}, err
			}
		}
		if set.Status == domain.SetStatusDisabled {
			if set, err = s.SetSetStatus(ctx, set.SetID, domain.SetStatusEnabled); err != nil {
				return InstanceSet{}, err
			}
		}
		mode := request.SubjectMode
		if mode == "" {
			mode = domain.SubjectModeAll
		}
		if mode != set.SubjectMode || !equalStrings(sortedCopy(request.Subjects), sortedCopy(set.Subjects)) {
			if set, err = s.UpdateSetSubjects(ctx, set.SetID, mode, request.Subjects); err != nil {
				return InstanceSet{}, err
			}
		}
	}
	existing, err := s.db.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return InstanceSet{}, err
	}
	present := make(map[string]domain.SetMember, len(existing))
	for _, member := range existing {
		present[member.MemberAlias] = member
	}
	// 先移出不再声明或换了定义的成员，再加入并启用声明的成员。
	for alias, member := range present {
		if want, ok := declared[alias]; ok && want.FactorID == member.FactorID {
			continue
		}
		if member.Status == domain.MemberStatusEnabled {
			if _, _, err := s.SetFactorMemberStatus(ctx, set.SetID, alias, domain.MemberStatusDisabled); err != nil {
				return InstanceSet{}, err
			}
		}
		if err := s.RemoveFactorFromSet(ctx, set.SetID, alias); err != nil {
			return InstanceSet{}, err
		}
		delete(present, alias)
	}
	aliases := make([]string, 0, len(declared))
	for alias := range declared {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	result := InstanceSet{Set: set}
	jobs := map[string]bool{}
	for _, alias := range aliases {
		want := declared[alias]
		if _, ok := present[alias]; !ok {
			if _, err := s.AddFactorToSet(ctx, set.SetID, alias, want.FactorID); err != nil {
				return InstanceSet{}, fmt.Errorf("加入成员 %s：%w", alias, err)
			}
		}
		_, jobID, err := s.SetFactorMemberStatus(ctx, set.SetID, alias, domain.MemberStatusEnabled)
		if err != nil {
			return InstanceSet{}, fmt.Errorf("启用成员 %s：%w", alias, err)
		}
		if jobID != "" && !jobs[jobID] {
			jobs[jobID] = true
			result.BackfillJobs = append(result.BackfillJobs, jobID)
		}
	}
	if result.Set, result.Members, err = s.GetSet(ctx, set.SetID); err != nil {
		return InstanceSet{}, err
	}
	return result, nil
}

// SetInstanceSetEnabled 停用或重新启用实例的私有因子集（实例停用时停止计算，成员与已写入的数据保留）。
func (s *Service) SetInstanceSetEnabled(ctx context.Context, instanceID string, enabled bool) (domain.FactorSet, error) {
	set, err := s.db.GetSetByOwner(ctx, instanceID)
	if err != nil {
		return domain.FactorSet{}, err
	}
	status := domain.SetStatusDisabled
	if enabled {
		status = domain.SetStatusEnabled
	}
	return s.SetSetStatus(ctx, set.SetID, status)
}

// ReleaseInstanceSet 删除实例的私有因子集：停用并移出全部成员、停用集合，再删除集合与它的结果数据集。
// 实例已经没有私有集合时直接返回。
func (s *Service) ReleaseInstanceSet(ctx context.Context, instanceID string) error {
	set, err := s.db.GetSetByOwner(ctx, instanceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if set.Status != domain.SetStatusDeleting {
		members, err := s.db.ListMembers(ctx, set.SetID, "")
		if err != nil {
			return err
		}
		for _, member := range members {
			if member.Status == domain.MemberStatusEnabled {
				if _, _, err := s.SetFactorMemberStatus(ctx, set.SetID, member.MemberAlias, domain.MemberStatusDisabled); err != nil {
					return err
				}
			}
			if err := s.RemoveFactorFromSet(ctx, set.SetID, member.MemberAlias); err != nil {
				return err
			}
		}
		if set.Status == domain.SetStatusEnabled {
			if _, err := s.SetSetStatus(ctx, set.SetID, domain.SetStatusDisabled); err != nil {
				return err
			}
		}
	}
	return s.DeleteSet(ctx, set.SetID, true)
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
