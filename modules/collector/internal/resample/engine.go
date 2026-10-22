package resample

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/barclock"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/seriesset"
	"trpc.group/trpc-go/trpc-go/log"
)

// 引擎默认值：每次执行（读源到写完）的硬超时、目标周期的迟到容忍、单次读取的键数上限。
const (
	DefaultExecTimeout = 30 * time.Second
	// TargetLatenessSeconds 是目标数据集周期的迟到容忍：收盘后这么久还没写满，Storage 按 degraded 结束周期。
	TargetLatenessSeconds = 120
	defaultMaxReadKeys    = 5000
	// commitSafetyMargin 离周期截止这么近就不再尝试周期提交，直接普通写入，避免提交在途中越过截止时间。
	commitSafetyMargin = 3 * time.Second
	// snapshotLead 是登记快照时生效点至少领先当前时间的量。
	snapshotLead = time.Minute
	// maxSnapshotLead 不超过 Storage 允许的最远生效点（7 天）并留出余量。
	maxSnapshotLead = 7*24*time.Hour - time.Hour
)

// PeriodStorage 是目标数据集的周期提交与快照登记接口。
type PeriodStorage interface {
	CommitTimeSeriesBatch(context.Context, *storagepb.DatasetPeriodExpectation, []*storagepb.TimeSeriesBatchRow, string) error
}

// SnapshotRegistrar 把数据集的标的集合登记到 Storage。
type SnapshotRegistrar interface {
	RegisterSeriesSnapshot(ctx context.Context, spaceID, datasetID, frequency string, series []seriesset.Series, effectiveFrom int64) (marketstorage.SnapshotRegistration, error)
}

// AbsenceSource 查询已确认“行情源没有”的 K 线区间。
type AbsenceSource interface {
	AbsencesIn(ctx context.Context, spaceID, datasetID, frequency string, subjects []string, from, to time.Time) ([]store.SourceAbsence, error)
}

// Task 是一个就绪合成任务的运行时视图。
type Task struct {
	Collection domain.CollectionTask
	Params     *domain.CollectParams
	Spec       TaskSpec
	Source     FixedFrequency
	Target     FixedFrequency
	Market     barclock.Market
	Clock      barclock.Clock
}

// NewTask 解析合成任务：校验参数（含市场与周期规则），并按市场建立目标周期的时钟。
func NewTask(task domain.CollectionTask) (*Task, error) {
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	if err != nil {
		return nil, err
	}
	ids := taskresult.PersistedResultIDs(task.SpaceID, task.TaskID, task.ResultDatasetID, task.ResultViewID)
	params.TargetDatasetID = ids.DatasetID
	if err := ValidateTaskParams(task.SpaceID, params); err != nil {
		return nil, err
	}
	source, _ := ParseFixedFrequency(params.SourceFrequency)
	target, _ := ParseFixedFrequency(params.TargetFrequency)
	market, err := barclock.MarketFor(task.SpaceID)
	if err != nil {
		return nil, err
	}
	clock, err := market.Clock(target.Duration)
	if err != nil {
		return nil, err
	}
	return &Task{
		Collection: task, Params: params, Source: source, Target: target, Market: market, Clock: clock,
		Spec: TaskSpec{
			TaskID: task.TaskID, SpaceID: task.SpaceID, SourceDatasetID: params.SourceDatasetID, SourceFrequency: source,
			SourceSeriesTag: params.SourceSeriesTag, TargetDatasetID: params.TargetDatasetID, TargetFrequency: target,
			OutputFields: append([]string(nil), params.OutputFields...),
		},
	}, nil
}

// SkippedSubject 是一个没有产出的标的及原因。
type SkippedSubject struct {
	SubjectID string
	Reason    string
}

// BucketResult 是一次按桶执行的结果。
type BucketResult struct {
	Written int
	Skipped []SkippedSubject
	// Committed 是用周期提交写入的标的数；其余用普通写入。
	Committed int
}

