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
func WithReadyRecorder(record func(setID string, ready bool)) Option {
	return func(s *Service) { s.recordReady = record }
}

// Service coordinates local factor definitions with Storage metadata.
type Service struct {
	db          *store.Store
	metadata    storageio.Metadata
	recordReady func(setID string, ready bool)
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

func (s *Service) CreateSet(ctx context.Context, in domain.FactorSet) (domain.FactorSet, error) {
	if s == nil || s.db == nil || s.metadata == nil {
		return domain.FactorSet{}, errors.New("factor store and Storage metadata are required")
	}
	if strings.TrimSpace(in.Freq) == "" {
		return domain.FactorSet{}, errors.New("freq is required")
	}
	in.SpaceID = strings.TrimSpace(in.SpaceID)
	in.SourceDatasetID = strings.TrimSpace(in.SourceDatasetID)
	in.Freq = strings.ToLower(strings.TrimSpace(in.Freq))
	in.OwnerInstanceID = strings.TrimSpace(in.OwnerInstanceID)
	if in.SetID == "" {
		in.SetID = domain.SetID(in.SourceDatasetID, in.Freq)
		if in.OwnerInstanceID != "" {
			in.SetID = domain.InstanceSetID(in.OwnerInstanceID)
		}
	}
	if in.ResultDatasetID == "" {
		in.ResultDatasetID = domain.ResultDatasetID(in.SetID)
	}
	if in.Status == "" {
		in.Status = domain.SetStatusPending
	}
	if in.Status != domain.SetStatusPending {
		return domain.FactorSet{}, errors.New("new factor sets must start pending")
	}
	if err := domain.ValidateSet(in); err != nil {
		return domain.FactorSet{}, err
	}
	// 启用成员时才会取空间日历；不支持的空间在创建时就拒绝，避免出现永远无法启用的计算任务。
	if _, err := periodclock.ForSpace(in.SpaceID); err != nil {
		return domain.FactorSet{}, err
	}
	unlock, err := s.locks.LockContext(ctx, in.SetID)
	if err != nil {
		return domain.FactorSet{}, err
	}
	defer unlock()
	existing, err := s.db.GetSet(ctx, in.SetID)
	newSet := errors.Is(err, gorm.ErrRecordNotFound)
	if err == nil {
		if err := sameSetIdentity(existing, in); err != nil {
			return domain.FactorSet{}, err
		}
		if existing.Status == domain.SetStatusDeleting {
			return domain.FactorSet{}, errors.New("factor set purge is pending")
		}
		if existing.Status != domain.SetStatusPending {
			return existing, nil
		}
		in = existing
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.FactorSet{}, err
	}
	source, columns, err := s.sourceDataset(ctx, in)
	if err != nil {
		return domain.FactorSet{}, err
	}
	if newSet {
		if err := s.db.CreateSet(ctx, in); err != nil {
			return domain.FactorSet{}, err
		}
	}

	spec := resultDatasetSpec(in, source, columns)
	if in.OwnerInstanceID != "" {
		spec.Name = domain.InstanceDatasetName(in.OwnerInstanceID)
		spec.Description = "Private factor results of strategy instance " + in.OwnerInstanceID
		spec.Attributes["owner_instance_id"] = in.OwnerInstanceID
	}
	if err := s.metadata.CreateResultDataset(ctx, spec); err != nil {
		return domain.FactorSet{}, fmt.Errorf("create factor result dataset: %w", err)
	}
	if err := s.metadata.ActivateDataset(ctx, in.SpaceID, in.ResultDatasetID); err != nil {
		return domain.FactorSet{}, fmt.Errorf("activate factor result dataset: %w", err)
	}
	if err := s.db.SetSetStatus(ctx, in.SetID, domain.SetStatusPending, domain.SetStatusEnabled); err != nil {
		return domain.FactorSet{}, fmt.Errorf("enable factor set after result dataset activation: %w", err)
	}
	s.markReady(in.SetID, true)
	return s.db.GetSet(ctx, in.SetID)
}

func (s *Service) SetSetStatus(ctx context.Context, setID, status string) (domain.FactorSet, error) {
	setID = strings.TrimSpace(setID)
	unlock, err := s.locks.LockContext(ctx, setID)
	if err != nil {
		return domain.FactorSet{}, err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return domain.FactorSet{}, err
	}
	if status != domain.SetStatusEnabled && status != domain.SetStatusDisabled {
		return domain.FactorSet{}, fmt.Errorf("status must be %q or %q", domain.SetStatusEnabled, domain.SetStatusDisabled)
	}
	if set.Status == domain.SetStatusDeleting {
		return domain.FactorSet{}, errors.New("factor set purge is pending")
	}
	if set.Status == status {
		return set, nil
	}
	if set.Status == domain.SetStatusPending {
		return domain.FactorSet{}, errors.New("pending factor set cannot be toggled")
	}
	if status == domain.SetStatusEnabled {
		if err := s.reconcileSetUnlocked(ctx, set); err != nil {
			return domain.FactorSet{}, err
		}
	}
	if err := s.db.SetSetStatus(ctx, set.SetID, set.Status, status); err != nil {
		return domain.FactorSet{}, err
	}
	return s.db.GetSet(ctx, set.SetID)
}

func (s *Service) DeleteSet(ctx context.Context, setID string, purge bool) error {
	setID = strings.TrimSpace(setID)
	unlock, err := s.locks.LockContext(ctx, setID)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return err
	}
	if set.Status != domain.SetStatusDisabled && !(purge && set.Status == domain.SetStatusDeleting) {
		return errors.New("factor set must be disabled before deletion")
	}
	memberCount, err := s.db.CountMembers(ctx, setID)
	if err != nil {
		return err
	}
	if memberCount != 0 {
		return errors.New("factor set must not contain members before deletion")
	}
	if purge {
		if s.metadata == nil {
			return errors.New("Storage metadata is required to purge the result dataset")
		}
		if set.Status != domain.SetStatusDeleting {
			if err := s.db.SetSetStatus(ctx, set.SetID, domain.SetStatusDisabled, domain.SetStatusDeleting); err != nil {
				return fmt.Errorf("persist factor result purge intent: %w", err)
			}
		}
		if err := s.metadata.DeleteDataset(ctx, set.SpaceID, set.ResultDatasetID); err != nil {
			return fmt.Errorf("delete factor result dataset: %w", err)
		}
	}
	// 参数记录最后删：结果数据集清理失败（例如还有额外的 View 引用）时集合停在 deleting，冻结历史还在，可以重试。
	if err := s.db.DeleteSetParams(ctx, setID); err != nil {
		return fmt.Errorf("delete factor set params: %w", err)
	}
	if err := s.db.DeleteSet(ctx, setID); err != nil {
		return err
	}
	return nil
}

