package pipeline

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
)

type LiveInput struct {
	Set     domain.FactorTask
	Members []domain.MemberDef
	// Params 是这一期各成员用的参数（成员键 → 快照）。
	Params     map[string]domain.ParamSnapshot
	PeriodTime time.Time
	// UniverseSubjects、UpstreamFailed 是周期完成事件里的全集与失败标的（universe_subject_ids、failed_subjects）。
	UniverseSubjects []string
	UpstreamFailed   []string
	CarryColumns     []string
	Budget           time.Duration
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
	expected := uniqueSorted(input.UniverseSubjects)
	upstreamFailed := intersectLists(expected, uniqueSorted(input.UpstreamFailed))
	return Plan{
		Mode: ModeLive, Set: input.Set, Members: members, Params: input.Params, PeriodTime: target,
		TargetStart: window[0], TargetEnd: target.Add(duration),
		Expected: expected, Available: subjectDifference(expected, upstreamFailed), UpstreamFailed: upstreamFailed,
		CarryColumns: uniqueSorted(input.CarryColumns), WriteCarry: true,
		Budget: input.Budget,
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

// subjectSet 把标的列表做成集合。
func subjectSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
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
