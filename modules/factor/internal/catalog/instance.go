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

// InstanceTaskRequest 描述策略实例的私有因子任务（因子动态调参执行计划 2.3）：源数据集、频率与声明的成员。
type InstanceTaskRequest struct {
	InstanceID      string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	Members         []InstanceMember
}

// InstanceTask 是 EnsureInstanceTask 的结果：任务与成员。
type InstanceTask struct {
	Set     domain.FactorTask
	Members []domain.TaskMember
}

// EnsureInstanceTask 建立或对齐策略实例的私有因子任务，可重复调用：任务不存在时创建（实例是唯一的参数写入方），
// 已存在时核对源数据集与频率；声明的成员都加入并启用（启用会补齐结果列，不自动补算历史），
// 不再声明的成员停用并移出（已写入的列与值保留）；任务处于停用时重新启用。
func (s *Service) EnsureInstanceTask(ctx context.Context, request InstanceTaskRequest) (InstanceTask, error) {
	request.InstanceID = strings.TrimSpace(request.InstanceID)
	if request.InstanceID == "" {
		return InstanceTask{}, errors.New("instance_id is required")
	}
	if len(request.Members) == 0 {
		return InstanceTask{}, errors.New("私有因子任务至少需要一个成员")
	}
	declared := make(map[string]InstanceMember, len(request.Members))
	for _, member := range request.Members {
		member.Alias, member.FactorID = strings.TrimSpace(member.Alias), strings.TrimSpace(member.FactorID)
		if member.Alias == "" {
			member.Alias = member.FactorID
		}
		if err := domain.ValidateAlias(member.Alias); err != nil {
			return InstanceTask{}, err
		}
		if _, duplicate := declared[member.Alias]; duplicate {
			return InstanceTask{}, fmt.Errorf("成员别名 %s 重复", member.Alias)
		}
		declared[member.Alias] = member
	}
	set, err := s.db.GetTaskByOwner(ctx, request.InstanceID)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		set, err = s.CreateTask(ctx, domain.FactorTask{
			SpaceID: request.SpaceID, SourceDatasetID: request.SourceDatasetID, Freq: request.Freq,
			OwnerInstanceID: request.InstanceID,
		})
		if err != nil {
			return InstanceTask{}, fmt.Errorf("创建私有因子任务：%w", err)
		}
	case err != nil:
		return InstanceTask{}, err
	default:
		if set.SpaceID != strings.TrimSpace(request.SpaceID) || set.SourceDatasetID != strings.TrimSpace(request.SourceDatasetID) ||
			set.Freq != strings.ToLower(strings.TrimSpace(request.Freq)) {
			return InstanceTask{}, fmt.Errorf("实例 %s 的私有因子任务绑定的是 %s/%s，不能改成 %s/%s；先删除实例的私有因子任务",
				request.InstanceID, set.SourceDatasetID, set.Freq, request.SourceDatasetID, request.Freq)
		}
		if set.Status == domain.TaskStatusPending {
			if set, err = s.CreateTask(ctx, set); err != nil {
				return InstanceTask{}, err
			}
		}
		if set.Status == domain.TaskStatusDisabled {
			if set, err = s.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusEnabled); err != nil {
				return InstanceTask{}, err
			}
		}
	}
	existing, err := s.db.ListMembers(ctx, set.TaskID, "")
	if err != nil {
		return InstanceTask{}, err
	}
	present := make(map[string]domain.TaskMember, len(existing))
	for _, member := range existing {
		present[member.MemberAlias] = member
	}
	// 先移出不再声明或换了定义的成员，再加入并启用声明的成员。
	for alias, member := range present {
		if want, ok := declared[alias]; ok && want.FactorID == member.FactorID {
			continue
		}
		if member.Status == domain.MemberStatusEnabled {
			if _, err := s.SetTaskMemberStatus(ctx, set.TaskID, alias, domain.MemberStatusDisabled); err != nil {
				return InstanceTask{}, err
			}
		}
		if err := s.RemoveTaskMember(ctx, set.TaskID, alias); err != nil {
			return InstanceTask{}, err
		}
		delete(present, alias)
	}
	aliases := make([]string, 0, len(declared))
	for alias := range declared {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	result := InstanceTask{Set: set}
	for _, alias := range aliases {
		want := declared[alias]
		if _, ok := present[alias]; !ok {
			if _, err := s.AddTaskMember(ctx, set.TaskID, alias, want.FactorID, nil); err != nil {
				return InstanceTask{}, fmt.Errorf("加入成员 %s：%w", alias, err)
			}
		}
		if _, err := s.SetTaskMemberStatus(ctx, set.TaskID, alias, domain.MemberStatusEnabled); err != nil {
			return InstanceTask{}, fmt.Errorf("启用成员 %s：%w", alias, err)
		}
	}
	if result.Set, result.Members, err = s.GetTask(ctx, set.TaskID); err != nil {
		return InstanceTask{}, err
	}
	return result, nil
}

// SetInstanceTaskEnabled 停用或重新启用实例的私有因子任务（实例停用时停止计算，成员与已写入的数据保留）。
// 实例没有私有因子任务（没有声明因子）时停用直接返回，启用报错。
func (s *Service) SetInstanceTaskEnabled(ctx context.Context, instanceID string, enabled bool) (domain.FactorTask, error) {
	set, err := s.db.GetTaskByOwner(ctx, instanceID)
	if errors.Is(err, gorm.ErrRecordNotFound) && !enabled {
		return domain.FactorTask{}, nil
	}
	if err != nil {
		return domain.FactorTask{}, err
	}
	status := domain.TaskStatusDisabled
	if enabled {
		status = domain.TaskStatusEnabled
	}
	return s.SetTaskStatus(ctx, set.TaskID, status)
}

// ReleaseInstanceTask 删除实例的私有因子任务：停用并移出全部成员、停用任务，再删除任务与它的结果数据集。
// 实例已经没有私有任务时直接返回。
func (s *Service) ReleaseInstanceTask(ctx context.Context, instanceID string) error {
	set, err := s.db.GetTaskByOwner(ctx, instanceID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if set.Status != domain.TaskStatusDeleting {
		members, err := s.db.ListMembers(ctx, set.TaskID, "")
		if err != nil {
			return err
		}
		for _, member := range members {
			if member.Status == domain.MemberStatusEnabled {
				if _, err := s.SetTaskMemberStatus(ctx, set.TaskID, member.MemberAlias, domain.MemberStatusDisabled); err != nil {
					return err
				}
			}
			if err := s.RemoveTaskMember(ctx, set.TaskID, member.MemberAlias); err != nil {
				return err
			}
		}
		if set.Status == domain.TaskStatusEnabled {
			if _, err := s.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusDisabled); err != nil {
				return err
			}
		}
	}
	return s.DeleteTask(ctx, set.TaskID, true)
}