// GetSet returns a factor set with all of its members (any status) and their definitions.
func (s *Service) GetSet(ctx context.Context, setID string) (domain.FactorSet, []domain.SetMember, error) {
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return domain.FactorSet{}, nil, err
	}
	members, err := s.db.ListMembers(ctx, setID, "")
	return set, members, err
}

func (s *Service) ListSets(ctx context.Context) ([]domain.FactorSet, error) {
	return s.db.ListSets(ctx)
}

// CreateFactor registers a standalone definition. It only runs the static
// checks (compiling the spec); dataset checks wait for AddFactorToSet.
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
				store.ErrConflict, factor.FactorID, usage.SetID)
		}
	}
	setIDs := make([]string, 0, len(referencing))
	seenSets := map[string]bool{}
	for _, usage := range referencing {
		if !seenSets[usage.SetID] {
			seenSets[usage.SetID] = true
			setIDs = append(setIDs, usage.SetID)
		}
	}
	sort.Strings(setIDs)
	for _, setID := range setIDs {
		unlockSet, err := s.locks.LockContext(ctx, setID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		defer unlockSet()
	}
	for _, usage := range referencing {
		set, err := s.db.GetSet(ctx, usage.SetID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		// 成员保存的固定参数在新定义上必须仍然合法（参数名仍声明、类型与范围仍成立）。
		if err := domain.CheckMemberParams(factor, usage.Params); err != nil {
			return domain.FactorDef{}, fmt.Errorf("factor set %q member %q 的固定参数不再合法：%w", usage.SetID, usage.MemberAlias, err)
		}
		if _, err := s.validateMembership(ctx, set, domain.MemberDef{Alias: usage.MemberAlias, Factor: factor, Params: usage.Params}); err != nil {
			return domain.FactorDef{}, fmt.Errorf("factor set %q member %q: %w", usage.SetID, usage.MemberAlias, err)
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
		return fmt.Errorf("%w: factor %q is still used by factor set %q", store.ErrConflict, factorID, used[0].SetID)
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
// (optionally filtered by member status) when setID is given. Each entry carries
// all of its usages.
func (s *Service) ListFactors(ctx context.Context, setID, status string) ([]domain.FactorInfo, error) {
	var factors []domain.FactorDef
	if strings.TrimSpace(setID) == "" {
		all, err := s.db.ListFactors(ctx)
		if err != nil {
			return nil, err
		}
		factors = all
	} else {
		if _, err := s.db.GetSet(ctx, setID); err != nil {
			return nil, err
		}
		members, err := s.db.ListMembers(ctx, setID, status)
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

// AddFactorToSet attaches a definition to a set as a disabled member after
// validating it against the set's source dataset. It neither adds result columns
// nor submits a backfill.
//
// 非策略专属任务的成员带固定参数：params 给出的参数加上定义的默认值补全成完整参数并校验，之后不变；别名为空时按参数生成
// （全默认沿用因子 ID）；同任务内同因子同参数只能有一个成员。策略专属任务的参数由写入方逐期提交，不能带固定参数，
// 别名为空时取因子 ID。
func (s *Service) AddFactorToSet(ctx context.Context, setID, alias, factorID string, params map[string]float64) (domain.SetMember, error) {
	setID, alias, factorID = strings.TrimSpace(setID), strings.TrimSpace(alias), strings.TrimSpace(factorID)
	unlock, err := s.lockMemberContext(ctx, setID, factorID)
	if err != nil {
		return domain.SetMember{}, err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return domain.SetMember{}, err
	}
	if err := requireSetOpen(set); err != nil {
		return domain.SetMember{}, err
	}
	// Re-read the definition under the locks so a concurrent update cannot interleave.
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.SetMember{}, err
	}
	fixed, alias, err := s.resolveNewMember(ctx, set, factor, alias, params)
	if err != nil {
		return domain.SetMember{}, err
	}
	if _, err := s.validateMembership(ctx, set, domain.MemberDef{Alias: alias, Factor: factor, Params: fixed}); err != nil {
		return domain.SetMember{}, err
	}
	member, err := s.db.AddMember(ctx, setID, alias, factorID, fixed)
	if err != nil {
		return domain.SetMember{}, err
	}
	return domain.SetMember{FactorSetMember: member, Factor: factor}, nil
}

// RemoveFactorFromSet detaches a disabled member; result columns are kept.
func (s *Service) RemoveFactorFromSet(ctx context.Context, setID, alias string) error {
	setID, alias = strings.TrimSpace(setID), strings.TrimSpace(alias)
	unlock, _, err := s.lockExistingMember(ctx, setID, alias)
	if err != nil {
		return err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return err
	}
	if set.Status == domain.SetStatusDeleting {
		return errors.New("factor set purge is pending")
	}
	return s.db.RemoveMember(ctx, setID, alias)
}

// SetFactorMemberStatus enables or disables one member. Enabling re-validates
// and adds result columns, but does not backfill history; disabling only stops
// writes and keeps the columns.
func (s *Service) SetFactorMemberStatus(ctx context.Context, setID, alias, status string) (domain.SetMember, error) {
	setID, alias = strings.TrimSpace(setID), strings.TrimSpace(alias)
	unlock, factorID, err := s.lockExistingMember(ctx, setID, alias)
	if err != nil {
		return domain.SetMember{}, err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return domain.SetMember{}, err
	}
	if set.Status == domain.SetStatusDeleting {
		return domain.SetMember{}, errors.New("factor set purge is pending")
	}
	if status != domain.MemberStatusEnabled && status != domain.MemberStatusDisabled {
		return domain.SetMember{}, fmt.Errorf("status must be %q or %q", domain.MemberStatusEnabled, domain.MemberStatusDisabled)
	}
	member, err := s.db.GetMember(ctx, setID, alias)
	if err != nil {
		return domain.SetMember{}, err
	}
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.SetMember{}, err
	}
	result := domain.SetMember{FactorSetMember: member, Factor: factor}
	if member.Status == status {
		return result, nil
	}
	if status == domain.MemberStatusDisabled {
		if err := s.db.SetMemberStatus(ctx, setID, alias, member.Status, status); err != nil {
			return domain.SetMember{}, err
		}
		return s.reloadMember(ctx, setID, alias, factor)
	}
	if set.Status == domain.SetStatusPending {
		return domain.SetMember{}, errors.New("factor set is not ready")
	}
	// Definitions may have been edited and source columns may have changed while
	// the member was disabled, so enabling repeats the full membership checks.
	enabling := domain.MemberDef{Alias: alias, Factor: factor, Params: member.Params}
	members, err := s.validateMembership(ctx, set, enabling)
	if err != nil {
		return domain.SetMember{}, err
	}
	source, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return domain.SetMember{}, err
	}
	active := make([]domain.MemberDef, 0, len(members)+1)
	for _, candidate := range members {
		if candidate.Status == domain.MemberStatusEnabled {
			active = append(active, candidate.Def())
		}
	}
	active = replaceMember(active, enabling)
	if err := s.ensureResultColumns(ctx, set, source, columns, active...); err != nil {
		return domain.SetMember{}, err
	}
	s.markReady(set.SetID, true)
	// 启用只让成员参与此后每期的计算，不自动补算历史：需要历史因子值时用手动补算（recalc）指定范围。
	if err := s.db.SetMemberStatus(ctx, setID, alias, member.Status, status); err != nil {
		return domain.SetMember{}, err
	}
	return s.reloadMember(ctx, setID, alias, factor)
}

func (s *Service) reloadMember(ctx context.Context, setID, alias string, factor domain.FactorDef) (domain.SetMember, error) {
	member, err := s.db.GetMember(ctx, setID, alias)
	if err != nil {
		return domain.SetMember{}, err
	}
	return domain.SetMember{FactorSetMember: member, Factor: factor}, nil
}

// lockExistingMember 找到成员引用的定义，按固定顺序（定义锁、集合锁）加锁后再确认成员仍引用同一定义。
func (s *Service) lockExistingMember(ctx context.Context, setID, alias string) (func(), string, error) {
	member, err := s.db.GetMember(ctx, setID, alias)
	if err != nil {
		return nil, "", err
	}
	unlock, err := s.lockMemberContext(ctx, setID, member.FactorID)
	if err != nil {
		return nil, "", err
	}
	current, err := s.db.GetMember(ctx, setID, alias)
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
func (s *Service) lockMemberContext(ctx context.Context, setID, factorID string) (func(), error) {
	unlockFactor, err := s.locks.LockFactorContext(ctx, factorID)
	if err != nil {
		return nil, err
	}
	unlockSet, err := s.locks.LockContext(ctx, setID)
	if err != nil {
		unlockFactor()
		return nil, err
	}
	return func() {
		unlockSet()
		unlockFactor()
	}, nil
}

// validateMembership runs the dataset-dependent checks of one member against
// one set and returns the set's current members.
func (s *Service) validateMembership(ctx context.Context, set domain.FactorSet, member domain.MemberDef) ([]domain.SetMember, error) {
	_, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return nil, err
	}
	members, err := s.db.ListMembers(ctx, set.SetID, "")
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

func requireSetOpen(set domain.FactorSet) error {
	switch set.Status {
	case domain.SetStatusPending:
		return errors.New("factor set is not ready")
	case domain.SetStatusDeleting:
		return errors.New("factor set purge is pending")
	}
	return nil
}

func (s *Service) markReady(setID string, ready bool) {
	if s.recordReady != nil {
		s.recordReady(setID, ready)
	}
}

func sameSetIdentity(existing, requested domain.FactorSet) error {
	if existing.SpaceID != requested.SpaceID || existing.SourceDatasetID != requested.SourceDatasetID ||
		existing.Freq != requested.Freq || existing.ResultDatasetID != requested.ResultDatasetID ||
		existing.OwnerInstanceID != requested.OwnerInstanceID {
		return fmt.Errorf("factor set %q conflicts with the requested source identity", requested.SetID)
	}
	return nil
}

func resultDatasetSpec(set domain.FactorSet, source storageio.DatasetInfo, columns []storageio.ColumnInfo) storageio.ResultDatasetSpec {
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
// which is unique in the Space, so several factor sets can coexist.
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
func (s *Service) resolveNewMember(ctx context.Context, set domain.FactorSet, factor domain.FactorDef, alias string, params map[string]float64) (map[string]float64, string, error) {
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
	members, err := s.db.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return nil, "", err
	}
	for _, existing := range members {
		if existing.FactorID == factor.FactorID && sameParams(existing.Params, fixed) {
			return nil, "", fmt.Errorf("%w: 任务 %q 里已有成员 %q 使用因子 %q 的同一组参数", store.ErrConflict, set.SetID, existing.MemberAlias, factor.FactorID)
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