// Engine 按“数据集 × 桶”批量合成：一次读出全部标的的源窗口，逐标的聚合，再批量写入目标数据集。
// 事件主路径、兜底检查与历史作业共用它；它不加锁，重复执行读到同一份源数据时写出同一个值。
type Engine struct {
	Primary   PrimaryStorage
	Period    PeriodStorage
	Snapshots SnapshotRegistrar
	State     *store.ResampleStateRepository
	Absences  AbsenceSource
	Metrics   *Metrics

	ExecTimeout time.Duration
	MaxReadKeys int
	Now         func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) execTimeout() time.Duration {
	if e.ExecTimeout > 0 {
		return e.ExecTimeout
	}
	return DefaultExecTimeout
}

// RunBucket 合成一个目标桶。subjects 是要合成的标的；源窗口不完整的标的跳过（不写、不等待），由调用方决定后续。
// jobID 非空表示来自历史作业，会记入补写通知。
func (e *Engine) RunBucket(ctx context.Context, task *Task, bucket barclock.Bucket, subjects []string, jobID string) (BucketResult, error) {
	if e == nil || e.Primary == nil {
		return BucketResult{}, errors.New("resample engine Primary storage is required")
	}
	if len(subjects) == 0 {
		return BucketResult{}, nil
	}
	// 每次执行（读源到写完）都有硬超时，写请求带同一个截止时间：补采触发的作业等 2×超时再开始，就不会被还没写完的旧执行覆盖。
	ctx, cancel := context.WithTimeout(ctx, e.execTimeout())
	defer cancel()

	expected, err := task.Clock.SourceTimes(bucket, task.Source.Duration)
	if err != nil {
		return BucketResult{}, err
	}
	if len(expected) == 0 {
		return BucketResult{}, fmt.Errorf("bucket %s has no source bars", bucket.Key.Format(time.RFC3339))
	}
	rows, decodeErrs, err := e.readSource(ctx, task, expected, subjects)
	if err != nil {
		return BucketResult{}, err
	}

	var result BucketResult
	var aggregated []Result
	pending := make([]string, 0)
	for _, subject := range subjects {
		if len(rows[subject]) != len(expected) {
			pending = append(pending, subject)
		}
	}
	absent, err := e.absentTimes(ctx, task, bucket, pending)
	if err != nil {
		return BucketResult{}, err
	}
	for _, subject := range subjects {
		want := expected
		if skip := absent[subject]; len(skip) > 0 {
			want = withoutTimes(expected, skip)
			if len(want) == 0 {
				result.Skipped = append(result.Skipped, SkippedSubject{SubjectID: subject, Reason: "整个桶的源 K 线都已确认行情源没有"})
				continue
			}
		}
		if decodeErr := decodeErrs[subject]; decodeErr != nil {
			result.Skipped = append(result.Skipped, SkippedSubject{SubjectID: subject, Reason: "源 K 线无法解析：" + decodeErr.Error()})
			continue
		}
		aggregate, aggErr := Aggregate(task.Spec, subject, bucket, want, rows[subject])
		if aggErr != nil {
			result.Skipped = append(result.Skipped, SkippedSubject{SubjectID: subject, Reason: aggErr.Error()})
			continue
		}
		aggregated = append(aggregated, aggregate)
	}
	if e.Metrics != nil {
		e.Metrics.Skipped.Add(float64(len(result.Skipped)))
	}
	if len(aggregated) == 0 {
		return result, nil
	}
	committed, err := e.write(ctx, task, bucket, aggregated, jobID)
	if err != nil {
		if e.Metrics != nil {
			e.Metrics.Errors.Inc()
		}
		return result, err
	}
	result.Written, result.Committed = len(aggregated), committed
	if e.Metrics != nil {
		e.Metrics.Buckets.Inc()
		e.Metrics.Writes.Add(float64(len(aggregated)))
	}
	return result, nil
}

