package eastmoney

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestDateRange(t *testing.T) {
	five := marketdata.Frequency5Min
	for _, tc := range []struct {
		name             string
		start, end, now  time.Time
		wantBeg, wantEnd string
	}{
		{
			name:  "冬令时整天：首根标签 22:35、末根次日 05:00",
			start: cst("2026-11-11", "22:30:00"), end: cst("2026-11-12", "05:00:00"),
			wantBeg: "20261111", wantEnd: "20261113",
		},
		{
			name:  "夏令时整天：首根标签 21:35 减 22:30 落在前一天，beg 要取前一天",
			start: cst("2026-10-29", "21:30:00"), end: cst("2026-10-30", "04:00:00"),
			wantBeg: "20261028", wantEnd: "20261031",
		},
		{
			name:  "夏令时的周一：首根 21:35 的 beg 是周日",
			start: cst("2026-10-26", "21:30:00"), end: cst("2026-10-27", "04:00:00"),
			wantBeg: "20261025", wantEnd: "20261028",
		},
		{
			name:  "冬令时的周一：首根 22:35，beg 是周一当天",
			start: cst("2026-11-09", "22:30:00"), end: cst("2026-11-10", "05:00:00"),
			wantBeg: "20261109", wantEnd: "20261111",
		},
		{
			name:  "半日市：末根 02:00",
			start: cst("2026-11-27", "22:30:00"), end: cst("2026-11-28", "02:00:00"),
			wantBeg: "20261127", wantEnd: "20261129",
		},
		{
			name:  "跨多个交易日",
			start: cst("2026-11-10", "22:30:00"), end: cst("2026-11-12", "05:00:00"),
			wantBeg: "20261110", wantEnd: "20261113",
		},
		{
			name:  "跨夏令时结束（11-01）的区间",
			start: cst("2026-10-30", "21:30:00"), end: cst("2026-11-03", "05:00:00"),
			wantBeg: "20261029", wantEnd: "20261104",
		},
		{
			name:  "实时请求：目标是 22:50 那一根",
			start: cst("2026-11-12", "22:45:00"), end: cst("2026-11-12", "22:50:00"),
			wantBeg: "20261112", wantEnd: "20261113",
		},
		{
			name:  "out11 实测：夏令时周一首根 21:35，beg 是周日（beg=20261025&end=20261027 返回 78 根）",
			start: cst("2026-10-26", "21:30:00"), end: cst("2026-10-26", "21:35:00"),
			wantBeg: "20261025", wantEnd: "20261027",
		},
		{
			name:  "out11 实测：首根标签 22:30（10-28 22:30 起），beg 是当天（beg=20261028 首根 10-28 22:30）",
			start: cst("2026-10-28", "22:25:00"), end: cst("2026-10-30", "04:00:00"),
			wantBeg: "20261028", wantEnd: "20261031",
		},
		{
			name:  "实时请求：夏令时首根 21:35",
			start: cst("2026-10-29", "21:30:00"), end: cst("2026-10-29", "21:35:00"),
			wantBeg: "20261028", wantEnd: "20261030",
		},
		{
			name:  "实时请求：末根次日 05:00，end 是再后一天",
			start: cst("2026-11-12", "04:55:00"), end: cst("2026-11-12", "05:00:00"),
			wantBeg: "20261111", wantEnd: "20261113",
		},
		{
			name:  "日期按北京时间：UTC 还在 11-12 的 21:00（北京时间 11-13 05:00）",
			start: cst("2026-11-12", "22:30:00"), end: cst("2026-11-13", "05:00:00"),
			wantBeg: "20261112", wantEnd: "20261114",
		},
		{
			name: "不带起点（取最近若干根）：不带 beg，end 取请求时刻的日期加一天",
			now:  cst("2026-11-12", "22:50:04"), wantBeg: "", wantEnd: "20261113",
		},
		{
			name:  "只有起点：终点取请求时刻",
			start: cst("2026-11-12", "22:30:00"), now: cst("2026-11-12", "23:06:00"),
			wantBeg: "20261112", wantEnd: "20261113",
		},
		{
			name:    "起止与请求时刻都没有：终点取远期日期",
			wantBeg: "", wantEnd: "20500101",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beg, end := dateRange(marketdata.KlineRequest{StartTime: tc.start, EndTime: tc.end, Now: tc.now}, five)
			require.Equal(t, tc.wantBeg, beg)
			require.Equal(t, tc.wantEnd, end)
		})
	}
}

// 上游模型：东方财富对美股按 beg、end 过滤的规律（实测推出，见计划 0.1 节）——返回收盘标签（北京时间）不早于 beg 日 22:30、
// 在 end 日某个时刻之前的根，这个时刻在 05:00 之后、21:35 之前，数据只能确定到这个范围。两个极端各算一遍。
type upstream struct {
	labels []time.Time
}

