package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/packages/seriesset"
)

type LiveInput struct {
	Set     domain.FactorTask
	Members []domain.MemberDef
	// Params 是这一期各成员用的参数（成员键 → 快照）。
	Params         map[string]domain.ParamSnapshot
	PeriodTime     time.Time
	Universe       []string
	UpstreamFailed []string
	// UniverseSeries、UpstreamFailedSeries 是统一数据集事件里按序列的全集与失败名单；非空时按序列处理（见 Plan.ExpectedSeries）。
	UniverseSeries       []seriesset.Series
	UpstreamFailedSeries []seriesset.Series
	CarryColumns         []string
	TriggerEventID       string
	Budget               time.Duration
}

func BuildLivePlan(clock periodclock.Clock, input LiveInput) (Plan, error) {
	if clock == nil {
		return Plan{}, fmt.Errorf("period clock is required")
	}
	if input.Set.Status != domain.TaskStatusEnabled {
		return Plan{}, fmt.Errorf("factor task must be enabled")
	}
	if strings.TrimSpace(input.Set.SpaceID) == "" || strings.TrimSpace(input.Set.SourceDatasetID) == "" || strings.TrimSpace(input.Set.Freq) == "" {
		return Plan{}, fmt.Errorf("factor task identity is incomplete")
	}
	target, err := clock.Align(input.PeriodTime, input.Set.Freq)
	if err != nil {
		return Plan{}, fmt.Errorf("align target period: %w", err)
	}
	duration, err := clock.Duration(input.Set.Freq)
	if err != nil {
		return Plan{}, err
	}
	members := make([]domain.MemberDef, 0, len(input.Members))
	maxLookback := 1
	for _, member := range input.Members {
		member.Factor.InputColumns = append([]string(nil), member.Factor.InputColumns...)
		member.Factor.Outputs = append([]string(nil), member.Factor.Outputs...)
		members = append(members, member)
		if member.Factor.LookbackPeriods > maxLookback {
			maxLookback = member.Factor.LookbackPeriods
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Key < members[j].Key })
	window, err := clock.Window(target, input.Set.Freq, maxLookback)
	if err != nil {
		return Plan{}, err
	}
	expected := uniqueSorted(input.Universe)
	upstreamFailed := intersectLists(expected, input.UpstreamFailed)
	failedTask := make(map[string]struct{}, len(upstreamFailed))
	for _, subject := range upstreamFailed {
		failedTask[subject] = struct{}{}
	}
	available := make([]string, 0, len(expected)-len(upstreamFailed))
	for _, subject := range expected {
		if _, failed := failedTask[subject]; !failed {
			available = append(available, subject)
		}
	}
	var expectedSeries, upstreamFailedSeries []seriesset.Series
	if len(input.UniverseSeries) > 0 {
		// 统一数据集：全集按序列。标的口径由序列推出：Available 是至少有一条序列没有上游失败的标的，
		// 只有现货失败的标的、永续照常可算，所以不能因为标的出现在失败名单里就整体不读。
		expectedSeries = uniqueSeriesSorted(input.UniverseSeries)
		upstreamFailedSeries = intersectSeries(uniqueSeriesSorted(input.UpstreamFailedSeries), expectedSeries)
		expected = seriesSubjects(expectedSeries)
		upstreamFailed = seriesSubjects(upstreamFailedSeries)
		available = seriesSubjects(differenceSeries(expectedSeries, upstreamFailedSeries))
	}
	return Plan{
		Mode: ModeLive, Set: input.Set, Members: members, Params: input.Params, PeriodTime: target,
		TargetStart: window[0], TargetEnd: target.Add(duration),
		Expected: expected, Available: available, UpstreamFailed: upstreamFailed,
		ExpectedSeries: expectedSeries, UpstreamFailedSeries: upstreamFailedSeries,
		CarryColumns: uniqueSorted(input.CarryColumns), WriteCarry: true,
		TriggerEventID: strings.TrimSpace(input.TriggerEventID), Budget: input.Budget,
	}, nil
}

func intersectLists(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, value := range right {
		rightSet[value] = struct{}{}
	}
	out := make([]string, 0, len(left))
	for _, value := range left {
		if _, ok := rightSet[value]; ok {
			out = append(out, value)
		}
	}
	return out
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