// withoutTimes 返回 expected 去掉 skip 里时刻之后的列表。
func withoutTimes(expected, skip []time.Time) []time.Time {
	drop := make(map[int64]struct{}, len(skip))
	for _, at := range skip {
		drop[at.UTC().Unix()] = struct{}{}
	}
	out := make([]time.Time, 0, len(expected))
	for _, at := range expected {
		if _, ok := drop[at.UTC().Unix()]; !ok {
			out = append(out, at)
		}
	}
	return out
}

// absentTimes 返回每个标的在桶内已确认行情源没有的 K 线时刻（只对源窗口不完整的标的查询）。
func (e *Engine) absentTimes(ctx context.Context, task *Task, bucket barclock.Bucket, subjects []string) (map[string][]time.Time, error) {
	out := map[string][]time.Time{}
	if e.Absences == nil || len(subjects) == 0 {
		return out, nil
	}
	absences, err := e.Absences.AbsencesIn(ctx, task.Spec.SpaceID, task.Spec.SourceDatasetID, task.Source.Storage, subjects, bucket.Start, bucket.End)
	if err != nil {
		return nil, fmt.Errorf("read source absences: %w", err)
	}
	for _, absence := range absences {
		times, timesErr := task.Clock.SourceTimes(barclock.Bucket{Key: bucket.Key, Start: maxTime(bucket.Start, absence.From), End: minTime(bucket.End, absence.To)}, task.Source.Duration)
		if timesErr != nil {
			continue
		}
		out[absence.SubjectID] = append(out[absence.SubjectID], times...)
	}
	return out, nil
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// readSource 批量读出全部标的的源窗口；返回按标的分组且升序的源 K 线。无法解析的行记入 decodeErrs，不进入分组。
func (e *Engine) readSource(ctx context.Context, task *Task, expected []time.Time, subjects []string) (map[string][]SourceBar, map[string]error, error) {
	keys := make([]*storagepb.RowKey, 0, len(subjects)*len(expected))
	for _, subject := range subjects {
		for _, at := range expected {
			keys = append(keys, rowKey(task.Spec.SpaceID, task.Spec.SourceDatasetID, subject, task.Source.Storage, at, task.Spec.SourceSeriesTag))
		}
	}
	limit := e.MaxReadKeys
	if limit <= 0 {
		limit = defaultMaxReadKeys
	}
	fields := sourceFields(task.Spec.SpaceID)
	groups := make(map[string][]SourceBar, len(subjects))
	decodeErrs := map[string]error{}
	for start := 0; start < len(keys); start += limit {
		chunk := keys[start:min(start+limit, len(keys))]
		rows, err := e.Primary.ReadRows(ctx, chunk, fields, nil)
		if err != nil {
			return nil, nil, fmt.Errorf("read source rows: %w", err)
		}
		for _, row := range rows {
			bar, decodeErr := decodeSourceBar(row)
			if decodeErr != nil {
				if series := row.GetKey().GetTimeSeries(); series != nil {
					decodeErrs[series.GetSubjectId()] = decodeErr
				}
				continue
			}
			groups[bar.SubjectID] = append(groups[bar.SubjectID], bar)
		}
	}
	for subject := range groups {
		sort.Slice(groups[subject], func(i, j int) bool { return groups[subject][i].DataTime.Before(groups[subject][j].DataTime) })
	}
	return groups, decodeErrs, nil
}

// write 把一个桶的结果写入目标数据集：落在已登记快照内且周期还没截止的标的用周期提交（Storage 跟踪到齐并发出完成事件），
// 其余（首次生效点之前的桶、截止之后的补写、快照之外的标的、周期提交失败）用普通写入，并记为补写通知。
func (e *Engine) write(ctx context.Context, task *Task, bucket barclock.Bucket, results []Result, jobID string) (int, error) {
	rows := make([]*storagepb.RowFieldUpsert, len(results))
	for i, result := range results {
		rows[i] = resultRowWithSpec(result, task.Spec)
	}
	committed := 0
	plain := rows
	plainResults := results
	// 历史作业是对已有结果的重算：周期已经完成时 Storage 只回执历史终态、不会写入新值，所以一律用普通写入。
	if commitRows, commitResults, plainRows, others := e.splitForCommit(ctx, task, bucket, rows, results); jobID == "" && len(commitRows) > 0 {
		if err := e.commit(ctx, task, bucket, commitRows, commitResults); err == nil {
			committed = len(commitRows)
			plain, plainResults = plainRows, others
		} else {
			log.WarnContextf(ctx, "[Collector] resample period commit failed, falling back to plain write task=%s bucket=%s: %v", task.Spec.TaskID, bucket.Key.Format(time.RFC3339), err)
		}
	}
	if len(plain) == 0 {
		return committed, nil
	}
	if err := e.Primary.UpsertFieldsWithSource(ctx, plain, ""); err != nil {
		return committed, fmt.Errorf("write resample rows: %w", err)
	}
	// 通知落库失败时整个桶算失败：事件不确认、作业不推进游标，重试时写入是幂等的，下游不会漏掉这次改写。
	if err := e.recordNotices(ctx, task, bucket, plainResults, jobID); err != nil {
		return committed, err
	}
	return committed, nil
}

// splitForCommit 按快照把结果分成周期提交部分与普通写入部分；不能周期提交时 commitRows 为空。
func (e *Engine) splitForCommit(ctx context.Context, task *Task, bucket barclock.Bucket, rows []*storagepb.RowFieldUpsert, results []Result) (commit []*storagepb.RowFieldUpsert, commitResults []Result, plain []*storagepb.RowFieldUpsert, plainResults []Result) {
	if e.Period == nil || e.State == nil {
		return nil, nil, rows, results
	}
	snapshot, ok, err := e.State.SnapshotFor(ctx, task.Spec.SpaceID, task.Spec.TaskID, bucket.Key.Unix())
	if err != nil || !ok {
		return nil, nil, rows, results
	}
	target, parseErr := frequencypkg.Parse(task.Spec.TargetFrequency.Storage)
	if parseErr != nil {
		return nil, nil, rows, results
	}
	deadline := target.BarEnd(bucket.Key).Add(time.Duration(TargetLatenessSeconds) * time.Second)
	if !e.now().Add(commitSafetyMargin).Before(deadline) {
		return nil, nil, rows, results
	}
	in := map[string]bool{}
	for _, subject := range snapshot.Subjects {
		in[subject] = true
	}
	for i, result := range results {
		if in[result.SubjectID] {
			commit, commitResults = append(commit, rows[i]), append(commitResults, results[i])
		} else {
			plain, plainResults = append(plain, rows[i]), append(plainResults, results[i])
		}
	}
	return commit, commitResults, plain, plainResults
}

// commit 用周期提交写入落在快照内的标的。
func (e *Engine) commit(ctx context.Context, task *Task, bucket barclock.Bucket, rows []*storagepb.RowFieldUpsert, results []Result) error {
	snapshot, ok, err := e.State.SnapshotFor(ctx, task.Spec.SpaceID, task.Spec.TaskID, bucket.Key.Unix())
	if err != nil || !ok {
		return fmt.Errorf("resample snapshot is unavailable: %v", err)
	}
	set, err := seriesset.Canonicalize(task.series(snapshot.Subjects))
	if err != nil {
		return err
	}
	if set.Hash != snapshot.SeriesHash {
		return fmt.Errorf("resample snapshot hash changed: want %s got %s", snapshot.SeriesHash, set.Hash)
	}
	index := make(map[string]uint32, len(set.Series))
	for i, series := range set.Series {
		index[series.SubjectID] = uint32(i)
	}
	items := make([]*storagepb.TimeSeriesBatchRow, 0, len(rows))
	for i, result := range results {
		position, found := index[result.SubjectID]
		if !found {
			return fmt.Errorf("subject %s is not in the resample snapshot", result.SubjectID)
		}
		items = append(items, &storagepb.TimeSeriesBatchRow{SeriesIndex: position, Row: rows[i]})
	}
	expectation := &storagepb.DatasetPeriodExpectation{
		SpaceId: task.Spec.SpaceID, DatasetId: task.Spec.TargetDatasetID, Frequency: task.Spec.TargetFrequency.Storage,
		PeriodTime: bucket.Key.Unix(), SeriesHash: set.Hash, ExpectedCount: uint32(len(set.Series)),
	}
	return e.Period.CommitTimeSeriesBatch(ctx, expectation, items, "")
}

// series 把标的 ID 列表变成目标数据集的序列身份。
func (t *Task) series(subjects []string) []seriesset.Series {
	out := make([]seriesset.Series, len(subjects))
	for i, subject := range subjects {
		out[i] = seriesset.Series{Provider: t.Params.Provider, MarketType: t.Params.MarketType, SubjectID: subject, SeriesTag: t.Spec.SourceSeriesTag}
	}
	return out
}

// recordNotices 把普通写入记为补写通知，通知下游（因子等）这些标的在 [bucket.Start, bucket.End) 被写入过。
func (e *Engine) recordNotices(ctx context.Context, task *Task, bucket barclock.Bucket, results []Result, jobID string) error {
	if e.State == nil || len(results) == 0 {
		return nil
	}
	notices := make([]store.BackfillNotice, len(results))
	for i, result := range results {
		notices[i] = store.BackfillNotice{
			SpaceID: task.Spec.SpaceID, DatasetID: task.Spec.TargetDatasetID, Frequency: task.Spec.TargetFrequency.Storage,
			SubjectID: result.SubjectID, Start: bucket.Start, End: bucket.End, Origin: store.NoticeOriginResample, JobID: jobID,
		}
	}
	if err := e.State.AddNotices(ctx, notices); err != nil {
		return fmt.Errorf("record resample notices: %w", err)
	}
	return nil
}

// EnsureSnapshot 保证目标数据集登记了与当前标的集合一致的快照：集合变化时登记一份新的，从下一个目标桶起生效。
// 生效点之前的桶用普通写入。登记失败不影响合成本身（只是不由 Storage 跟踪周期）。
func (e *Engine) EnsureSnapshot(ctx context.Context, task *Task, subjects []string) error {
	if e.Snapshots == nil || e.State == nil || len(subjects) == 0 {
		return nil
	}
	subjects = uniqueSorted(subjects)
	latest, ok, err := e.State.LatestSnapshot(ctx, task.Spec.SpaceID, task.Spec.TaskID)
	if err != nil {
		return err
	}
	if ok && sameStrings(latest.Subjects, subjects) {
		return nil
	}
	set, err := seriesset.Canonicalize(task.series(subjects))
	if err != nil {
		return err
	}
	now := e.now()
	from := now.Add(snapshotLead)
	buckets, err := task.Clock.Between(from, from.Add(maxSnapshotLead), 64)
	if err != nil {
		return err
	}
	var effective barclock.Bucket
	found := false
	for _, bucket := range buckets {
		if bucket.Key.After(from) {
			effective, found = bucket, true
			break
		}
	}
	if !found || effective.Key.Sub(now) > maxSnapshotLead {
		return nil // 近期没有合适的生效点（例如长假），下次再登记
	}
	if ok && effective.Key.Unix() <= latest.EffectiveFrom {
		return nil // 上一份快照还没生效，等它生效后再登记新的
	}
	registration, err := e.Snapshots.RegisterSeriesSnapshot(ctx, task.Spec.SpaceID, task.Spec.TargetDatasetID, task.Spec.TargetFrequency.Storage, set.Series, effective.Key.Unix())
	if err != nil {
		if errors.Is(err, marketstorage.ErrStaleEffectiveFrom) {
			return nil // 生效点已不在未来，下一轮重新选择
		}
		return fmt.Errorf("register resample snapshot: %w", err)
	}
	return e.State.AddSnapshot(ctx, store.ResampleSnapshot{
		SpaceID: task.Spec.SpaceID, TaskID: task.Spec.TaskID, EffectiveFrom: effective.Key.Unix(), SeriesHash: registration.Hash, Subjects: subjects,
	})
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
