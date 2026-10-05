package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// SourceChecker asks the Python runtime to load a materialized source version.
type SourceChecker interface {
	CheckSource(ctx context.Context, factor domain.FactorDef, sourcePath string) error
}

// RecalcSubmitter durably accepts an initial historical backfill before the
// factor becomes visible to live triggers.
type RecalcSubmitter interface {
	PrepareEnableBackfill(ctx context.Context, set domain.FactorSet, factor domain.FactorDef, start, end time.Time) (store.RecalcJob, error)
}

type EarliestPeriodProvider interface {
	EarliestDatasetPeriod(ctx context.Context, spaceID, datasetID, freq string) (time.Time, bool, error)
}

type Option func(*Service)

func WithLockDir(path string) Option { return func(s *Service) { s.locks = setlock.New(path) } }
func WithSourceChecker(checker SourceChecker) Option {
	return func(s *Service) { s.sourceChecker = checker }
}
func WithRecalcSubmitter(submitter RecalcSubmitter) Option {
	return func(s *Service) { s.recalc = submitter }
}
func WithEarliestPeriodProvider(provider EarliestPeriodProvider) Option {
	return func(s *Service) { s.earliestPeriod = provider }
}

// WithReadyRecorder receives whether each set's result dataset accepted its
// columns; the compute engine only writes to ready sets.
func WithReadyRecorder(record func(setID string, ready bool)) Option {
	return func(s *Service) { s.recordReady = record }
}
func WithClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

