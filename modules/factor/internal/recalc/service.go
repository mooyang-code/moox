package recalc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

type SubjectProvider interface {
	ListDatasetSubjects(context.Context, string, string) ([]string, error)
}

type config struct {
	subjects SubjectProvider
}

type Option func(*config)

func WithSubjectProvider(provider SubjectProvider) Option {
	return func(cfg *config) { cfg.subjects = provider }
}

// Service owns durable request acceptance and inspection in moox-factor-mgr;
// moox-factor-engine pulls the accepted jobs and runs them with recalcexec.
type Service struct {
	db  *store.Store
	cfg config
}

func NewService(db *store.Store, options ...Option) *Service {
	cfg := config{}
	for _, option := range options {
		if option != nil {
			option(&cfg)
		}
	}
	return &Service{db: db, cfg: cfg}
}

func (s *Service) Submit(ctx context.Context, taskID string, memberKeys, subjects []string, requestID string, start, end time.Time) (store.RecalcJob, error) {
	if s == nil || s.db == nil {
		return store.RecalcJob{}, errors.New("factor recalc store is required")
	}
	requestID = strings.TrimSpace(requestID)
	taskID = strings.TrimSpace(taskID)
	if requestID == "" || taskID == "" {
		return store.RecalcJob{}, errors.New("request_id and task_id are required")
	}
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return store.RecalcJob{}, err
	}
	start, end, err = AlignRange(set.SpaceID, set.Freq, start, end)
	if err != nil {
		return store.RecalcJob{}, err
	}
	if existing, found, err := s.db.FindRecalcJobByRequestID(ctx, requestID); err != nil {
		return store.RecalcJob{}, err
	} else if found {
		if !matchesSubmitRequest(existing, taskID, memberKeys, subjects, start.Unix(), end.Unix()) {
			return store.RecalcJob{}, fmt.Errorf("request_id %q already belongs to a different recalc request", requestID)
		}
		return existing, nil
	}
	set, selected, normalizedSubjects, err := s.selection(ctx, taskID, memberKeys, subjects)
	if err != nil {
		return store.RecalcJob{}, err
	}
	start, end, err = AlignRange(set.SpaceID, set.Freq, start, end)
	if err != nil {
		return store.RecalcJob{}, err
	}
	ids := make([]string, 0, len(selected))
	for _, member := range selected {
		ids = append(ids, member.Key)
	}
	job, err := s.db.CreateRecalcJob(ctx, store.RecalcJob{
		JobID: requestID, RequestID: requestID, TaskID: taskID,
		Members: ids, Subjects: normalizedSubjects,
		MembersOmitted: len(memberKeys) == 0, SubjectsOmitted: len(subjects) == 0,
		StartTime: start.Unix(), EndTime: end.Unix(), Status: store.RecalcStatusAccepted,
	})
	if err != nil {
		return store.RecalcJob{}, err
	}
	if !matchesSubmitRequest(job, taskID, memberKeys, subjects, start.Unix(), end.Unix()) {
		return store.RecalcJob{}, fmt.Errorf("request_id %q already belongs to a different recalc request", requestID)
	}
	return job, nil
}

