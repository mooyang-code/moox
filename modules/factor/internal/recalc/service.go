package recalc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

const defaultChunkPeriods = 2000

type Runner interface {
	Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
}

type ColumnProvider interface {
	DatasetColumns(context.Context, string, string) ([]string, error)
}

type SubjectProvider interface {
	ListDatasetSubjects(context.Context, string, string) ([]string, error)
}

type config struct {
	chunkPeriods int
	pollInterval time.Duration
	chunkRetries int
	retryBackoff time.Duration
	clock        periodclock.Clock
	locks        *catalog.Locks
	columns      ColumnProvider
	subjects     SubjectProvider
}

type Option func(*config)

func WithChunkPeriods(n int) Option           { return func(cfg *config) { cfg.chunkPeriods = n } }
func WithPollInterval(d time.Duration) Option { return func(cfg *config) { cfg.pollInterval = d } }
func WithChunkRetry(attempts int, backoff time.Duration) Option {
	return func(cfg *config) { cfg.chunkRetries, cfg.retryBackoff = attempts, backoff }
}
func WithClock(clock periodclock.Clock) Option { return func(cfg *config) { cfg.clock = clock } }
func WithLocks(locks *catalog.Locks) Option    { return func(cfg *config) { cfg.locks = locks } }
func WithColumnProvider(provider ColumnProvider) Option {
	return func(cfg *config) { cfg.columns = provider }
}
func WithSubjectProvider(provider SubjectProvider) Option {
	return func(cfg *config) { cfg.subjects = provider }
}

func defaultConfig(options []Option) config {
	cfg := config{chunkPeriods: defaultChunkPeriods, pollInterval: time.Second, chunkRetries: 3, retryBackoff: 5 * time.Second}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	if cfg.chunkPeriods <= 0 {
		cfg.chunkPeriods = defaultChunkPeriods
	}
	if cfg.chunkRetries <= 0 {
		cfg.chunkRetries = 1
	}
	if cfg.pollInterval <= 0 {
		cfg.pollInterval = time.Second
	}
	return cfg
}

// Service owns durable request acceptance and inspection. A nil Runner still
// permits a control-only process to submit jobs for a separate worker.
type Service struct {
	db     *store.Store
	worker *Worker
}

func NewService(db *store.Store, runner Runner, options ...Option) *Service {
	return &Service{db: db, worker: NewWorker(db, runner, options...)}
}

