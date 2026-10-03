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
	Set            domain.FactorSet
	Factors        []domain.FactorDef
	PeriodTime     time.Time
	Universe       []string
	UpstreamFailed []string
	CarryColumns   []string
	TriggerEventID string
	Budget         time.Duration
}

func BuildLivePlan(clock periodclock.Clock, input LiveInput) (Plan, error) {
	if clock == nil {
		return Plan{}, fmt.Errorf("period clock is required")
	}
	if input.Set.Status != domain.SetStatusEnabled {
		return Plan{}, fmt.Errorf("factor set must be enabled")
	}
	if strings.TrimSpace(input.Set.SpaceID) == "" || strings.TrimSpace(input.Set.SourceDatasetID) == "" || strings.TrimSpace(input.Set.Freq) == "" {
		return Plan{}, fmt.Errorf("factor set identity is incomplete")
	}
	target, err := clock.Align(input.PeriodTime, input.Set.Freq)
	if err != nil {
		return Plan{}, fmt.Errorf("align target period: %w", err)
	}
	duration, err := clock.Duration(input.Set.Freq)
	if err != nil {
		return Plan{}, err
	}
	factors := make([]domain.FactorDef, 0, len(input.Factors))
	maxLookback := 1
	for _, factor := range input.Factors {
		if factor.Status != domain.FactorStatusEnabled {
			continue
		}
		factor.InputColumns = append([]string(nil), factor.InputColumns...)
		factor.Outputs = append([]string(nil), factor.Outputs...)
		factors = append(factors, factor)
		if factor.LookbackPeriods > maxLookback {
			maxLookback = factor.LookbackPeriods
		}
	}
	sort.Slice(factors, func(i, j int) bool { return factors[i].FactorID < factors[j].FactorID })
	window, err := clock.Window(target, input.Set.Freq, maxLookback)
	if err != nil {
		return Plan{}, err
	}
	expected := intersectSubjects(input.Universe, input.Set)
	upstreamFailed := intersectLists(expected, input.UpstreamFailed)
	failedSet := make(map[string]struct{}, len(upstreamFailed))
	for _, subject := range upstreamFailed {
		failedSet[subject] = struct{}{}
	}
	available := make([]string, 0, len(expected)-len(upstreamFailed))
	for _, subject := range expected {
		if _, failed := failedSet[subject]; !failed {
			available = append(available, subject)
		}
	}
	return Plan{
		Mode: ModeLive, Set: input.Set, Factors: factors,
		TargetStart: window[0], TargetEnd: target.Add(duration),
		Expected: expected, Available: available, UpstreamFailed: upstreamFailed,
		CarryColumns: uniqueSorted(input.CarryColumns), WriteCarry: true,
		TriggerEventID: strings.TrimSpace(input.TriggerEventID), Budget: input.Budget,
	}, nil
}

func intersectSubjects(universe []string, set domain.FactorSet) []string {
	unique := uniqueSorted(universe)
	out := make([]string, 0, len(unique))
	for _, subject := range unique {
		if set.InScope(subject) {
			out = append(out, subject)
		}
	}
	return out
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