// Service coordinates local factor definitions with Storage metadata.
type Service struct {
	db             *store.Store
	metadata       storageio.Metadata
	sourceChecker  SourceChecker
	recalc         RecalcSubmitter
	earliestPeriod EarliestPeriodProvider
	recordReady    func(setID string, ready bool)
	locks          *setlock.Locks
	now            func() time.Time
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
	if in.SetID == "" {
		in.SetID = domain.SetID(in.SourceDatasetID, in.Freq)
	}
	if in.ResultDatasetID == "" {
		in.ResultDatasetID = domain.ResultDatasetID(in.SourceDatasetID, in.Freq)
	}
	if in.SubjectMode == "" {
		in.SubjectMode = domain.SubjectModeAll
	}
	if in.Status == "" {
		in.Status = domain.SetStatusPending
	}
	if in.Status != domain.SetStatusPending {
		return domain.FactorSet{}, errors.New("new factor sets must start pending")
	}
	if in.Subjects == nil {
		in.Subjects = []string{}
	}
	if err := domain.ValidateSet(in); err != nil {
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

func (s *Service) UpdateSetSubjects(ctx context.Context, setID, mode string, subjects []string) (domain.FactorSet, error) {
	unlock, err := s.locks.LockContext(ctx, strings.TrimSpace(setID))
	if err != nil {
		return domain.FactorSet{}, err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return domain.FactorSet{}, err
	}
	if set.Status == domain.SetStatusPending {
		return domain.FactorSet{}, errors.New("pending factor set cannot be updated")
	}
	if set.Status == domain.SetStatusDeleting {
		return domain.FactorSet{}, errors.New("factor set purge is pending")
	}
	set.SubjectMode, set.Subjects = mode, append([]string(nil), subjects...)
	if set.Subjects == nil {
		set.Subjects = []string{}
	}
	if err := domain.ValidateSet(set); err != nil {
		return domain.FactorSet{}, err
	}
	if err := s.db.UpdateSet(ctx, set); err != nil {
		return domain.FactorSet{}, err
	}
	return s.db.GetSet(ctx, set.SetID)
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
// checks and a trial load of the source; dataset checks wait for AddFactorToSet.
func (s *Service) CreateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	factor, err := normalizeFactor(in)
	if err != nil {
		return domain.FactorDef{}, err
	}
	if err := domain.ValidateDefinition(factor); err != nil {
		return domain.FactorDef{}, err
	}
	unlock, err := s.locks.LockFactorContext(ctx, factor.FactorID)
	if err != nil {
		return domain.FactorDef{}, err
	}
	defer unlock()
	if err := s.loadSource(ctx, factor); err != nil {
		return domain.FactorDef{}, err
	}
	if err := s.db.CreateFactor(ctx, factor); err != nil {
		return domain.FactorDef{}, err
	}
	return s.db.GetFactor(ctx, factor.FactorID)
}

// UpdateFactor rewrites a definition. It is rejected while any member is
// enabled; otherwise the new definition is re-validated against every set that
// references it and any failure rejects the whole update.
func (s *Service) UpdateFactor(ctx context.Context, in domain.FactorDef) (domain.FactorDef, error) {
	factor, err := normalizeFactor(in)
	if err != nil {
		return domain.FactorDef{}, err
	}
	if err := domain.ValidateDefinition(factor); err != nil {
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
	for _, usage := range referencing {
		setIDs = append(setIDs, usage.SetID)
	}
	sort.Strings(setIDs)
	for _, setID := range setIDs {
		unlockSet, err := s.locks.LockContext(ctx, setID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		defer unlockSet()
	}
	for _, setID := range setIDs {
		set, err := s.db.GetSet(ctx, setID)
		if err != nil {
			return domain.FactorDef{}, err
		}
		if _, err := s.validateMembership(ctx, set, factor); err != nil {
			return domain.FactorDef{}, fmt.Errorf("factor set %q: %w", setID, err)
		}
	}
	if err := s.loadSource(ctx, factor); err != nil {
		return domain.FactorDef{}, err
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
// validating it against the set's source dataset. It neither adds result
// columns nor submits a backfill.
func (s *Service) AddFactorToSet(ctx context.Context, setID, factorID string) (domain.SetMember, error) {
	setID, factorID = strings.TrimSpace(setID), strings.TrimSpace(factorID)
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
	if _, err := s.validateMembership(ctx, set, factor); err != nil {
		return domain.SetMember{}, err
	}
	member, err := s.db.AddMember(ctx, setID, factorID)
	if err != nil {
		return domain.SetMember{}, err
	}
	return domain.SetMember{FactorSetMember: member, Factor: factor}, nil
}

// RemoveFactorFromSet detaches a disabled member; result columns are kept.
func (s *Service) RemoveFactorFromSet(ctx context.Context, setID, factorID string) error {
	setID, factorID = strings.TrimSpace(setID), strings.TrimSpace(factorID)
	unlock, err := s.lockMemberContext(ctx, setID, factorID)
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
	return s.db.RemoveMember(ctx, setID, factorID)
}

// SetFactorMemberStatus enables or disables one member. Enabling re-validates,
// adds result columns, accepts a backfill and returns its job id; disabling only
// stops writes and keeps the columns.
func (s *Service) SetFactorMemberStatus(ctx context.Context, setID, factorID, status string) (domain.SetMember, string, error) {
	setID, factorID = strings.TrimSpace(setID), strings.TrimSpace(factorID)
	unlock, err := s.lockMemberContext(ctx, setID, factorID)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	defer unlock()
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	if set.Status == domain.SetStatusDeleting {
		return domain.SetMember{}, "", errors.New("factor set purge is pending")
	}
	if status != domain.MemberStatusEnabled && status != domain.MemberStatusDisabled {
		return domain.SetMember{}, "", fmt.Errorf("status must be %q or %q", domain.MemberStatusEnabled, domain.MemberStatusDisabled)
	}
	member, err := s.db.GetMember(ctx, setID, factorID)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	factor, err := s.db.GetFactor(ctx, factorID)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	result := domain.SetMember{FactorSetMember: member, Factor: factor}
	if member.Status == status {
		return result, "", nil
	}
	if status == domain.MemberStatusDisabled {
		if err := s.db.SetMemberStatus(ctx, setID, factorID, member.Status, status); err != nil {
			return domain.SetMember{}, "", err
		}
		return s.reloadMember(ctx, setID, factorID, factor)
	}
	if set.Status == domain.SetStatusPending {
		return domain.SetMember{}, "", errors.New("factor set is not ready")
	}
	if s.recalc == nil {
		return domain.SetMember{}, "", errors.New("recalc submitter is required to enable a factor")
	}
	// Definitions may have been edited and source columns may have changed while
	// the member was disabled, so enabling repeats the full membership checks.
	members, err := s.validateMembership(ctx, set, factor)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	source, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	start, end, err := s.backfillWindow(ctx, set)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	activeFactors := make([]domain.FactorDef, 0, len(members)+1)
	for _, candidate := range members {
		if candidate.Status == domain.MemberStatusEnabled {
			activeFactors = append(activeFactors, candidate.Factor)
		}
	}
	activeFactors = replaceFactor(activeFactors, factor)
	if err := s.ensureResultColumns(ctx, set, source, columns, activeFactors...); err != nil {
		return domain.SetMember{}, "", err
	}
	s.markReady(set.SetID, true)
	// Persist the accepted backfill and expose the member in one SQLite
	// transaction so neither the worker nor live triggers can observe half of
	// the activation.
	job, err := s.recalc.PrepareEnableBackfill(ctx, set, factor, start, end)
	if err != nil {
		return domain.SetMember{}, "", fmt.Errorf("prepare factor backfill: %w", err)
	}
	if _, err := s.db.EnableMemberWithRecalcJob(ctx, setID, factorID, member.Status, job); err != nil {
		return domain.SetMember{}, "", err
	}
	updated, _, err := s.reloadMember(ctx, setID, factorID, factor)
	return updated, job.JobID, err
}

func (s *Service) reloadMember(ctx context.Context, setID, factorID string, factor domain.FactorDef) (domain.SetMember, string, error) {
	member, err := s.db.GetMember(ctx, setID, factorID)
	if err != nil {
		return domain.SetMember{}, "", err
	}
	return domain.SetMember{FactorSetMember: member, Factor: factor}, "", nil
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

// validateMembership runs the dataset-dependent checks of one definition
// against one set and returns the set's current members.
func (s *Service) validateMembership(ctx context.Context, set domain.FactorSet, factor domain.FactorDef) ([]domain.SetMember, error) {
	_, columns, err := s.sourceDataset(ctx, set)
	if err != nil {
		return nil, err
	}
	members, err := s.db.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return nil, err
	}
	siblings := make([]domain.FactorDef, 0, len(members))
	for _, member := range members {
		siblings = append(siblings, member.Factor)
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
	if err := domain.ValidateMembership(set, factor, columnNames(columns), siblings, origins); err != nil {
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

func (s *Service) loadSource(ctx context.Context, factor domain.FactorDef) error {
	if s.sourceChecker == nil {
		return errors.New("factor source checker is required")
	}
	tmp, err := os.CreateTemp("", "moox-factor-check-*.py")
	if err != nil {
		return fmt.Errorf("create temporary factor source: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(factor.SourceCode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary factor source: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary factor source: %w", err)
	}
	if err := s.sourceChecker.CheckSource(ctx, factor, tmpPath); err != nil {
		return fmt.Errorf("load factor source: %w", err)
	}
	return nil
}

func (s *Service) backfillWindow(ctx context.Context, set domain.FactorSet) (time.Time, time.Time, error) {
	if s.now == nil {
		return time.Time{}, time.Time{}, errors.New("catalog clock is required")
	}
	now := s.now().UTC()
	clock, err := periodclock.ForSpace(set.SpaceID)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	duration, err := clock.Duration(set.Freq)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	periodSeconds := int64(duration / time.Second)
	if periodSeconds <= 0 {
		return time.Time{}, time.Time{}, errors.New("factor period duration is invalid")
	}
	endUnix := now.Unix() - now.Unix()%periodSeconds
	if now.Unix() < 0 && now.Unix()%periodSeconds != 0 {
		endUnix -= periodSeconds
	}
	end := time.Unix(endUnix, 0).UTC()
	dataset, err := s.metadata.GetDataset(ctx, set.SpaceID, set.ResultDatasetID)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("get result dataset retention: %w", err)
	}
	var start time.Time
	keepDuration := strings.TrimSpace(dataset.KeepDuration)
	switch keepDuration {
	case "0":
		if s.earliestPeriod == nil {
			return time.Time{}, time.Time{}, errors.New("earliest source period provider is required for unlimited retention backfill")
		}
		var found bool
		start, found, err = s.earliestPeriod.EarliestDatasetPeriod(ctx, set.SpaceID, set.SourceDatasetID, set.Freq)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("find earliest source period: %w", err)
		}
		if found {
			start = floorPeriod(start, periodSeconds)
		} else {
			// An empty source has no historical range to backfill. Keep the
			// accepted job bounded to the most recent completed period.
			start = end.Add(-duration)
		}
	default:
		keep, parseErr := time.ParseDuration(keepDuration)
		if parseErr != nil || keep <= 0 {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid result dataset keep_duration %q", keepDuration)
		}
		start = now.Add(-keep)
		start = floorPeriod(start, periodSeconds)
	}
	if _, err := clock.Align(start, set.Freq); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align factor backfill start: %w", err)
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("factor backfill window is empty")
	}
	return start, end, nil
}

func floorPeriod(value time.Time, periodSeconds int64) time.Time {
	seconds := value.Unix()
	remainder := seconds % periodSeconds
	if remainder < 0 {
		remainder += periodSeconds
	}
	return time.Unix(seconds-remainder, 0).UTC()
}

func (s *Service) markReady(setID string, ready bool) {
	if s.recordReady != nil {
		s.recordReady(setID, ready)
	}
}

func normalizeFactor(factor domain.FactorDef) (domain.FactorDef, error) {
	normalized, err := domain.NormalizeFactorDefinition(factor)
	if err != nil {
		return domain.FactorDef{}, err
	}
	if normalized.SourceHash == "" {
		normalized.SourceHash = domain.SourceHash(normalized.SourceCode)
	}
	if normalized.SourceHash != domain.SourceHash(normalized.SourceCode) {
		return domain.FactorDef{}, errors.New("source_hash does not match source_code")
	}
	return normalized, nil
}

func sameSetIdentity(existing, requested domain.FactorSet) error {
	if existing.SpaceID != requested.SpaceID || existing.SourceDatasetID != requested.SourceDatasetID ||
		existing.Freq != requested.Freq || existing.ResultDatasetID != requested.ResultDatasetID {
		return fmt.Errorf("factor set %q conflicts with the requested source identity", requested.SetID)
	}
	return nil
}

func resultDatasetSpec(set domain.FactorSet, source storageio.DatasetInfo, columns []storageio.ColumnInfo) storageio.ResultDatasetSpec {
	return storageio.ResultDatasetSpec{
		SpaceID: set.SpaceID, DatasetID: set.ResultDatasetID, SourceDatasetID: set.SourceDatasetID,
		Name: "因子结果", Description: "Factor results for " + source.DatasetID,
		DataSourceID: source.DataSourceID, DataNodeID: source.DataNodeID,
		DataKind: storageio.DataKindTimeSeries, Frequency: set.Freq, KeepDuration: source.KeepDuration,
		SubjectTags: append([]string(nil), source.SubjectTags...), Attributes: map[string]string{
			"owner_module": "factor", "dataset_role": storageio.DatasetRoleFactorResult, "source_dataset_id": source.DatasetID, "write_owner": "factor",
		},
		Columns: columns,
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

func columnNames(columns []storageio.ColumnInfo) []string {
	out := make([]string, 0, len(columns))
	for _, col := range columns {
		out = append(out, col.ColumnName)
	}
	return out
}

func appendFactorColumns(columns []storageio.ColumnInfo, factors ...domain.FactorDef) []storageio.ColumnInfo {
	seen := make(map[string]struct{}, len(columns))
	for _, col := range columns {
		seen[col.ColumnName] = struct{}{}
	}
	for _, factor := range factors {
		for _, output := range factor.Outputs {
			if _, exists := seen[output]; exists {
				continue
			}
			seen[output] = struct{}{}
			columns = append(columns, storageio.ColumnInfo{
				ColumnName: output, OriginType: storageio.ColumnOriginFactor, OriginID: factor.FactorID,
				ValueType: storageio.ColumnTypeDouble, Status: storageio.ColumnStatusActive,
				Attributes: map[string]string{"display_name": output, "factor_output": output, "origin_factor_id": factor.FactorID},
			})
		}
	}
	return columns
}

func replaceFactor(factors []domain.FactorDef, next domain.FactorDef) []domain.FactorDef {
	for i, factor := range factors {
		if factor.FactorID == next.FactorID {
			factors[i] = next
			return factors
		}
	}
	return append(factors, next)
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
