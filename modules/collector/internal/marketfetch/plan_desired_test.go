package marketfetch

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/packages/seriesset"
)

func testPlanOptions() PlanOptions {
	return PlanOptions{
		SpaceID: "crypto", Markets: map[string][]string{"spot": {"ap-nanjing"}, "swap": {"ap-hongkong"}},
		SymbolsPerFunction: 50, DefaultLatenessS: 15,
		Fetch: PlanFetch{Concurrency: 5, RPS: 50, Burst: 50, FeedConcurrency: 10},
	}
}

func testGroups(count int, frequencies ...string) []TaskGroup {
	subjects := make([]string, 0, count)
	for i := 0; i < count; i++ {
		subjects = append(subjects, fmt.Sprintf("S%03d-USDT", i))
	}
	groups := make([]TaskGroup, 0, len(frequencies))
	for _, frequency := range frequencies {
		groups = append(groups, TaskGroup{
			Provider: "binance", SourceID: "binance", MarketType: "spot", MarketID: "crypto", InstrumentType: "spot",
			DatasetID: "ds_spot_" + frequency, Frequency: frequency, Subjects: append([]string(nil), subjects...),
		})
	}
	return groups
}

func choose(now time.Time) func(string) (int64, error) {
	return func(frequency string) (int64, error) {
		return ChoosePeriodStartWithLead(frequency, now, planLeadTime)
	}
}

