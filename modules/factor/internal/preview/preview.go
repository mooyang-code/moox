// Package preview 在管理端试算一个表达式定义（设计 9.4 PreviewFactor）：在指定因子集的源数据上，
// 对少量标的计算最近若干期。直接读 Storage，复用周期流水线的读取与计算，不写任何结果，也不经过引擎。
package preview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

const (
	// DefaultPeriods 是不指定期数时输出的最近期数。
	DefaultPeriods = 20
	// MaxPeriods 是一次试算最多输出的期数。
	MaxPeriods = 500
	// DefaultSubjects 是不指定标的时取源数据集的前几个标的。
	DefaultSubjects = 5
	// MaxSubjects 是一次试算最多输出的标的数；截面因子仍用集合的全部标的计算。
	MaxSubjects = 20
	// MaxUniverse 是截面因子试算时参与计算的标的上限。
	MaxUniverse = 2000
	// MaxMemory 是一次试算估算峰值内存的上限：试算跑在管理端进程里，与正式计算分开限额。
	MaxMemory = 256 << 20
)

// Source 是试算需要的 Storage 读取能力。
type Source interface {
	storageio.Store
	ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error)
}

// Request 是一次试算的输入。
type Request struct {
	Set      domain.FactorSet
	Factor   domain.FactorDef // 已经 NormalizeFactorDefinition 编译过
	Subjects []string
	Periods  int
}

// Row 是一个 (标的, 标签, 周期) 的输出；Values 与 Result.Columns 一一对应，null 为 nil。
type Row struct {
	Subject  string
	Tag      string
	DataTime time.Time
	Values   []*float64
}

// Result 是试算结果。
type Result struct {
	Columns []string
	Rows    []Row
}

// Service 执行试算。
type Service struct {
	source Source
	clock  periodclock.Clock
	now    func() time.Time
	runner *pipeline.Runner
}

func New(source Source, workers int) *Service {
	clock := periodclock.Continuous{}
	return &Service{source: source, clock: clock, now: time.Now,
		runner: pipeline.NewRunner(source, pipeline.NewStorageData(source), clock, pipeline.Config{ComputeWorkers: workers, MaxMemory: MaxMemory})}
}

// Preview 计算 [对齐后的当前周期 - Periods 期, 当前周期) 的结果。
func (s *Service) Preview(ctx context.Context, req Request) (Result, error) {
	if s == nil || s.source == nil {
		return Result{}, errors.New("试算未启用：管理端没有 Storage 读取客户端")
	}
	factor := req.Factor
	if factor.Compiled() == nil {
		return Result{}, errors.New("定义尚未编译")
	}
	periods := req.Periods
	if periods <= 0 {
		periods = DefaultPeriods
	}
	if periods > MaxPeriods {
		return Result{}, fmt.Errorf("periods 最多 %d", MaxPeriods)
	}
	if len(req.Subjects) > MaxSubjects {
		return Result{}, fmt.Errorf("subjects 最多 %d 个", MaxSubjects)
	}
	set := req.Set
	columns, err := s.source.DatasetColumns(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return Result{}, fmt.Errorf("读取源数据集的列：%w", err)
	}
	available := make(map[string]bool, len(columns))
	for _, column := range columns {
		available[column] = true
	}
	for _, column := range factor.InputColumns {
		if !available[column] {
			return Result{}, fmt.Errorf("源数据集没有输入列 %s", column)
		}
	}
	universe, err := s.universe(ctx, set)
	if err != nil {
		return Result{}, err
	}
	shown := req.Subjects
	if len(shown) == 0 {
		shown = universe[:min(DefaultSubjects, len(universe))]
	}
	if len(shown) == 0 {
		return Result{}, errors.New("因子集没有可用的标的")
	}
	computed := shown
	if factor.FactorType == domain.FactorTypeCrossSection {
		if len(universe) > MaxUniverse {
			return Result{}, fmt.Errorf("截面因子试算最多支持 %d 个标的，集合有 %d 个", MaxUniverse, len(universe))
		}
		computed = universe
	}
	duration, err := s.clock.Duration(set.Freq)
	if err != nil {
		return Result{}, err
	}
	// 当前所在周期的起点（不含当前未收盘的周期）：周期时钟是连续日历，按周期长度向下取整。
	end := s.now().UTC().Truncate(duration)
	if _, err := s.clock.Align(end, set.Freq); err != nil {
		return Result{}, err
	}
	// 读取之前按标的数估算峰值内存，超限直接拒绝，避免长窗口 × 大全集的试算耗尽管理端内存。
	if err := pipeline.CheckChunkMemory([]domain.FactorDef{factor}, len(computed), periods, len(factor.InputColumns), MaxMemory); err != nil {
		return Result{}, fmt.Errorf("试算范围太大，请减少期数或标的：%w", err)
	}
	plan := pipeline.Plan{
		Mode: pipeline.ModeRecalc, Set: set, Factors: []domain.FactorDef{factor},
		TargetStart: end.Add(-time.Duration(periods) * duration), TargetEnd: end,
		Expected: computed, Available: computed, CarryColumns: factor.InputColumns,
	}
	loaded, err := s.runner.Load(ctx, plan)
	if err != nil {
		return Result{}, fmt.Errorf("读取源数据：%w", err)
	}
	computation, err := s.runner.Compute(ctx, plan, loaded)
	if err != nil {
		return Result{}, err
	}
	return collect(factor, computation, shown)
}

func (s *Service) universe(ctx context.Context, set domain.FactorSet) ([]string, error) {
	if set.SubjectMode == domain.SubjectModeInclude {
		return append([]string(nil), set.Subjects...), nil
	}
	subjects, err := s.source.ListDatasetSubjects(ctx, set.SpaceID, set.SourceDatasetID)
	if err != nil {
		return nil, fmt.Errorf("列出源数据集的标的：%w", err)
	}
	sort.Strings(subjects)
	return subjects, nil
}

// collect 把计算结果整理成按 (标的, 周期, 标签) 排序的行，只保留要展示的标的。
func collect(factor domain.FactorDef, computation pipeline.Computation, shown []string) (Result, error) {
	wanted := make(map[string]bool, len(shown))
	for _, subject := range shown {
		wanted[subject] = true
	}
	result := Result{Columns: append([]string(nil), factor.Outputs...)}
	for key, item := range computation.Results[factor.FactorID] {
		if item.Err != nil {
			return Result{}, item.Err
		}
		cross := len(item.Columns) > 2 && item.Columns[2] == "subject_id"
		offset := 2
		if cross {
			offset = 3
		}
		for _, values := range item.Rows {
			subject := key
			if cross {
				subject, _ = values[2].(string)
			}
			if !wanted[subject] {
				continue
			}
			at, _ := values[0].(time.Time)
			tag, _ := values[1].(string)
			row := Row{Subject: subject, Tag: tag, DataTime: at, Values: make([]*float64, len(factor.Outputs))}
			for i := range factor.Outputs {
				if value, ok := values[offset+i].(float64); ok {
					row.Values[i] = &value
				}
			}
			result.Rows = append(result.Rows, row)
		}
	}
	if state, ok := computation.FactorStates[factor.FactorID]; ok && state.Status == "skipped" {
		return Result{}, errors.New("截面因子本次被跳过：集合的标的不完整")
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		left, right := result.Rows[i], result.Rows[j]
		if left.Subject != right.Subject {
			return left.Subject < right.Subject
		}
		if !left.DataTime.Equal(right.DataTime) {
			return left.DataTime.Before(right.DataTime)
		}
		return left.Tag < right.Tag
	})
	return result, nil
}
