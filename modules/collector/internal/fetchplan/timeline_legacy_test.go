package fetchplan_test

import (
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/klinecheck"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// legacyBuildTimeline 是提交 2255b045 上 klinecheck.BuildTimeline 的原样拷贝（连同它用到的 segmentSeries、closeSegment）：
// 每次把全部采集计划记录重放一遍，还原每段时间应有的标的。批 2 把时间线拆成一张表（T3）之后，
// 用它当对照：同一个场景里，重放计划记录得到的时间线必须与从新表构造的逐字段相等。
// 除了包名限定，不要改这里的任何逻辑。
func legacyBuildTimeline(records []*store.FetchPlanRecord) (klinecheck.Timeline, error) {
	timeline := klinecheck.Timeline{}
	for _, record := range records {
		content, err := fetchplan.DecodeFetchPlanContent(record.Plan)
		if err != nil {
			return nil, err
		}
		progress, err := fetchplan.DecodeFetchPlanProgress(record.Progress)
		if err != nil {
			return nil, err
		}
		applied := record.Status == store.FetchPlanApplied
		keys := make([]string, 0, len(content.Datasets))
		for key := range content.Datasets {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			change := content.Datasets[key]
			if !change.Changed || change.P <= 0 {
				continue
			}
			switch {
			case change.Gone:
				legacyCloseSegment(timeline, key, change.P)
			case change.HashChanged && (applied || progress.Registered[key]):
				legacyCloseSegment(timeline, key, change.P)
				subjects, byTag := legacySegmentSeries(change.Series)
				timeline[key] = append(timeline[key], klinecheck.Segment{From: change.P, Subjects: subjects, Series: byTag, LatenessS: change.LatenessS, OutputFields: change.OutputFields})
			}
		}
		// 快照清空发生在这份计划生效之后，所以先按上面还原它的变更，再丢掉此前的全部计划段。
		if len(content.ClearedDatasets()) > 0 {
			clear(timeline)
		}
	}
	return timeline, nil
}

// legacySegmentSeries 把计划里的序列（标的、tag）整理成一段的标的与按 tag 的成员：只有出现 ≥2 个不同 tag 才返回按序列的成员。
func legacySegmentSeries(series []seriesset.Series) (subjects []string, byTag map[string][]string) {
	seen := map[string]bool{}
	members := map[string][]string{}
	for _, item := range series {
		subject := strings.ToUpper(item.SubjectID)
		if !seen[subject] {
			seen[subject] = true
			subjects = append(subjects, subject)
		}
		tag := strings.TrimSpace(item.SeriesTag)
		members[tag] = append(members[tag], subject)
	}
	sort.Strings(subjects)
	if len(members) < 2 {
		return subjects, nil
	}
	for tag := range members {
		sort.Strings(members[tag])
	}
	return subjects, members
}

// legacyCloseSegment 把最后一段在 at 处结束。
func legacyCloseSegment(timeline klinecheck.Timeline, key string, at int64) {
	segments := timeline[key]
	if len(segments) == 0 {
		return
	}
	last := &segments[len(segments)-1]
	if last.To == 0 || last.To > at {
		last.To = at
	}
}

// 新旧等价的证据：同一个场景、同一时刻，左边用旧算法重放全部计划记录，右边用 klinecheck.BuildTimeline
// 从时间线表读出的行构造，两者逐字段相等。场景是驱动对账器跑出来的，所以“什么时候写入时间线”也一起验证了。
func TestLegacyTimelineMatchesBuildTimeline(t *testing.T) {
	shapes := map[string]string{}
	for _, scenario := range fetchplan.TimelineScenarios(t) {
		want, err := legacyBuildTimeline(scenario.Records)
		require.NoError(t, err, scenario.Name)
		got := klinecheck.BuildTimeline(scenario.Timeline)
		require.Equal(t, want, got, scenario.Name)
		shapes[scenario.Name] = timelineShape(want)
	}
	// 场景确实走到了各自要覆盖的形状，防止夹具变化后悄悄退化（每个键后面是各段的“起点状态:标的数”）。
	require.Equal(t, map[string]string{
		"subject_change/first_plan_registered_not_applied":  "ds_spot|1m=[open:120]",
		"subject_change/first_plan_applied":                 "ds_spot|1m=[open:120]",
		"subject_change/second_plan_registered_not_applied": "ds_spot|1m=[closed:120 open:119]",
		"subject_change/second_plan_applied":                "ds_spot|1m=[closed:120 open:119]",
		"retire_functions":                                  "ds_spot|1m=[closed:120 open:40]",
		"dataset_gone":                                      "ds_spot_1m|1m=[closed:60]",
		"dataset_gone_then_back":                            "ds_spot_1m|1m=[closed:60 open:60]",
		"reregister/hashes_cleared":                         "",
		"reregister/new_plan_registered_not_applied":        "ds_spot|1m=[open:60]",
		"reregister/new_plan_applied":                       "ds_spot|1m=[open:60]",
		"unified_dataset":                                   "ds_unified|1m=[closed:60/2tags open:59/2tags]",
		"handoff_scf_stream_scf":                            "ds_spot_1h|1h=[open:60] ds_spot_1m|1m=[open:60]",
		"stale_period/saved_not_registered":                 "",
		"stale_period/reselected_not_registered":            "",
		"stale_period/applied":                              "ds_spot|1m=[open:60]",
		"subject_increase":                                  "ds_spot|1m=[closed:60 open:61]",
		"pause_resume/paused_registered_not_applied":        "ds_spot|1m=[open:60]",
		"pause_resume/resumed_not_applied":                  "ds_spot|1m=[open:60]",
		"pause_resume/applied":                              "ds_spot|1m=[open:60]",
		"change_while_open/first_still_open":                "ds_spot|1m=[open:120]",
		"change_while_open/both_applied":                    "ds_spot|1m=[closed:120 open:119]",
	}, shapes)
}

// timelineShape 把时间线压成一行摘要。
func timelineShape(timeline klinecheck.Timeline) string {
	keys := make([]string, 0, len(timeline))
	for key := range timeline {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		segments := make([]string, 0, len(timeline[key]))
		for _, segment := range timeline[key] {
			state := "open"
			if segment.To != 0 {
				state = "closed"
			}
			item := state + ":" + strconv.Itoa(len(segment.Subjects))
			if len(segment.Series) > 0 {
				item += "/" + strconv.Itoa(len(segment.Series)) + "tags"
			}
			segments = append(segments, item)
		}
		parts = append(parts, key+"=["+strings.Join(segments, " ")+"]")
	}
	return strings.Join(parts, " ")
}