func TestBuildDesiredStateShardsAndIndexes(t *testing.T) {
	state, err := BuildDesiredState(testPlanOptions(), testGroups(120, "1m", "1h"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Functions) != 3 {
		t.Fatalf("functions=%d want 3", len(state.Functions))
	}
	seen := map[string]string{}
	for name, function := range state.Functions {
		if !strings.Contains(name, PlanFleetMarker) || function.Region != "ap-nanjing" || function.Cron != "0 * * * * * *" {
			t.Fatalf("unexpected function %s %+v", name, function)
		}
		dataset := findPlanDataset(function.Plan, "ds_spot_1m|1m")
		if dataset == nil || len(dataset.Copies) != 1 || len(dataset.Copies[0].Subjects) > 50 {
			t.Fatalf("bad dataset in %s: %+v", name, dataset)
		}
		for _, subject := range dataset.Copies[0].Subjects {
			if previous, dup := seen[subject.ID]; dup {
				t.Fatalf("%s assigned to %s and %s", subject.ID, previous, name)
			}
			seen[subject.ID] = name
		}
		// 序号必须等于规范排序后的下标。
		for _, subject := range dataset.Copies[0].Subjects {
			if want := state.Datasets["ds_spot_1m|1m"].index[subject.ID]; want != subject.Index {
				t.Fatalf("%s index=%d want %d", subject.ID, subject.Index, want)
			}
		}
	}
	if len(seen) != 120 {
		t.Fatalf("assigned %d subjects want 120", len(seen))
	}
	hourly, err := BuildDesiredState(testPlanOptions(), testGroups(10, "1h"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, function := range hourly.Functions {
		if function.Cron != "0 0 * * * * *" {
			t.Fatalf("hourly cron=%q", function.Cron)
		}
	}
}

func TestBuildDesiredStateHashMatchesSharedPackage(t *testing.T) {
	state, err := BuildDesiredState(testPlanOptions(), testGroups(5, "1m"), nil)
	if err != nil {
		t.Fatal(err)
	}
	dataset := state.Datasets["ds_spot_1m|1m"]
	series := make([]seriesset.Series, 0, 5)
	for i := 0; i < 5; i++ {
		series = append(series, seriesset.Series{SubjectID: fmt.Sprintf("S%03d-USDT", i), SeriesTag: PlanSeriesTag("crypto", PlanMarket{Provider: "binance", SourceID: "binance", MarketType: "spot"})})
	}
	set, err := seriesset.Canonicalize(series)
	if err != nil || set.Hash != dataset.Hash || dataset.Count != 5 {
		t.Fatalf("hash=%s want %s count=%d err=%v", dataset.Hash, set.Hash, dataset.Count, err)
	}
}

func TestBuildDesiredStateRequiresRegions(t *testing.T) {
	options := testPlanOptions()
	options.Markets = nil
	if _, err := BuildDesiredState(options, testGroups(3, "1m"), nil); err == nil {
		t.Fatal("expected a missing region error")
	}
}

func TestStableAssignmentKeepsSubjectsInPlace(t *testing.T) {
	options := testPlanOptions()
	first, err := BuildDesiredState(options, testGroups(120, "1m"), nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := map[string]*PlanFunction{}
	for name, function := range first.Functions {
		previous[name] = function
	}
	groups := testGroups(121, "1m")
	groups[0].Subjects = append(groups[0].Subjects[:5], groups[0].Subjects[6:]...) // 去掉 S005，加上 S120
	second, err := BuildDesiredState(options, groups, previous)
	if err != nil {
		t.Fatal(err)
	}
	moved := 0
	for name, function := range second.Functions {
		before := map[string]struct{}{}
		if old := previous[name]; old != nil {
			for _, subject := range old.Plan.Datasets[0].Copies[0].Subjects {
				before[subject.ID] = struct{}{}
			}
		}
		for _, subject := range function.Plan.Datasets[0].Copies[0].Subjects {
			if _, ok := before[subject.ID]; !ok {
				moved++
			}
		}
	}
	if moved != 1 {
		t.Fatalf("%d subjects changed function, want only the new one", moved)
	}
}

func TestBuildPlanContentLifecycle(t *testing.T) {
	options := testPlanOptions()
	now := time.Date(2026, 10, 12, 8, 30, 20, 0, time.UTC)
	desired, err := BuildDesiredState(options, testGroups(60, "1m"), nil)
	if err != nil {
		t.Fatal(err)
	}
	first, changed, err := BuildPlanContent(nil, nil, desired, choose(now))
	if err != nil || !changed {
		t.Fatalf("first plan changed=%v err=%v", changed, err)
	}
	change := first.Datasets["ds_spot_1m|1m"]
	if want := time.Date(2026, 10, 12, 8, 33, 0, 0, time.UTC).Unix(); change.P != want || !change.HashChanged || change.OldHash != "" {
		t.Fatalf("unexpected first dataset change %+v", change)
	}
	for _, function := range first.Functions {
		if !function.Create || !function.Push {
			t.Fatalf("new function must be created and pushed: %+v", function)
		}
		if _, err := EncodePlan("crypto", first.TransitionalPlan(function)); err != nil {
			t.Fatalf("transitional plan is invalid: %v", err)
		}
		if _, err := EncodePlan("crypto", first.FinalPlan(function)); err != nil {
			t.Fatalf("final plan is invalid: %v", err)
		}
		if dataset := findPlanDataset(first.FinalPlan(function), "ds_spot_1m|1m"); len(dataset.Copies) != 1 || dataset.Copies[0].From != change.P {
			t.Fatalf("final copy must start at P: %+v", dataset)
		}
	}
	if end := first.WindowEnd(); !end.Equal(time.Unix(change.P, 0).Add(15 * time.Second)) {
		t.Fatalf("window end=%s", end)
	}

	// 已生效后没有变化：不产生新计划。
	appliedFunctions, appliedDatasets := first.Applied()
	again, err := BuildDesiredState(options, testGroups(60, "1m"), appliedFunctions)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := BuildPlanContent(appliedFunctions, appliedDatasets, again, choose(now.Add(time.Hour))); err != nil || changed {
		t.Fatalf("unchanged state produced a plan: changed=%v err=%v", changed, err)
	}

	// 去掉一个标的：每个函数都带两份，旧份在前、新份从 P 起。
	groups := testGroups(60, "1m")
	groups[0].Subjects = groups[0].Subjects[1:]
	next, err := BuildDesiredState(options, groups, appliedFunctions)
	if err != nil {
		t.Fatal(err)
	}
	second, changed, err := BuildPlanContent(appliedFunctions, appliedDatasets, next, choose(now.Add(time.Hour)))
	if err != nil || !changed {
		t.Fatalf("second plan changed=%v err=%v", changed, err)
	}
	secondChange := second.Datasets["ds_spot_1m|1m"]
	if len(secondChange.Removed) != 1 || secondChange.Removed[0] != "S000-USDT" || secondChange.OldHash == "" || secondChange.OldHash == secondChange.Hash {
		t.Fatalf("unexpected second change %+v", secondChange)
	}
	for _, function := range second.Functions {
		dataset := findPlanDataset(second.TransitionalPlan(function), "ds_spot_1m|1m")
		if len(dataset.Copies) != 2 || dataset.Copies[0].From != change.P || dataset.Copies[1].From != secondChange.P {
			t.Fatalf("transitional copies wrong for %s: %+v", function.Name, dataset.Copies)
		}
		if _, ok := dataset.SelectCopy(time.Unix(secondChange.P-60, 0)); !ok {
			t.Fatal("old copy must serve periods before P")
		}
		selected, _ := dataset.SelectCopy(time.Unix(secondChange.P, 0))
		if selected.Hash != secondChange.Hash {
			t.Fatal("new copy must serve P itself")
		}
		if final := second.FinalPlan(function); len(findPlanDataset(final, "ds_spot_1m|1m").Copies) != 1 {
			t.Fatal("final plan keeps a single copy")
		}
	}
}

func TestBuildPlanContentRetiresFunctionsAndDatasets(t *testing.T) {
	options := testPlanOptions()
	now := time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)
	desired, _ := BuildDesiredState(options, testGroups(120, "1m"), nil)
	first, _, err := BuildPlanContent(nil, nil, desired, choose(now))
	if err != nil {
		t.Fatal(err)
	}
	functions, datasets := first.Applied()
	small, err := BuildDesiredState(options, testGroups(20, "1m"), functions)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := BuildPlanContent(functions, datasets, small, choose(now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	retired := 0
	for _, function := range second.Functions {
		if function.New != nil {
			continue
		}
		retired++
		transitional := second.TransitionalPlan(function)
		dataset := findPlanDataset(transitional, "ds_spot_1m|1m")
		if len(dataset.Copies) != 2 || len(dataset.Copies[1].Subjects) != 0 {
			t.Fatalf("retired function must stop after P: %+v", dataset.Copies)
		}
		if _, err := EncodePlan("crypto", transitional); err != nil {
			t.Fatal(err)
		}
		if second.FinalPlan(function) != nil || second.FinalCron(function) != "" {
			t.Fatal("retired function has no final plan")
		}
	}
	if retired == 0 {
		t.Fatal("expected retired functions")
	}
	// 数据集整体不再采集：生成的新份没有标的。
	third, changed, err := BuildPlanContent(functions, datasets, &DesiredState{Datasets: map[string]*DesiredDataset{}, Functions: map[string]*PlanFunction{}}, choose(now))
	if err != nil || !changed || !third.Datasets["ds_spot_1m|1m"].Gone {
		t.Fatalf("gone dataset: changed=%v err=%v", changed, err)
	}
}

func TestChoosePeriodStartWithLead(t *testing.T) {
	now := time.Date(2026, 10, 12, 8, 59, 30, 0, time.UTC)
	if p, _ := ChoosePeriodStartWithLead("1h", now, planLeadTime); p != time.Date(2026, 10, 12, 9, 2, 0, 0, time.UTC).Truncate(time.Hour).Add(time.Hour).Unix() {
		t.Fatalf("1h P=%s", time.Unix(p, 0).UTC())
	}
	if p, _ := ChoosePeriodStartWithLead("1m", now, planLeadTime); p != time.Date(2026, 10, 12, 9, 2, 0, 0, time.UTC).Unix() {
		t.Fatalf("1m P=%s", time.Unix(p, 0).UTC())
	}
}

func TestNextBoundaryAlignsWithKlineGrid(t *testing.T) {
	cases := []struct {
		frequency string
		at        time.Time
		want      time.Time
	}{
		// 1h、1d 与原先按 Unix 秒取整的结果相同。
		{"1h", time.Date(2026, 10, 12, 9, 2, 0, 0, time.UTC), time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)},
		{"1h", time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)},
		{"1d", time.Date(2026, 10, 12, 0, 0, 1, 0, time.UTC), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		{"1d", time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)},
		// 1w 落在周一 UTC 零点，而不是 Unix 取模得到的周四。
		{"1w", time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
		{"1w", time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
		{"1w", time.Date(2026, 10, 19, 0, 0, 1, 0, time.UTC), time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)},
		// 非 UTC 输入按同一时刻处理。
		{"1w", time.Date(2026, 10, 19, 7, 0, 0, 0, time.FixedZone("CST", 8*60*60)), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)},
		{"1mo", time.Date(2026, 10, 1, 0, 0, 1, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		frequency, err := marketdata.ParseFrequency(tc.frequency)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.frequency, err)
		}
		got := nextBoundary(frequency, tc.at)
		if !got.Equal(tc.want) || got.Location() != time.UTC {
			t.Fatalf("%s at %s: got %s want %s", tc.frequency, tc.at, got, tc.want)
		}
		if frequency.Duration() > 0 {
			// 与采集触发（frequencyDue）、检查（alignUp）用的是同一张网格。
			if !got.Truncate(frequency.Duration()).Equal(got) {
				t.Fatalf("%s boundary %s is not on the Truncate grid", tc.frequency, got)
			}
		}
	}
	if weekday := nextBoundary(marketdata.FrequencyWeek, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)).Weekday(); weekday != time.Monday {
		t.Fatalf("1w boundary weekday=%s", weekday)
	}
}
