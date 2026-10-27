package catalog

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/factorname"
	"gorm.io/gorm"
)

type Option func(*Service)

func WithLockDir(path string) Option { return func(s *Service) { s.locks = setlock.New(path) } }

// WithReadyRecorder receives whether each set's result dataset accepted its
// columns; the compute engine only writes to ready sets.
func WithReadyRecorder(record func(taskID string, ready bool)) Option {
	return func(s *Service) { s.recordReady = record }
}

// Service coordinates local factor definitions with Storage metadata.
type Service struct {
	db          *store.Store
	metadata    storageio.Metadata
	recordReady func(taskID string, ready bool)
	locks       *setlock.Locks
	now         func() time.Time
	subjects    SubjectSource
}

func NewService(db *store.Store, metadata storageio.Metadata, options ...Option) *Service {
	s := &Service{db: db, metadata: metadata, now: time.Now, locks: setlock.New("")}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s
}

// Locks exposes the per-set locks shared with live pipeline and recalculation.
func (s *Service) Locks() *setlock.Locks { return s.locks }

// CreateTask 创建因子任务及其结果数据集。TaskID 为空时生成随机后缀的任务 ID；调用方也可以指定 ftask_<后缀>，
// 同一个 ID 重复创建且身份一致时按幂等处理（创建中途失败后用同一个 ID 重试）。名称含汉字、不超过 10 个字、空间内唯一，
// 直接用作 Storage 结果数据集的名称。结果数据集创建时同时注册 __params 系统列。
func (s *Service) CreateTask(ctx context.Context, in domain.FactorTask) (domain.FactorTask, error) {
	if s == nil || s.db == nil || s.metadata == nil {
		return domain.FactorTask{}, errors.New("factor store and Storage metadata are required")
	}
	if strings.TrimSpace(in.Freq) == "" {
		return domain.FactorTask{}, errors.New("freq is required")
	}
	in.SpaceID = strings.TrimSpace(in.SpaceID)
	in.Name = strings.TrimSpace(in.Name)
	in.SourceDatasetID = strings.TrimSpace(in.SourceDatasetID)
	in.Freq = strings.ToLower(strings.TrimSpace(in.Freq))
	in.BoundInstanceID = ""
	in.TaskID = strings.TrimSpace(in.TaskID)
	if in.TaskID == "" {
		suffix, err := factorname.NewSuffix()
		if err != nil {
			return domain.FactorTask{}, err
		}
		if in.TaskID, err = factorname.TaskID(suffix); err != nil {
			return domain.FactorTask{}, err
		}
	}
	if in.ResultDatasetID == "" {
		in.ResultDatasetID = domain.ResultDatasetID(in.TaskID)
	}
	if in.Status == "" {
		in.Status = domain.TaskStatusPending
	}
	if in.Status != domain.TaskStatusPending {
		return domain.FactorTask{}, errors.New("new factor tasks must start pending")
	}
	if err := domain.ValidateTask(in); err != nil {
		return domain.FactorTask{}, err
	}
	// 启用成员时才会取空间日历；不支持的空间在创建时就拒绝，避免出现永远无法启用的计算任务。
	if _, err := periodclock.ForSpace(in.SpaceID); err != nil {
		return domain.FactorTask{}, err
	}
	unlock, err := s.locks.LockContext(ctx, in.TaskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	defer unlock()
	existing, err := s.db.GetTask(ctx, in.TaskID)
	newTask := errors.Is(err, gorm.ErrRecordNotFound)
	if err == nil {
		if err := sameTaskIdentity(existing, in); err != nil {
			return domain.FactorTask{}, err
		}
		if existing.Status != domain.TaskStatusPending {
			return existing, nil
		}
		in = existing
	} else if !newTask {
		return domain.FactorTask{}, err
	}
	source, columns, err := s.sourceDataset(ctx, in)
	if err != nil {
		return domain.FactorTask{}, err
	}
	if newTask {
		if err := s.db.CreateTask(ctx, in); err != nil {
			return domain.FactorTask{}, err
		}
	}
	spec := resultDatasetSpec(in, source, columns)
	if err := s.metadata.CreateResultDataset(ctx, spec); err != nil {
		return domain.FactorTask{}, fmt.Errorf("create factor result dataset（任务 %s 已登记为待创建，可用同一个任务 ID 重试）: %w", in.TaskID, err)
	}
	if err := s.metadata.ActivateDataset(ctx, in.SpaceID, in.ResultDatasetID); err != nil {
		return domain.FactorTask{}, fmt.Errorf("activate factor result dataset: %w", err)
	}
	if err := s.metadata.EnsureResultView(ctx, in.SpaceID, in.ResultDatasetID); err != nil {
		return domain.FactorTask{}, fmt.Errorf("create factor result default View（任务 %s 已登记为待创建，可用同一个任务 ID 重试）: %w", in.TaskID, err)
	}
	if err := s.db.SetTaskStatus(ctx, in.TaskID, domain.TaskStatusPending, domain.TaskStatusEnabled); err != nil {
		return domain.FactorTask{}, fmt.Errorf("enable factor task after result dataset activation: %w", err)
	}
	s.markReady(in.TaskID, true)
	return s.db.GetTask(ctx, in.TaskID)
}

func (s *Service) SetTaskStatus(ctx context.Context, taskID, status string) (domain.FactorTask, error) {
	taskID = strings.TrimSpace(taskID)
	unlock, err := s.locks.LockContext(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	defer unlock()
	task, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	if status != domain.TaskStatusEnabled && status != domain.TaskStatusDisabled {
		return domain.FactorTask{}, fmt.Errorf("status must be %q or %q", domain.TaskStatusEnabled, domain.TaskStatusDisabled)
	}
	if task.Status == status {
		return task, nil
	}
	if task.Status == domain.TaskStatusPending {
		return domain.FactorTask{}, errors.New("pending factor task cannot be toggled")
	}
	if status == domain.TaskStatusEnabled {
		if err := s.reconcileTaskUnlocked(ctx, task); err != nil {
			return domain.FactorTask{}, err
		}
	}
	if err := s.db.SetTaskStatus(ctx, task.TaskID, task.Status, status); err != nil {
		return domain.FactorTask{}, err
	}
	return s.db.GetTask(ctx, task.TaskID)
}

// BindTask 把任务与策略实例永久关联（独占）：同一个实例重复调用直接成功，换一个实例则拒绝；写入后不能修改或清空。
// 任务没有成员时拒绝：没有成员的任务不会产出任何列，关联没有意义（设计 S1）。
func (s *Service) BindTask(ctx context.Context, taskID, instanceID string) (domain.FactorTask, error) {
	taskID, instanceID = strings.TrimSpace(taskID), strings.TrimSpace(instanceID)
	if taskID == "" || instanceID == "" {
		return domain.FactorTask{}, errors.New("task_id and instance_id are required")
	}
	unlock, err := s.locks.LockContext(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	defer unlock()
	task, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	if task.BoundInstanceID == instanceID {
		return task, nil
	}
	if task.BoundInstanceID != "" {
		return domain.FactorTask{}, fmt.Errorf("%w: factor task %q is already bound to another strategy instance", store.ErrConflict, taskID)
	}
	if task.Status == domain.TaskStatusPending {
		return domain.FactorTask{}, errors.New("factor task is not ready")
	}
	count, err := s.db.CountMembers(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	if count == 0 {
		return domain.FactorTask{}, fmt.Errorf("factor task %q has no members; add at least one member before binding", taskID)
	}
	return s.db.BindTask(ctx, taskID, instanceID)
}

// DeleteTask 删除没有成员、也没有关联策略实例的已停用任务。purge 为 true 时先删结果数据集、再删数据库记录；
// Storage 删除数据集对"已不存在"幂等，所以两步都可以重试，不需要持久化删除意图。
func (s *Service) DeleteTask(ctx context.Context, taskID string, purge bool) error {
	taskID = strings.TrimSpace(taskID)
	unlock, err := s.locks.LockContext(ctx, taskID)
	if err != nil {
		return err
	}
	defer unlock()
	task, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if task.BoundInstanceID != "" {
		return fmt.Errorf("%w: factor task %q is bound to strategy instance %q and cannot be deleted", store.ErrConflict, taskID, task.BoundInstanceID)
	}
	if task.Status != domain.TaskStatusDisabled {
		return errors.New("factor task must be disabled before deletion")
	}
	memberCount, err := s.db.CountMembers(ctx, taskID)
	if err != nil {
		return err
	}
	if memberCount != 0 {
		return errors.New("factor task must not contain members before deletion")
	}
	if purge {
		if s.metadata == nil {
			return errors.New("Storage metadata is required to purge the result dataset")
		}
		if err := s.metadata.DeleteDataset(ctx, task.SpaceID, task.ResultDatasetID); err != nil {
			return fmt.Errorf("delete factor result dataset: %w", err)
		}
	}
	return s.db.DeleteTask(ctx, taskID)
}

// GetTask returns a factor task with all of its members (any status) and their definitions.
func (s *Service) GetTask(ctx context.Context, taskID string) (domain.FactorTask, []domain.TaskMember, error) {
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, nil, err
	}
	members, err := s.db.ListMembers(ctx, taskID, "")
	return set, members, err
}

func (s *Service) ListTasks(ctx context.Context) ([]domain.FactorTask, error) {
	return s.db.ListTasks(ctx)
}

// CreateFactor registers a standalone definition. It only runs the static
// checks (compiling the spec); dataset checks wait for AddTaskMember.
func (s *Service) CreateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	factor, err := domain.NormalizeFactorDefinition(in)
	if err != nil {
		return domain.FactorDef{}, err
	}
	unlock, err := s.locks.LockFactorContext(ctx, factor.FactorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	defer unlock()
	if err := s.db.CreateFactor(ctx, factor); err != nil {
		return domain.FactorDef{}, err
	}
	return s.db.GetFactor(ctx, factor.FactorID)
}

// UpdateFactor 改写一个定义。被任务引用的定义有这些限制：任一成员已启用时拒绝；不允许增删参数，不允许改参数的类型、
// 声明顺序和默认值，也不允许增删、改名或重排输出（设计 5.4）；新的参数范围必须仍包含所有引用它的成员保存的参数；
// 新定义还要对每个引用它的任务重新做成员校验，任何一项不通过都拒绝整次更新。
func (s *Service) UpdateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	factor, err := domain.NormalizeFactorDefinition(in)
	if err != nil {
		return domain.FactorDef{}, err
	}
	unlockFactor, err := s.locks.LockFactorContext(ctx, factor.FactorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	defer unlockFactor()
	current, err := s.db.GetFactor(ctx, factor.FactorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	// Every member operation takes the definition lock first, so the usage list
	// read here cannot change until this update finishes.
	usages, err := s.db.ListUsages(ctx, factor.FactorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	referencing := usages[factor.FactorID]
	for _, usage := range referencing {
		if usage.Status == domain.MemberStatusEnabled {
			return domain.FactorDef{}, fmt.Errorf("%w: factor %q is enabled in task %q; disable it before updating its definition",
				store.ErrConflict, factor.FactorID, usage.TaskID)
		}
	}
	if len(referencing) > 0 {
		if err := domain.CheckDefinitionCompatible(current, factor); err != nil {
			return domain.FactorDef{}, fmt.Errorf("%w: %v", store.ErrConflict, err)
		}
	}
	taskIDs := make([]string, 0, len(referencing))
	seenTasks := map[string]bool{}
	for _, usage := range referencing {
		if !seenTasks[usage.TaskID] {
			seenTasks[usage.TaskID] = true
			taskIDs = append(taskIDs, usage.TaskID)
		}
	}
	sort.Strings(taskIDs)
	for _, taskID := range taskIDs {
		unlockTask, err := s.locks.LockContext(ctx, taskID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		defer unlockTask()
	}
	for _, usage := range referencing {
		task, err := s.db.GetTask(ctx, usage.TaskID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		member, err := s.db.GetMember(ctx, usage.TaskID, usage.MemberKey)
		if err != nil {
			return domain.FactorDef{}, err
		}
		if err := domain.CheckMemberParams(factor, member.Params); err != nil {
			return domain.FactorDef{}, fmt.Errorf("factor task %q member %q 的参数不再合法：%w", usage.TaskID, usage.MemberKey, err)
		}
		if _, err := s.validateMembership(ctx, task, domain.MemberDef{Key: usage.MemberKey, Factor: factor, Kind: member.Kind, Params: member.Params}); err != nil {
			return domain.FactorDef{}, fmt.Errorf("factor task %q member %q: %w", usage.TaskID, usage.MemberKey, err)
		}
	}
	if err := s.db.UpdateFactor(ctx, factor); err != nil {
		return domain.FactorDef{}, err
	}
	return s.db.GetFactor(ctx, factor.FactorID)
}

// DeleteFactor removes a definition that no set references.
func (s *Service) DeleteFactor(ctx context.Context, factorID string) error {
	factorID = strings.TrimSpace(factorID)
	unlock, err := s.locks.LockFactorContext(ctx, factorID)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := s.db.GetFactor(ctx, factorID); err != nil {
		return err
	}
	usages, err := s.db.ListUsages(ctx, factorID)
	if err != nil {
		return err
	}
	if used := usages[factorID]; len(used) > 0 {
		return fmt.Errorf("%w: factor %q is still used by factor task %q", store.ErrConflict, factorID, used[0].TaskID)
	}
	return s.db.DeleteFactor(ctx, factorID)
}

func (s *Service) GetFactor(ctx context.Context, factorID string) (domain.FactorInfo, error) {
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.FactorInfo{}, err
	}
	usages, err := s.db.ListUsages(ctx, factor.FactorID)
	if err != nil {
		return domain.FactorInfo{}, err
	}
	return domain.FactorInfo{Factor: factor, Usages: usages[factor.FactorID]}, nil
}

// ListFactors returns every definition, or only one set's members' definitions
// (optionally filtered by member status) when taskID is given. Each entry carries
// all of its usages.
func (s *Service) ListFactors(ctx context.Context, taskID, status string) ([]domain.FactorInfo, error) {
	var factors []domain.FactorDef
	if strings.TrimSpace(taskID) == "" {
		all, err := s.db.ListFactors(ctx)
		if err != nil {
			return nil, err
		}
		factors = all
	} else {
		if _, err := s.db.GetTask(ctx, taskID); err != nil {
			return nil, err
		}
		members, err := s.db.ListMembers(ctx, taskID, status)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			factors = append(factors, member.Factor)
		}
	}
	usages, err := s.db.ListUsages(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]domain.FactorInfo, 0, len(factors))
	for _, factor := range factors {
		infos = append(infos, domain.FactorInfo{Factor: factor, Usages: usages[factor.FactorID]})
	}
	return infos, nil
}

// AddTaskMember 把定义作为一个停用的成员加入任务，加入前对照任务的源数据集校验；既不添加结果列，也不提交补算。
// kind 为空按 fixed：固定成员的参数由 params 加上定义的默认值补全成完整参数并校验，之后不变；可变成员的 params 是默认参数
// （没有策略提交时用它），参数由关联的策略逐期提交。成员键与列名都按公共命名包推导；同一个任务里同一个因子最多一个可变成员。
func (s *Service) AddTaskMember(ctx context.Context, taskID, factorID, kind string, params map[string]float64) (domain.TaskMember, error) {
	taskID, factorID, kind = strings.TrimSpace(taskID), strings.TrimSpace(factorID), strings.TrimSpace(kind)
	if kind == "" {
		kind = domain.MemberKindFixed
	}
	unlock, err := s.lockMemberContext(ctx, taskID, factorID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	defer unlock()
	task, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	if err := requireTaskOpen(task); err != nil {
		return domain.TaskMember{}, err
	}
	// Re-read the definition under the locks so a concurrent update cannot interleave.
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	member, err := newMemberDef(factor, kind, params)
	if err != nil {
		return domain.TaskMember{}, err
	}
	if _, err := s.validateMembership(ctx, task, member); err != nil {
		return domain.TaskMember{}, err
	}
	if err := s.checkReaddConsistent(ctx, task, member); err != nil {
		return domain.TaskMember{}, err
	}
	stored, err := s.db.AddMember(ctx, taskID, member.Key, factorID, member.Kind, member.Params)
	if err != nil {
		return domain.TaskMember{}, err
	}
	return domain.TaskMember{FactorTaskMember: stored, Factor: factor}, nil
}

// newMemberDef 按类型和参数构造成员：补全参数、推导成员键。
func newMemberDef(factor domain.FactorDef, kind string, params map[string]float64) (domain.MemberDef, error) {
	if kind != domain.MemberKindFixed && kind != domain.MemberKindVar {
		return domain.MemberDef{}, fmt.Errorf("成员类型 %q 不合法：只能是 %s 或 %s", kind, domain.MemberKindFixed, domain.MemberKindVar)
	}
	full, err := domain.ResolveMemberParams(factor, params)
	if err != nil {
		return domain.MemberDef{}, err
	}
	if kind == domain.MemberKindVar && len(full) == 0 {
		return domain.MemberDef{}, fmt.Errorf("因子 %s 没有可调参数，不能作为可变成员", factor.FactorID)
	}
	key, err := domain.DeriveMemberKey(factor, kind, full)
	if err != nil {
		return domain.MemberDef{}, err
	}
	return domain.MemberDef{Key: key, Factor: factor, Kind: kind, Params: full}, nil
}

// checkReaddConsistent 重新加入曾经移除的成员键时，核对旧列元数据里的 factor_params、member_kind 与本次一致：
// 被引用的定义连默认值都不能改，所以同一个键必然对应同一组参数；这里是兜底，不一致就拒绝。
func (s *Service) checkReaddConsistent(ctx context.Context, task domain.FactorTask, member domain.MemberDef) error {
	columns, err := s.metadata.ListColumns(ctx, task.SpaceID, task.ResultDatasetID)
	if err != nil {
		return fmt.Errorf("list factor result columns: %w", err)
	}
	wanted := make(map[string]storageio.ColumnInfo)
	for _, column := range factorOutputColumns(member) {
		wanted[column.ColumnName] = column
	}
	for _, existing := range columns {
		want, ok := wanted[existing.ColumnName]
		if !ok || existing.OriginType != storageio.ColumnOriginCustom {
			continue
		}
		if existing.Attributes["member_kind"] != want.Attributes["member_kind"] ||
			existing.Attributes["factor_params"] != want.Attributes["factor_params"] {
			return fmt.Errorf("结果列 %q 的旧元数据（member_kind=%q，factor_params=%q）与本次成员不一致，不能重新加入",
				existing.ColumnName, existing.Attributes["member_kind"], existing.Attributes["factor_params"])
		}
	}
	return nil
}

// RemoveTaskMember detaches a disabled member; result columns are kept.
func (s *Service) RemoveTaskMember(ctx context.Context, taskID, memberKey string) error {
	taskID, memberKey = strings.TrimSpace(taskID), strings.TrimSpace(memberKey)
	unlock, _, err := s.lockExistingMember(ctx, taskID, memberKey)
	if err != nil {
		return err
	}
	defer unlock()
	return s.db.RemoveMember(ctx, taskID, memberKey)
}

// SetTaskMemberStatus enables or disables one member. Enabling re-validates
// and adds result columns, but does not backfill history; disabling only stops
// writes and keeps the columns.
func (s *Service) SetTaskMemberStatus(ctx context.Context, taskID, memberKey, status string) (domain.TaskMember, error) {
	taskID, memberKey = strings.TrimSpace(taskID), strings.TrimSpace(memberKey)
	unlock, factorID, err := s.lockExistingMember(ctx, taskID, memberKey)
	if err != nil {
		return domain.TaskMember{}, err
	}
	defer unlock()
	task, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	if status != domain.MemberStatusEnabled && status != domain.MemberStatusDisabled {
		return domain.TaskMember{}, fmt.Errorf("status must be %q or %q", domain.MemberStatusEnabled, domain.MemberStatusDisabled)
	}
	member, err := s.db.GetMember(ctx, taskID, memberKey)
	if err != nil {
		return domain.TaskMember{}, err
	}
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	result := domain.TaskMember{FactorTaskMember: member, Factor: factor}
	if member.Status == status {
		return result, nil
	}
	if status == domain.MemberStatusDisabled {
		if err := s.db.SetMemberStatus(ctx, taskID, memberKey, member.Status, status); err != nil {
			return domain.TaskMember{}, err
		}
		return s.reloadMember(ctx, taskID, memberKey, factor)
	}
	if task.Status == domain.TaskStatusPending {
		return domain.TaskMember{}, errors.New("factor task is not ready")
	}
	// Definitions may have been edited and source columns may have changed while
	// the member was disabled, so enabling repeats the full membership checks.
	enabling := domain.MemberDef{Key: memberKey, Factor: factor, Kind: member.Kind, Params: member.Params}
	members, err := s.validateMembership(ctx, task, enabling)
	if err != nil {
		return domain.TaskMember{}, err
	}
	source, columns, err := s.sourceDataset(ctx, task)
	if err != nil {
		return domain.TaskMember{}, err
	}
	active := make([]domain.MemberDef, 0, len(members)+1)
	for _, candidate := range members {
		if candidate.Status == domain.MemberStatusEnabled {
			active = append(active, candidate.Def())
		}
	}
	active = replaceMember(active, enabling)
	if err := s.ensureResultColumns(ctx, task, source, columns, active...); err != nil {
		return domain.TaskMember{}, err
	}
	s.markReady(task.TaskID, true)
	// 启用只让成员参与此后每期的计算，不自动补算历史：需要历史因子值时用手动补算（recalc）指定范围。
	if err := s.db.SetMemberStatus(ctx, taskID, memberKey, member.Status, status); err != nil {
		return domain.TaskMember{}, err
	}
	return s.reloadMember(ctx, taskID, memberKey, factor)
}

func (s *Service) reloadMember(ctx context.Context, taskID, memberKey string, factor domain.FactorDef) (domain.TaskMember, error) {
	member, err := s.db.GetMember(ctx, taskID, memberKey)
	if err != nil {
		return domain.TaskMember{}, err
	}
	return domain.TaskMember{FactorTaskMember: member, Factor: factor}, nil
}

// lockExistingMember 找到成员引用的定义，按固定顺序（定义锁、任务锁）加锁后再确认成员仍引用同一定义。
func (s *Service) lockExistingMember(ctx context.Context, taskID, memberKey string) (func(), string, error) {
	member, err := s.db.GetMember(ctx, taskID, memberKey)
	if err != nil {
		return nil, "", err
	}
	unlock, err := s.lockMemberContext(ctx, taskID, member.FactorID)
	if err != nil {
		return nil, "", err
	}
	current, err := s.db.GetMember(ctx, taskID, memberKey)
	if err != nil || current.FactorID != member.FactorID {
		unlock()
		if err == nil {
			err = fmt.Errorf("%w: member %q changed concurrently", store.ErrConflict, memberKey)
		}
		return nil, "", err
	}
	return unlock, member.FactorID, nil
}

// lockMemberContext takes the definition lock and then the set lock, the fixed
// order for every operation that touches one (set, factor) pair.
func (s *Service) lockMemberContext(ctx context.Context, taskID, factorID string) (func(), error) {
	unlockFactor, err := s.locks.LockFactorContext(ctx, factorID)
	if err != nil {
		return nil, err
	}
	unlockTask, err := s.locks.LockContext(ctx, taskID)
	if err != nil {
		unlockFactor()
		return nil, err
	}
	return func() {
		unlockTask()
		unlockFactor()
	}, nil
}

// validateMembership runs the dataset-dependent checks of one member against
// one set and returns the set's current members.
func (s *Service) validateMembership(ctx context.Context, set domain.FactorTask, member domain.MemberDef) ([]domain.TaskMember, error) {
	_, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return nil, err
	}
	members, err := s.db.ListMembers(ctx, set.TaskID, "")
	if err != nil {
		return nil, err
	}
	siblings := make([]domain.MemberDef, 0, len(members))
	for _, sibling := range members {
		siblings = append(siblings, sibling.Def())
	}
	resultColumns, err := s.metadata.ListColumns(ctx, set.SpaceID, set.ResultDatasetID)
	if err != nil {
		return nil, fmt.Errorf("list factor result columns: %w", err)
	}
	origins := make(map[string]string)
	for _, column := range resultColumns {
		if memberKey := column.Attributes["member_key"]; column.OriginType == storageio.ColumnOriginCustom && memberKey != "" {
			origins[column.ColumnName] = memberKey
		}
	}
	if err := domain.ValidateMembership(set, member, sourceColumns(columns), siblings, origins); err != nil {
		return nil, err
	}
	return members, nil
}

func requireTaskOpen(task domain.FactorTask) error {
	if task.Status == domain.TaskStatusPending {
		return errors.New("factor task is not ready")
	}
	return nil
}

func (s *Service) markReady(taskID string, ready bool) {
	if s.recordReady != nil {
		s.recordReady(taskID, ready)
	}
}

func sameTaskIdentity(existing, requested domain.FactorTask) error {
	if existing.SpaceID != requested.SpaceID || existing.Name != requested.Name || existing.SourceDatasetID != requested.SourceDatasetID ||
		existing.Freq != requested.Freq || existing.ResultDatasetID != requested.ResultDatasetID {
		return fmt.Errorf("factor task %q conflicts with the requested identity", requested.TaskID)
	}
	return nil
}

func resultDatasetSpec(task domain.FactorTask, source storageio.DatasetInfo, columns []storageio.ColumnInfo) storageio.ResultDatasetSpec {
	return storageio.ResultDatasetSpec{
		SpaceID: task.SpaceID, DatasetID: task.ResultDatasetID, SourceDatasetID: task.SourceDatasetID,
		Name: task.Name, Description: "Factor results for " + source.DatasetID,
		DataSourceID: source.DataSourceID, DataNodeID: source.DataNodeID,
		DataKind: storageio.DataKindTimeSeries, Frequency: task.Freq,
		SubjectTags: append([]string(nil), source.SubjectTags...), Attributes: map[string]string{
			"owner_module": "factor", "dataset_role": storageio.DatasetRoleFactorResult, "source_dataset_id": source.DatasetID,
		},
		Columns: append(append([]storageio.ColumnInfo(nil), columns...), paramsColumn()),
	}
}

func businessColumns(columns []storageio.ColumnInfo) []storageio.ColumnInfo {
	reserved := make(map[string]struct{}, len(domain.ReservedColumns))
	for _, name := range domain.ReservedColumns {
		reserved[name] = struct{}{}
	}
	out := make([]storageio.ColumnInfo, 0, len(columns))
	for _, col := range columns {
		if col.ColumnName == "" || col.OriginType == storageio.ColumnOriginSystem || (col.Status != "" && col.Status != storageio.ColumnStatusActive) {
			continue
		}
		if _, ok := reserved[col.ColumnName]; ok {
			continue
		}
		out = append(out, col)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ColumnName < out[j].ColumnName })
	return out
}

// sourceColumns 把源数据集的列转换成成员校验用的形式：int 与 double 是数值列。
func sourceColumns(columns []storageio.ColumnInfo) []domain.SourceColumn {
	out := make([]domain.SourceColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, domain.SourceColumn{Name: column.ColumnName,
			Numeric: column.ValueType == storageio.ColumnTypeInt || column.ValueType == storageio.ColumnTypeDouble})
	}
	return out
}

func replaceMember(members []domain.MemberDef, next domain.MemberDef) []domain.MemberDef {
	for i, member := range members {
		if member.Key == next.Key {
			members[i] = next
			return members
		}
	}
	return append(members, next)
}
