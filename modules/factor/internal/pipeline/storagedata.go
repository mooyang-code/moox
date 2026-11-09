package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/serieswindow"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// storageData 是直接读 Storage 的数据层：用于管理端试算和 run-once 诊断（没有本地时序存储、标的又很少），
// 直接从 Storage 读回看窗口，转成管线需要的列式视图。数值字段进视图，其余列只出现在目标区间的行里。
// 引擎的正式计算不用它，走本地时序存储与内存窗口（localdata）。
type storageData struct {
	store storageio.Store
}

// NewStorageData 返回直接读 Storage 的数据层。
func NewStorageData(store storageio.Store) Data { return storageData{store: store} }

// historyCandidateFactor、historyCandidateFloor 决定直接读 Storage 时候选数的粗限 max(factor·limit, floor)：
// 超过时不再逐个确认有没有行，直接拒绝。
const (
	historyCandidateFactor = 4
	historyCandidateFloor  = 8000
)

// historyProbeBatch 是确认候选标的有没有行时每次读取的标的数：确认数一超过上限就停，不读完全部候选。
const historyProbeBatch = 200

// ErrHistoryTooMany 表示历史截面的标的超过调用方给的上限；具体数字在 HistoryTooManyError 里。
var ErrHistoryTooMany = errors.New("历史截面的标的太多")

// HistoryTooManyError 是 ErrHistoryTooMany 的具体情况：Coarse 为真时是候选数超过粗限（Candidates 个，粗限 Bound），
// 否则是确认有行的标的已超过余量 Limit（确认到 Confirmed 个就停止）。
type HistoryTooManyError struct {
	Coarse     bool
	Candidates int
	Confirmed  int
	Limit      int
	Bound      int
}

func (e *HistoryTooManyError) Error() string {
	if e.Coarse {
		return fmt.Sprintf("不在册的候选标的 %d 个，超过余量 %d 的 %d 倍，也超过 %d 个", e.Candidates, e.Limit, historyCandidateFactor, e.Bound)
	}
	return fmt.Sprintf("区间内有行的不在册标的已超过余量 %d（确认到 %d 个）", e.Limit, e.Confirmed)
}

func (e *HistoryTooManyError) Is(target error) bool { return target == ErrHistoryTooMany }

// HistorySubjects 实现 HistorySource，不限个数。
func (d storageData) HistorySubjects(ctx context.Context, plan Plan) ([]string, error) {
	return d.HistorySubjectsWithin(ctx, plan, 0)
}

// HistorySubjectsWithin 实现 LimitedHistorySource（近似口径，用于管理端试算）：没有本地存储与任务登记，把 Storage 里在册
// （active）的标的都当作“有任务在同步”，只纳入不在册（已下架、停用）且在读取区间 [ReadStart, TargetEnd) 里有行的标的；
// 本期可用与上游失败的标的不算。limit > 0 时：候选数超过粗限 max(4·limit, 8000) 直接拒绝；确认有行的个数一超过
// limit 就停止读取并返回 ErrHistoryTooMany。确认时每个标的只读一列（Storage 要求至少一列）。客户端不能列出全部标的时没有历史标的。
func (d storageData) HistorySubjectsWithin(ctx context.Context, plan Plan, limit int) ([]string, error) {
	lister, ok := d.store.(interface {
		storageio.AllSubjectsLister
		ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
	})
	if !ok {
		return []string{}, nil
	}
	all, err := lister.ListAllDatasetSubjects(ctx, plan.Set.SpaceID, plan.Set.SourceDatasetID)
	if err != nil {
		return nil, err
	}
	active, err := lister.ListDatasetSubjects(ctx, plan.Set.SpaceID, plan.Set.SourceDatasetID)
	if err != nil {
		return nil, err
	}
	exclude := make(map[string]struct{}, len(active)+len(plan.Available)+len(plan.UpstreamFailed))
	for _, list := range [][]string{active, plan.Available, plan.UpstreamFailed} {
		for _, subject := range list {
			exclude[subject] = struct{}{}
		}
	}
	var candidates []string
	for _, subject := range all {
		if _, skip := exclude[subject]; !skip {
			candidates = append(candidates, subject)
		}
	}
	sort.Strings(candidates)
	if bound := max(limit*historyCandidateFactor, historyCandidateFloor); limit > 0 && len(candidates) > bound {
		return nil, &HistoryTooManyError{Coarse: true, Candidates: len(candidates), Limit: limit, Bound: bound}
	}
	out := []string{}
	if len(candidates) == 0 {
		return out, nil
	}
	column := probeColumn(plan)
	if column == "" {
		return nil, errors.New("确认历史截面的标的需要至少一个输入列")
	}
	start, err := ReadStart(periodclock.Continuous{}, plan)
	if err != nil {
		return nil, err
	}
	for lo := 0; lo < len(candidates); lo += historyProbeBatch {
		batch := candidates[lo:min(len(candidates), lo+historyProbeBatch)]
		frames, err := d.store.ReadWindow(ctx, storageio.ReadRequest{SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID,
			Freq: plan.Set.Freq, Subjects: batch, Start: start, End: plan.TargetEnd, Columns: []string{column}})
		if err != nil {
			return nil, err
		}
		for _, subject := range batch {
			if frame := frames[subject]; frame != nil && len(frame.Rows) > 0 {
				out = append(out, subject)
			}
		}
		if limit > 0 && len(out) > limit {
			return nil, &HistoryTooManyError{Candidates: len(candidates), Confirmed: len(out), Limit: limit}
		}
	}
	return out, nil
}