// fetch 按 beg、end 取；beg 为空时不带起点，取 end 之前最近 lmt 根。22:30 在这里写死，不引用生产代码的常量，
// 否则生产代码改错了（比如改成 22:25）模型跟着错，测试发现不了。
func (u upstream) fetch(beg, end string, lmt int, cutoff time.Duration, inclusive bool) []time.Time {
	endDay, err := time.ParseInLocation("20060102", end, beijing)
	if err != nil {
		panic(err)
	}
	from, to := time.Time{}, endDay.Add(cutoff)
	if beg != "" {
		begDay, err := time.ParseInLocation("20060102", beg, beijing)
		if err != nil {
			panic(err)
		}
		from = begDay.Add(22*time.Hour + 30*time.Minute)
	}
	var rows []time.Time
	for _, label := range u.labels {
		if label.Before(from) || label.After(to) || (!inclusive && label.Equal(to)) {
			continue
		}
		rows = append(rows, label)
	}
	if beg == "" && len(rows) > lmt {
		rows = rows[len(rows)-lmt:]
	}
	return rows
}

// 两个极端：05:00（含）和 21:35（不含）。
var cutoffs = []struct {
	name      string
	offset    time.Duration
	inclusive bool
}{
	{"05:00 含", 5 * time.Hour, true},
	{"21:35 不含", 21*time.Hour + 35*time.Minute, false},
}

type recordedRequests struct {
	Universe []string `json:"universe"`
	Requests []struct {
		Source string `json:"source"`
		Name   string `json:"name"`
		Beg    string `json:"beg"`
		End    string `json:"end"`
		Lmt    int    `json:"lmt"`
		N      int    `json:"n"`
		First  string `json:"first"`
		Last   string `json:"last"`
	} `json:"requests"`
}

func loadRecordedRequests(t *testing.T) (upstream, recordedRequests) {
	t.Helper()
	var recorded recordedRequests
	require.NoError(t, json.Unmarshal(readTestdata(t, "date_range_requests.json"), &recorded))
	model := upstream{}
	for _, label := range recorded.Universe {
		parsed, err := time.ParseInLocation("2006-01-02 15:04", label, beijing)
		require.NoError(t, err)
		model.labels = append(model.labels, parsed)
	}
	return model, recorded
}

// 上游模型能解释实测的 48 组请求：返回的根数、首根、末根都与实测一致（两个极端的切点结果相同）。
func TestUpstreamModelExplainsRecordedRequests(t *testing.T) {
	model, recorded := loadRecordedRequests(t)
	require.Len(t, model.labels, 1412)
	require.Len(t, recorded.Requests, 48)
	for _, request := range recorded.Requests {
		for _, cutoff := range cutoffs {
			rows := model.fetch(request.Beg, request.End, request.Lmt, cutoff.offset, cutoff.inclusive)
			label := request.Source + " " + request.Name + " " + cutoff.name
			require.Len(t, rows, request.N, label)
			if request.N == 0 {
				continue
			}
			require.Equal(t, request.First, rows[0].In(beijing).Format("2006-01-02 15:04"), label)
			require.Equal(t, request.Last, rows[len(rows)-1].In(beijing).Format("2006-01-02 15:04"), label)
		}
	}
}

// 日期区间规则算出的 beg、end 能取到要取的每一根：按上游模型，每一根的单根请求（实时）、每个交易日的整天请求（补采）
// 都包含全部目标根。universe 是 10-19～11-12 的 1412 根，跨了夏令时结束（11-01）。
func TestDateRangeCoversTargetBars(t *testing.T) {
	model, _ := loadRecordedRequests(t)
	five := marketdata.Frequency5Min
	covers := func(rows []time.Time, want ...time.Time) bool {
		have := map[time.Time]bool{}
		for _, row := range rows {
			have[row] = true
		}
		for _, label := range want {
			if !have[label] {
				return false
			}
		}
		return true
	}

	// 每一根单独取：标签 L，请求区间 [L-5m, L)。
	for _, label := range model.labels {
		beg, end := dateRange(marketdata.KlineRequest{StartTime: label.Add(-5 * time.Minute), EndTime: label}, five)
		for _, cutoff := range cutoffs {
			require.True(t, covers(model.fetch(beg, end, 0, cutoff.offset, cutoff.inclusive), label), "%s 单根 %s", label.In(beijing), cutoff.name)
		}
	}

	// 每个交易日取整天：相邻两根间隔超过 1 小时就是新的一天。
	var sessions [][]time.Time
	for _, label := range model.labels {
		if n := len(sessions); n > 0 && label.Sub(sessions[n-1][len(sessions[n-1])-1]) <= time.Hour {
			sessions[n-1] = append(sessions[n-1], label)
		} else {
			sessions = append(sessions, []time.Time{label})
		}
	}
	require.Len(t, sessions, 18+1, "18 个完整交易日加 11-12 晚上开盘后的那一段")
	for _, session := range sessions {
		first, last := session[0], session[len(session)-1]
		beg, end := dateRange(marketdata.KlineRequest{StartTime: first.Add(-5 * time.Minute), EndTime: last}, five)
		for _, cutoff := range cutoffs {
			require.True(t, covers(model.fetch(beg, end, 0, cutoff.offset, cutoff.inclusive), session...), "%s 整天 %s", first.In(beijing), cutoff.name)
		}
	}
}
