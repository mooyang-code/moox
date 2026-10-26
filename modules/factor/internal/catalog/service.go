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

func (s *Service) CreateTask(ctx context.Context, in domain.FactorTask) (domain.FactorTask, error) {
	if s == nil || s.db == nil || s.metadata == nil {
		return domain.FactorTask{}, errors.New("factor store and Storage metadata are required")
	}
	if strings.TrimSpace(in.Freq) == "" {
		return domain.FactorTask{}, errors.New("freq is required")
	}
	in.SpaceID = strings.TrimSpace(in.SpaceID)
	in.SourceDatasetID = strings.TrimSpace(in.SourceDatasetID)
	in.Freq = strings.ToLower(strings.TrimSpace(in.Freq))
	in.OwnerInstanceID = strings.TrimSpace(in.OwnerInstanceID)
	if in.TaskID == "" {
		in.TaskID = domain.TaskID(in.SourceDatasetID, in.Freq)
		if in.OwnerInstanceID != "" {
			in.TaskID = domain.InstanceTaskID(in.OwnerInstanceID)
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
		if existing.Status == domain.TaskStatusDeleting {
			return domain.FactorTask{}, errors.New("factor task purge is pending")
		}
		if existing.Status != domain.TaskStatusPending {
			return existing, nil
		}
		in = existing
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
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
	if in.OwnerInstanceID != "" {
		spec.Name = domain.InstanceDatasetName(in.OwnerInstanceID)
		spec.Description = "Private factor results of strategy instance " + in.OwnerInstanceID
		spec.Attributes["owner_instance_id"] = in.OwnerInstanceID
	}
	if err := s.metadata.CreateResultDataset(ctx, spec); err != nil {
		return domain.FactorTask{}, fmt.Errorf("create factor result dataset: %w", err)
	}
	if err := s.metadata.ActivateDataset(ctx, in.SpaceID, in.ResultDatasetID); err != nil {
		return domain.FactorTask{}, fmt.Errorf("activate factor result dataset: %w", err)
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
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	if status != domain.TaskStatusEnabled && status != domain.TaskStatusDisabled {
		return domain.FactorTask{}, fmt.Errorf("status must be %q or %q", domain.TaskStatusEnabled, domain.TaskStatusDisabled)
	}
	if set.Status == domain.TaskStatusDeleting {
		return domain.FactorTask{}, errors.New("factor task purge is pending")
	}
	if set.Status == status {
		return set, nil
	}
	if set.Status == domain.TaskStatusPending {
		return domain.FactorTask{}, errors.New("pending factor task cannot be toggled")
	}
	if status == domain.TaskStatusEnabled {
		if err := s.reconcileTaskUnlocked(ctx, set); err != nil {
			return domain.FactorTask{}, err
		}
	}
	if err := s.db.SetTaskStatus(ctx, set.TaskID, set.Status, status); err != nil {
		return domain.FactorTask{}, err
	}
	return s.db.GetTask(ctx, set.TaskID)
}

func (s *Service) DeleteTask(ctx context.Context, taskID string, purge bool) error {
	taskID = strings.TrimSpace(taskID)
	unlock, err := s.locks.LockContext(ctx, taskID)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if set.Status != domain.TaskStatusDisabled && !(purge && set.Status == domain.TaskStatusDeleting) {
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
		if set.Status != domain.TaskStatusDeleting {
			if err := s.db.SetTaskStatus(ctx, set.TaskID, domain.TaskStatusDisabled, domain.TaskStatusDeleting); err != nil {
				return fmt.Errorf("persist factor result purge intent: %w", err)
			}
		}
		if err := s.metadata.DeleteDataset(ctx, set.SpaceID, set.ResultDatasetID); err != nil {
			return fmt.Errorf("delete factor result dataset: %w", err)
		}
	}
	// 参数记录最后删：结果数据集清理失败（例如还有额外的 View 引用）时任务停在 deleting，冻结历史还在，可以重试。
	if err := s.db.DeleteTaskParams(ctx, taskID); err != nil {
		return fmt.Errorf("delete factor task params: %w", err)
	}
	if err := s.db.DeleteTask(ctx, taskID); err != nil {
		return err
	}
	return nil
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

// UpdateFactor rewrites a definition. It is rejected while any member is
// enabled; otherwise the new definition is re-validated against every set that
// references it and any failure rejects the whole update.
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
	if _, err := s.db.GetFactor(ctx, factor.FactorID); err != nil {
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
			return domain.FactorDef{}, fmt.Errorf("%w: factor %q is enabled in set %q; disable it before updating its definition",
				store.ErrConflict, factor.FactorID, usage.TaskID)
		}
	}
	setIDs := make([]string, 0, len(referencing))
	seenTasks := map[string]bool{}
	for _, usage := range referencing {
		if !seenTasks[usage.TaskID] {
			seenTasks[usage.TaskID] = true
			setIDs = append(setIDs, usage.TaskID)
		}
	}
	sort.Strings(setIDs)
	for _, taskID := range setIDs {
		unlockTask, err := s.locks.LockContext(ctx, taskID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		defer unlockTask()
	}
	for _, usage := range referencing {
		set, err := s.db.GetTask(ctx, usage.TaskID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		// 普通任务的成员保存的固定参数在新定义上必须恰好是声明的全部参数且仍然合法；策略专属任务的成员不带固定参数。
		if set.OwnerInstanceID == "" {
			if err := domain.CheckSharedMemberParams(factor, usage.Params); err != nil {
				return domain.FactorDef{}, fmt.Errorf("factor task %q member %q 的固定参数不再合法：%w", usage.TaskID, usage.MemberAlias, err)
			}
		}
		if _, err := s.validateMembership(ctx, set, domain.MemberDef{Alias: usage.MemberAlias, Factor: factor, Params: usage.Params}); err != nil {
			return domain.FactorDef{}, fmt.Errorf("factor task %q member %q: %w", usage.TaskID, usage.MemberAlias, err)
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

// AddTaskMember attaches a definition to a set as a disabled member after
// validating it against the set's source dataset. It neither adds result columns
// nor submits a backfill.
//
// 非策略专属任务的成员带固定参数：params 给出的参数加上定义的默认值补全成完整参数并校验，之后不变；别名为空时按参数生成
// （全默认沿用因子 ID）；同任务内同因子同参数只能有一个成员。策略专属任务的参数由写入方逐期提交，不能带固定参数，
// 别名为空时取因子 ID。
func (s *Service) AddTaskMember(ctx context.Context, taskID, alias, factorID string, params map[string]float64) (domain.TaskMember, error) {
	taskID, alias, factorID = strings.TrimSpace(taskID), strings.TrimSpace(alias), strings.TrimSpace(factorID)
	unlock, err := s.lockMemberContext(ctx, taskID, factorID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	defer unlock()
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	if err := requireSetOpen(set); err != nil {
		return domain.TaskMember{}, err
	}
	// Re-read the definition under the locks so a concurrent update cannot interleave.
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	fixed, alias, err := s.resolveNewMember(ctx, set, factor, alias, params)
	if err != nil {
		return domain.TaskMember{}, err
	}
	if _, err := s.validateMembership(ctx, set, domain.MemberDef{Alias: alias, Factor: factor, Params: fixed}); err != nil {
		return domain.TaskMember{}, err
	}
	member, err := s.db.AddMember(ctx, taskID, alias, factorID, fixed)
	if err != nil {
		return domain.TaskMember{}, err
	}
	return domain.TaskMember{FactorTaskMember: member, Factor: factor}, nil
}

// RemoveTaskMember detaches a disabled member; result columns are kept.
func (s *Service) RemoveTaskMember(ctx context.Context, taskID, alias string) error {
	taskID, alias = strings.TrimSpace(taskID), strings.TrimSpace(alias)
	unlock, _, err := s.lockExistingMember(ctx, taskID, alias)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if set.Status == domain.TaskStatusDeleting {
		return errors.New("factor task purge is pending")
	}
	return s.db.RemoveMember(ctx, taskID, alias)
}

// SetTaskMemberStatus enables or disables one member. Enabling re-validates
// and adds result columns, but does not backfill history; disabling only stops
// writes and keeps the columns.
func (s *Service) SetTaskMemberStatus(ctx context.Context, taskID, alias, status string) (domain.TaskMember, error) {
	taskID, alias = strings.TrimSpace(taskID), strings.TrimSpace(alias)
	unlock, factorID, err := s.lockExistingMember(ctx, taskID, alias)
	if err != nil {
		return domain.TaskMember{}, err
	}
	defer unlock()
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.TaskMember{}, err
	}
	if set.Status == domain.TaskStatusDeleting {
		return domain.TaskMember{}, errors.New("factor task purge is pending")
	}
	if status != domain.MemberStatusEnabled && status != domain.MemberStatusDisabled {
		return domain.TaskMember{}, fmt.Errorf("status must be %q or %q", domain.MemberStatusEnabled, domain.MemberStatusDisabled)
	}
	member, err := s.db.GetMember(ctx, taskID, alias)
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
		if err := s.db.SetMemberStatus(ctx, taskID, alias, member.Status, status); err != nil {
			return domain.TaskMember{}, err
		}
		return s.reloadMember(ctx, taskID, alias, factor)
	}
	if set.Status == domain.TaskStatusPending {
		return domain.TaskMember{}, errors.New("factor task is not ready")
	}
	// Definitions may have been edited and source columns may have changed while
	// the member was disabled, so enabling repeats the full membership checks.
	enabling := domain.MemberDef{Alias: alias, Factor: factor, Params: member.Params}
	members, err := s.validateMembership(ctx, set, enabling)
	if err != nil {
		return domain.TaskMember{}, err
	}
	source, columns, err := s.sourceDataset(ctx, set)
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
	if err := s.ensureResultColumns(ctx, set, source, columns, active...); err != nil {
		return domain.TaskMember{}, err
	}
	s.markReady(set.TaskID, true)
	// 启用只让成员参与此后每期的计算，不自动补算历史：需要历史因子值时用手动补算（recalc）指定范围。
	if err := s.db.SetMemberStatus(ctx, taskID, alias, member.Status, status); err != nil {
		return domain.TaskMember{}, err
	}
	return s.reloadMember(ctx, taskID, alias, factor)
}

func (s *Service) reloadMember(ctx context.Context, taskID, alias string, factor domain.FactorDef) (domain.TaskMember, error) {
	member, err := s.db.GetMember(ctx, taskID, alias)
	if err != nil {
		return domain.TaskMember{}, err
	}
	return domain.TaskMember{FactorTaskMember: member, Factor: factor}, nil
}

// lockExistingMember 找到成员引用的定义，按固定顺序（定义锁、任务锁）加锁后再确认成员仍引用同一定义。
func (s *Service) lockExistingMember(ctx context.Context, taskID, alias string) (func(), string, error) {
	member, err := s.db.GetMember(ctx, taskID, alias)
	if err != nil {
		return nil, "", err
	}
	unlock, err := s.lockMemberContext(ctx, taskID, member.FactorID)
	if err != nil {
		return nil, "", err
	}
	current, err := s.db.GetMember(ctx, taskID, alias)
	if err != nil || current.FactorID != member.FactorID {
		unlock()
		if err == nil {
			err = fmt.Errorf("%w: member %q changed concurrently", store.ErrConflict, alias)
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
		if column.OriginType == storageio.ColumnOriginFactor {
			origins[column.ColumnName] = column.OriginID
		}
	}
	if err := domain.ValidateMembership(set, member, sourceColumns(columns), siblings, origins); err != nil {
		return nil, err
	}
	return members, nil
}

func requireSetOpen(set domain.FactorTask) error {
	switch set.Status {
	case domain.TaskStatusPending:
		return errors.New("factor task is not ready")
	case domain.TaskStatusDeleting:
		return errors.New("factor task purge is pending")
	}
	return nil
}

func (s *Service) markReady(taskID string, ready bool) {
	if s.recordReady != nil {
		s.recordReady(taskID, ready)
	}
}

func sameTaskIdentity(existing, requested domain.FactorTask) error {
	if existing.SpaceID != requested.SpaceID || existing.SourceDatasetID != requested.SourceDatasetID ||
		existing.Freq != requested.Freq || existing.ResultDatasetID != requested.ResultDatasetID ||
		existing.OwnerInstanceID != requested.OwnerInstanceID {
		return fmt.Errorf("factor task %q conflicts with the requested source identity", requested.TaskID)
	}
	return nil
}

func resultDatasetSpec(set domain.FactorTask, source storageio.DatasetInfo, columns []storageio.ColumnInfo) storageio.ResultDatasetSpec {
	return storageio.ResultDatasetSpec{
		SpaceID: set.SpaceID, DatasetID: set.ResultDatasetID, SourceDatasetID: set.SourceDatasetID,
		Name: resultDatasetName(source.Name), Description: "Factor results for " + source.DatasetID,
		DataSourceID: source.DataSourceID, DataNodeID: source.DataNodeID,
		DataKind: storageio.DataKindTimeSeries, Frequency: set.Freq,
		SubjectTags: append([]string(nil), source.SubjectTags...), Attributes: map[string]string{
			"owner_module": "factor", "dataset_role": storageio.DatasetRoleFactorResult, "source_dataset_id": source.DatasetID, "write_owner": "factor",
		},
		Columns: columns,
	}
}

// maxResultDatasetNameRunes is Storage's display-name limit.
const maxResultDatasetNameRunes = 10

// resultDatasetName derives the result Dataset name from its source name,
// which is unique in the Space, so several factor tasks can coexist.
func resultDatasetName(sourceName string) string {
	const suffix = "因子"
	source := []rune(strings.TrimSpace(sourceName))
	if len(source) == 0 {
		return "因子结果"
	}
	if keep := maxResultDatasetNameRunes - len([]rune(suffix)); len(source) > keep {
		source = source[:keep]
	}
	return string(source) + suffix
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

// 结果列的属性：列归属的成员（origin_factor_id 与 origin_id 都是成员别名，Storage 要求两者一致、factor_output
// 与列名一致）、定义 ID、分类，以及列是输出值还是参数取值。
const (
	ColumnKindOutput = "output"
	ColumnKindParam  = "param"
)

func appendFactorColumns(columns []storageio.ColumnInfo, members ...domain.MemberDef) []storageio.ColumnInfo {
	seen := make(map[string]struct{}, len(columns))
	for _, col := range columns {
		seen[col.ColumnName] = struct{}{}
	}
	add := func(member domain.MemberDef, column, kind, name string) {
		if _, exists := seen[column]; exists {
			return
		}
		seen[column] = struct{}{}
		attributes := map[string]string{
			"display_name": column, "factor_output": column, "origin_factor_id": member.Alias,
			"member_alias": member.Alias, "factor_id": member.Factor.FactorID, "category_id": member.Factor.CategoryID,
			"column_kind": kind,
		}
		if kind == ColumnKindParam {
			attributes["param_name"] = name
		} else {
			attributes["output_name"] = name
		}
		columns = append(columns, storageio.ColumnInfo{
			ColumnName: column, OriginType: storageio.ColumnOriginFactor, OriginID: member.Alias,
			ValueType: storageio.ColumnTypeDouble, Status: storageio.ColumnStatusActive, Attributes: attributes,
		})
	}
	for _, member := range members {
		for i, output := range member.Factor.Outputs {
			add(member, member.OutputColumns()[i], ColumnKindOutput, output)
		}
		names := member.ParamNames()
		for i, column := range member.ParamColumns() {
			add(member, column, ColumnKindParam, names[i])
		}
	}
	return columns
}

func replaceMember(members []domain.MemberDef, next domain.MemberDef) []domain.MemberDef {
	for i, member := range members {
		if member.Alias == next.Alias {
			members[i] = next
			return members
		}
	}
	return append(members, next)
}

// resolveNewMember 决定新成员的固定参数与别名，并校验同任务内同因子同参数不重复。
func (s *Service) resolveNewMember(ctx context.Context, set domain.FactorTask, factor domain.FactorDef, alias string, params map[string]float64) (map[string]float64, string, error) {
	if set.OwnerInstanceID != "" {
		if len(params) > 0 {
			return nil, "", fmt.Errorf("策略实例专属任务的成员参数由实例逐期提交，不能带固定参数")
		}
		if alias == "" {
			alias = factor.FactorID
		}
		return nil, alias, nil
	}
	fixed, err := domain.ResolveMemberParams(factor, params)
	if err != nil {
		return nil, "", err
	}
	if alias == "" {
		if alias, err = domain.DeriveMemberAlias(factor, fixed); err != nil {
			return nil, "", err
		}
	}
	members, err := s.db.ListMembers(ctx, set.TaskID, "")
	if err != nil {
		return nil, "", err
	}
	for _, existing := range members {
		if existing.FactorID == factor.FactorID && sameParams(existing.Params, fixed) {
			return nil, "", fmt.Errorf("%w: 任务 %q 里已有成员 %q 使用因子 %q 的同一组参数", store.ErrConflict, set.TaskID, existing.MemberAlias, factor.FactorID)
		}
	}
	return fixed, alias, nil
}

func sameParams(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		if other, ok := b[name]; !ok || other != value {
			return false
		}
	}
	return true
}