// probeColumn 是确认候选标的有没有行时读的那一列：计划的携带列或成员的第一个输入列。
func probeColumn(plan Plan) string {
	if len(plan.CarryColumns) > 0 {
		return plan.CarryColumns[0]
	}
	for _, member := range plan.Members {
		if compiled := member.Factor.Compiled(); compiled != nil && len(compiled.InputColumns) > 0 {
			return compiled.InputColumns[0]
		}
		if len(member.Factor.InputColumns) > 0 {
			return member.Factor.InputColumns[0]
		}
	}
	return ""
}

func (d storageData) Load(ctx context.Context, plan Plan) (DataResult, error) {
	// 管线始终在轴上运行：store 是换算边界包装过的客户端，读回的 data_time 已是轴上时间。
	start, err := ReadStart(periodclock.Continuous{}, plan)
	if err != nil {
		return DataResult{}, err
	}
	frames, err := d.store.ReadWindow(ctx, storageio.ReadRequest{
		SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID, Freq: plan.Set.Freq,
		Subjects: append(append([]string(nil), plan.Available...), plan.HistorySubjects...), Start: start, End: plan.TargetEnd, Columns: plan.CarryColumns,
	})
	if err != nil {
		return DataResult{}, err
	}
	step, err := seriesstore.Step(plan.Set.Freq)
	if err != nil {
		return DataResult{}, err
	}
	// 历史截面的标的（observed）只进视图，不产生目标区间的行。
	history := make(map[string]struct{}, len(plan.HistorySubjects))
	for _, subject := range plan.HistorySubjects {
		history[subject] = struct{}{}
	}
	cols := make(map[string]*seriesstore.Columns, len(frames))
	rows := make(map[string]*storageio.Frame, len(frames))
	var numeric []string
	for subject, frame := range frames {
		fields, positions := numericFields(frame)
		numeric = fields.names()
		c := seriesstore.NewColumns(fields.fields, len(frame.Rows))
		sorted := append([][]any(nil), frame.Rows...)
		sort.SliceStable(sorted, func(i, j int) bool {
			a, b := sorted[i][0].(time.Time), sorted[j][0].(time.Time)
			if !a.Equal(b) {
				return a.Before(b)
			}
			return sorted[i][1].(string) < sorted[j][1].(string)
		})
		target := &storageio.Frame{SubjectID: subject, Columns: frame.Columns}
		for _, row := range sorted {
			values := make([]seriesstore.Value, len(positions))
			for i, p := range positions {
				switch v := row[p].(type) {
				case float64:
					values[i] = seriesstore.Float(v)
				case int64:
					values[i] = seriesstore.Float(float64(v))
				default:
					values[i] = seriesstore.NullValue
				}
			}
			c.AppendRow(row[0].(time.Time), row[1].(string), false, values...)
			if at := row[0].(time.Time); !at.Before(plan.TargetStart) && at.Before(plan.TargetEnd) {
				target.Rows = append(target.Rows, row)
			}
		}
		if c.Len() > 0 {
			cols[subject] = c
		}
		if _, only := history[subject]; !only {
			rows[subject] = target
		}
	}
	view, err := serieswindow.FromColumns(seriesstore.Source{SpaceID: plan.Set.SpaceID, DatasetID: plan.Set.SourceDatasetID, Freq: plan.Set.Freq}, int64(step/time.Second), numeric, cols)
	if err != nil {
		return DataResult{}, fmt.Errorf("试算读到的数据不连续：%w", err)
	}
	return DataResult{View: view, Frames: rows}, nil
}

func (storageData) WindowStatuses() []WarmupStatus { return nil }

type fieldList struct{ fields []seriesstore.Field }

func (f fieldList) names() []string {
	out := make([]string, len(f.fields))
	for i, x := range f.fields {
		out[i] = x.Name
	}
	return out
}

// numericFields 取帧里的数值列（按第一个非空值判断类型）。
func numericFields(frame *storageio.Frame) (fieldList, []int) {
	var out fieldList
	var positions []int
	for i := 2; i < len(frame.Columns); i++ {
		numeric := true
		seen := false
		for _, row := range frame.Rows {
			if row[i] == nil {
				continue
			}
			seen = true
			switch row[i].(type) {
			case float64, int64:
			default:
				numeric = false
			}
			break
		}
		if numeric && (seen || len(frame.Rows) == 0) {
			out.fields = append(out.fields, seriesstore.Field{Name: frame.Columns[i], Type: seriesstore.TypeFloat64})
			positions = append(positions, i)
		}
	}
	return out, positions
}