func matchesSubmitRequest(job store.RecalcJob, taskID string, memberKeys, subjects []string, start, end int64) bool {
	if job.TaskID != taskID || job.StartTime != start || job.EndTime != end ||
		job.MembersOmitted != (len(memberKeys) == 0) || job.SubjectsOmitted != (len(subjects) == 0) {
		return false
	}
	if !job.MembersOmitted {
		normalized, err := normalizeIDs(memberKeys)
		if err != nil || !sameStrings(job.Members, normalized) {
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

func (s *Service) Get(ctx context.Context, jobID string) (store.RecalcJob, error) {
	if s == nil || s.db == nil {
		return store.RecalcJob{}, errors.New("factor recalc store is required")
	}
	return s.db.GetRecalcJob(ctx, jobID)
}

func (s *Service) List(ctx context.Context, taskID string, statuses []string) ([]store.RecalcJob, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("factor recalc store is required")
	}
	return s.db.ListRecalcJobs(ctx, store.RecalcJobFilter{TaskID: taskID, Statuses: statuses})
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

func (s *Service) selection(ctx context.Context, taskID string, memberKeys, subjects []string) (domain.FactorTask, []domain.MemberDef, []string, error) {
	set, err := s.db.GetTask(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, nil, nil, err
	}
	if set.Status != domain.TaskStatusEnabled {
		return domain.FactorTask{}, nil, nil, fmt.Errorf("factor task %q is not enabled", set.TaskID)
	}
	members, err := s.db.ListMembers(ctx, set.TaskID, "")
	if err != nil {
		return domain.FactorTask{}, nil, nil, err
	}
	selected, err := domain.SelectEnabledMembers(members, memberKeys)
	if err != nil {
		return domain.FactorTask{}, nil, nil, err
	}
	if len(subjects) > 0 {
		if err := rejectObservedSubset(selected); err != nil {
			return domain.FactorTask{}, nil, nil, err
		}
	}
	resolved, err := s.resolveSubjects(ctx, set, subjects)
	if err != nil {
		return domain.FactorTask{}, nil, nil, err
	}
	return set, selected, resolved, nil
}

// rejectObservedSubset 拒绝对写了 cross_section_history: observed 的成员做标的子集补算：没列出的在册标的正在同步、
// 又不在这次补算的范围里，按历史截面的规则既不在当前截面、也不进历史截面，截面比实时少了这些标的，结果与实时、补算全部标的
// 都不同，又会写回结果数据集。这类成员请补算全部标的（不指定 subjects）。
func rejectObservedSubset(members []domain.MemberDef) error {
	for _, member := range members {
		if member.Factor.ObservedHistory() {
			return fmt.Errorf("成员 %s 写了 cross_section_history: observed，不能只补算部分标的（结果会与实时不同）：请补算全部标的，"+
				"或者只选其他成员", member.Key)
		}
	}
	return nil
}

func (s *Service) resolveSubjects(ctx context.Context, set domain.FactorTask, requested []string) ([]string, error) {
	if s.cfg.subjects == nil {
		return nil, errors.New("Storage subject provider is required to resolve factor task subjects")
	}
	// 任务的标的就是源数据集当前的标的：不指定时取全部，指定时必须都属于源数据集。
	all, err := s.cfg.subjects.ListDatasetSubjects(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("list source dataset subjects: %w", err)
	}
	explicit := len(requested) > 0
	if !explicit {
		requested = all
	}
	resolved, err := normalizeSubjects(requested)
	if err != nil {
		return nil, err
	}
	if explicit {
		inDataset := make(map[string]struct{}, len(all))
		for _, subject := range all {
			inDataset[subject] = struct{}{}
		}
		for _, subject := range resolved {
			if _, ok := inDataset[subject]; !ok {
				return nil, fmt.Errorf("subject %q is not in source dataset %s", subject, set.SourceDatasetID)
			}
		}
	}
	return resolved, nil
}

// AlignRange 规整补算区间 [start, end)（真实时间，按真实时间入库）。
//   - 7×24 市场：起点、终点都必须按周期对齐（严格校验，不吸附）。
//   - 有交易时段的市场（A 股）：前端按 7×24 的网格给出的区间会落在午休、收盘后、周末，这里吸附到交易 K 线上：
//     起点取不早于它的第一根交易 K 线的开盘时刻，终点取不晚于它的最近一次收盘时刻；吸附后区间里没有 K 线才报错。
func AlignRange(spaceID, freq string, start, end time.Time) (time.Time, time.Time, error) {
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return time.Time{}, time.Time{}, errors.New("recalc start must be before end")
	}
	axis, err := periodclock.AxisFor(spaceID, freq)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start, end = start.UTC(), end.UTC()
	if axis.Identity() {
		if _, err := axis.ToAxis(start); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("align recalc start: %w", err)
		}
		if _, err := axis.ToAxis(end); err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("align recalc end: %w", err)
		}
		return start, end, nil
	}
	// 起点：收盘不晚于 start 的最后一根之后的那一根；它的开盘早于 start（start 落在这根的盘中）时再往后一根。
	axisStart, err := axis.AxisEnd(start)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc start: %w", err)
	}
	if open, err := axis.FromAxis(axisStart); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc start: %w", err)
	} else if open.Before(start) {
		axisStart = axisStart.Add(axis.Step())
	}
	axisEnd, err := axis.AxisEnd(end)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc end: %w", err)
	}
	if !axisStart.Before(axisEnd) {
		return time.Time{}, time.Time{}, fmt.Errorf("补算区间 [%s, %s) 内没有 %s 的交易 K 线", start.Format(time.RFC3339), end.Format(time.RFC3339), freq)
	}
	if start, err = axis.FromAxis(axisStart); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc start: %w", err)
	}
	if end, err = axis.FromAxisEnd(axisEnd); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("align recalc end: %w", err)
	}
	return start, end, nil
}

func normalizeIDs(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("members contains an empty value")
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