func (s *Service) Submit(ctx context.Context, setID string, factorIDs, subjects []string, requestID string, start, end time.Time) (store.RecalcJob, error) {
	if s == nil || s.db == nil || s.worker == nil {
		return store.RecalcJob{}, errors.New("factor recalc store is required")
	}
	requestID = strings.TrimSpace(requestID)
	setID = strings.TrimSpace(setID)
	if requestID == "" || setID == "" {
		return store.RecalcJob{}, errors.New("request_id and set_id are required")
	}
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	clock, err := s.worker.clockFor(set.SpaceID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	start, end, err = alignRange(clock, set.Freq, start, end)
	if err != nil {
		return store.RecalcJob{}, err
	}
	if existing, found, err := s.db.FindRecalcJobByRequestID(ctx, requestID); err != nil {
		return store.RecalcJob{}, err
	} else if found {
		if !matchesSubmitRequest(existing, setID, factorIDs, subjects, start.Unix(), end.Unix()) {
			return store.RecalcJob{}, fmt.Errorf("request_id %q already belongs to a different recalc request", requestID)
		}
		return existing, nil
	}
	set, selected, normalizedSubjects, clock, err := s.worker.selection(ctx, setID, factorIDs, subjects)
	if err != nil {
		return store.RecalcJob{}, err
	}
	start, end, err = alignRange(clock, set.Freq, start, end)
	if err != nil {
		return store.RecalcJob{}, err
	}
	ids := make([]string, 0, len(selected))
	for _, factor := range selected {
		ids = append(ids, factor.FactorID)
	}
	job, err := s.db.CreateRecalcJob(ctx, store.RecalcJob{
		JobID: requestID, RequestID: requestID, SetID: setID,
		FactorIDs: ids, Subjects: normalizedSubjects,
		FactorsOmitted: len(factorIDs) == 0, SubjectsOmitted: len(subjects) == 0,
		StartTime: start.Unix(), EndTime: end.Unix(), Status: store.RecalcStatusAccepted,
	})
	if err != nil {
		return store.RecalcJob{}, err
	}
	if !matchesSubmitRequest(job, setID, factorIDs, subjects, start.Unix(), end.Unix()) {
		return store.RecalcJob{}, fmt.Errorf("request_id %q already belongs to a different recalc request", requestID)
	}
	return job, nil
}

func matchesSubmitRequest(job store.RecalcJob, setID string, factorIDs, subjects []string, start, end int64) bool {
	if job.SetID != setID || job.StartTime != start || job.EndTime != end ||
		job.FactorsOmitted != (len(factorIDs) == 0) || job.SubjectsOmitted != (len(subjects) == 0) {
		return false
	}
	if !job.FactorsOmitted {
		normalized, err := normalizeIDs(factorIDs)
		if err != nil || !sameStrings(job.FactorIDs, normalized) {
			return false
		}
	}
	if !job.SubjectsOmitted {
		normalized, err := normalizeSubjects(subjects)
		if err != nil || !sameStrings(job.Subjects, normalized) {
			return false
		}
	}
	return true
}

// PrepareEnableBackfill validates and builds the accepted job that must be
// committed atomically with enabling a disabled set member in the catalog store.
func (s *Service) PrepareEnableBackfill(ctx context.Context, setID, factorID, requestID string, start, end time.Time) (store.RecalcJob, error) {
	if s == nil || s.db == nil || s.worker == nil {
		return store.RecalcJob{}, errors.New("factor recalc store is required")
	}
	setID = strings.TrimSpace(setID)
	factorID = strings.TrimSpace(factorID)
	requestID = strings.TrimSpace(requestID)
	if setID == "" || factorID == "" || requestID == "" {
		return store.RecalcJob{}, errors.New("request_id, set_id and factor_id are required")
	}
	set, err := s.db.GetSet(ctx, setID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	if set.Status != domain.SetStatusEnabled {
		return store.RecalcJob{}, fmt.Errorf("factor set %q is not enabled", set.SetID)
	}
	member, err := s.db.GetMember(ctx, setID, factorID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	if member.Status != domain.MemberStatusDisabled {
		return store.RecalcJob{}, fmt.Errorf("factor %q must be a disabled member of set %q before activation", factorID, setID)
	}
	clock, err := s.worker.clockFor(set.SpaceID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	start, end, err = alignRange(clock, set.Freq, start, end)
	if err != nil {
		return store.RecalcJob{}, err
	}
	subjects, err := s.worker.resolveSubjects(ctx, set, nil)
	if err != nil {
		return store.RecalcJob{}, err
	}
	return store.RecalcJob{
		JobID: requestID, RequestID: requestID, SetID: setID,
		FactorIDs: []string{factorID}, Subjects: subjects,
		StartTime: start.Unix(), EndTime: end.Unix(), Status: store.RecalcStatusAccepted,
	}, nil
}

func (s *Service) Get(ctx context.Context, jobID string) (store.RecalcJob, error) {
	if s == nil || s.db == nil {
		return store.RecalcJob{}, errors.New("factor recalc store is required")
	}
	return s.db.GetRecalcJob(ctx, jobID)
}

func (s *Service) List(ctx context.Context, setID string, statuses []string) ([]store.RecalcJob, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("factor recalc store is required")
	}
	return s.db.ListRecalcJobs(ctx, store.RecalcJobFilter{SetID: setID, Statuses: statuses})
}

func (s *Service) Cancel(ctx context.Context, jobID string) (store.RecalcJob, error) {
	if s == nil || s.db == nil {
		return store.RecalcJob{}, errors.New("factor recalc store is required")
	}
	return s.db.UpdateRecalcJob(ctx, jobID, func(job *store.RecalcJob) error {
		switch job.Status {
		case store.RecalcStatusSucceeded, store.RecalcStatusFailed, store.RecalcStatusCancelled:
			return nil
		default:
			job.Status = store.RecalcStatusCancelled
			return nil
		}
	})
}

// RunJob executes one accepted or interrupted job synchronously. Progress is
// advanced only after an entire chunk has returned successfully.
func (s *Service) RunJob(ctx context.Context, jobID string) error {
	if s == nil || s.worker == nil {
		return errors.New("factor recalc worker is required")
	}
	return s.worker.Run(ctx, jobID)
}

func (s *Service) RunPending(ctx context.Context) error {
	if s == nil || s.worker == nil {
		return errors.New("factor recalc worker is required")
	}
	return s.worker.RunPending(ctx)
}

// Start resumes durable accepted/running jobs and polls for newly accepted
// requests. The returned stop function joins the worker before returning.
func (s *Service) Start(ctx context.Context) (func() error, error) {
	if s == nil || s.worker == nil || s.worker.runner == nil {
		return nil, errors.New("factor recalc worker runner is required")
	}
	if ctx == nil {
		return nil, errors.New("factor recalc context is required")
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(s.worker.cfg.pollInterval)
		defer ticker.Stop()
		for {
			_ = s.worker.RunPending(runCtx)
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	return func() error {
		once.Do(func() {
			cancel()
			<-done
		})
		return nil
	}, nil
}

type selection struct {
	set      domain.FactorSet
	factors  []domain.FactorDef
	subjects []string
	columns  []string
	clock    periodclock.Clock
}

func (w *Worker) selection(ctx context.Context, setID string, factorIDs, subjects []string) (domain.FactorSet, []domain.FactorDef, []string, periodclock.Clock, error) {
	if w == nil || w.db == nil {
		return domain.FactorSet{}, nil, nil, nil, errors.New("factor store is required")
	}
	set, err := w.db.GetSet(ctx, setID)
	if err != nil {
		return domain.FactorSet{}, nil, nil, nil, err
	}
	if set.Status != domain.SetStatusEnabled {
		return domain.FactorSet{}, nil, nil, nil, fmt.Errorf("factor set %q is not enabled", set.SetID)
	}
	clock, err := w.clockFor(set.SpaceID)
	if err != nil {
		return domain.FactorSet{}, nil, nil, nil, err
	}
	members, err := w.db.ListMembers(ctx, set.SetID, "")
	if err != nil {
		return domain.FactorSet{}, nil, nil, nil, err
	}
	selected, err := selectFactors(members, factorIDs)
	if err != nil {
		return domain.FactorSet{}, nil, nil, nil, err
	}
	resolved, err := w.resolveSubjects(ctx, set, subjects)
	if err != nil {
		return domain.FactorSet{}, nil, nil, nil, err
	}
	return set, selected, resolved, clock, nil
}

func (w *Worker) resolveSubjects(ctx context.Context, set domain.FactorSet, requested []string) ([]string, error) {
	if len(requested) == 0 {
		if set.SubjectMode == domain.SubjectModeInclude {
			requested = append([]string(nil), set.Subjects...)
		} else {
			if w.cfg.subjects == nil {
				return nil, errors.New("Storage subject provider is required for an all-subject factor set")
			}
			var err error
			requested, err = w.cfg.subjects.ListDatasetSubjects(ctx, set.SpaceID, set.SourceDatasetID)
			if err != nil {
				return nil, fmt.Errorf("list source dataset subjects: %w", err)
			}
		}
	}
	resolved, err := normalizeSubjects(requested)
	if err != nil {
		return nil, err
	}
	for _, subject := range resolved {
		if !set.InScope(subject) {
			return nil, fmt.Errorf("subject %q is outside factor set scope", subject)
		}
	}
	return resolved, nil
}

// selectFactors resolves a recalc request against the set's members: no ids
// means every enabled member, explicit ids must all be enabled members.
func selectFactors(members []domain.SetMember, factorIDs []string) ([]domain.FactorDef, error) {
	byID := make(map[string]domain.SetMember, len(members))
	enabled := make([]domain.FactorDef, 0, len(members))
	for _, member := range members {
		byID[member.FactorID] = member
		if member.Status == domain.MemberStatusEnabled {
			enabled = append(enabled, member.Factor)
		}
	}
	if len(factorIDs) == 0 {
		if len(enabled) == 0 {
			return nil, errors.New("factor set has no enabled members")
		}
		return enabled, nil
	}
	requested, err := normalizeIDs(factorIDs)
	if err != nil {
		return nil, err
	}
	selected := make([]domain.FactorDef, 0, len(requested))
	for _, id := range requested {
		member, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("factor %q is not a member of set", id)
		}
		if member.Status != domain.MemberStatusEnabled {
			return nil, fmt.Errorf("factor %q is not enabled in set", id)
		}
		selected = append(selected, member.Factor)
	}
	return selected, nil
}

func (w *Worker) columns(ctx context.Context, set domain.FactorSet, factors []domain.FactorDef) ([]string, error) {
	if w.cfg.columns != nil {
		columns, err := w.cfg.columns.DatasetColumns(ctx, set.SpaceID, set.SourceDatasetID)
		if err != nil {
			return nil, fmt.Errorf("list source dataset columns: %w", err)
		}
		return sortedUnique(columns), nil
	}
	columns := make([]string, 0)
	for _, factor := range factors {
		columns = append(columns, factor.InputColumns...)
	}
	return sortedUnique(columns), nil
}

func (w *Worker) clockFor(space string) (periodclock.Clock, error) {
	if w.cfg.clock != nil {
		return w.cfg.clock, nil
	}
	return periodclock.ForSpace(space)
}

func alignRange(clock periodclock.Clock, freq string, start, end time.Time) (time.Time, time.Time, error) {
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("recalc start must be before end")
	}
	start, err := clock.Align(start, freq)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc start: %w", err)
	}
	end, err = clock.Align(end, freq)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc end: %w", err)
	}
	if !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("recalc start must be before end")
	}
	return start, end, nil
}

func normalizeIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("factor_ids contains an empty value")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeSubjects(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("subjects contains an empty value")
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func sortedUnique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func sameStrings(left, right []string) bool {
	left = sortedUnique(left)
	right = sortedUnique(right)
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
