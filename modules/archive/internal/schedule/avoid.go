package schedule

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
)

const (
	// AvoidBeforeSeconds、AvoidAfterSeconds：每个采集触发时刻 T 的 [T − 5s, T + 25s) 不发请求。
	AvoidBeforeSeconds = 5
	AvoidAfterSeconds  = 25
	// HourQuietFrom～HourQuietUntil 是有 1h 及以上频率时每小时的整点窗口 :58:00～:04:00（"MM:SS"）。
	HourQuietFrom  = "58:00"
	HourQuietUntil = "04:00"
)

// Avoidance 是避开采集触发时刻的规则。
type Avoidance struct {
	// EveryMinutes 是触发间隔 N（分钟，整除 60）：触发时刻是 Unix 纪元起分钟数为 N 倍数的第 0 秒；0 表示不限秒。
	EveryMinutes  int
	BeforeSeconds int
	AfterSeconds  int
	// HourQuiet 是每小时不发请求的区间 [from, until)（"MM:SS"，from 大于 until 表示跨整点）；nil 表示不避让整点。
	HourQuiet *[2]string
	// Basis 是给人看的依据，ComputedAt 是计算时间。
	Basis      string
	ComputedAt time.Time
}

// DefaultAvoidance 是还没取到采集频率时的兜底：每分钟 25～55 秒可发、整点 :58～:04 不发。
func DefaultAvoidance() Avoidance {
	return Avoidance{
		EveryMinutes: 1, BeforeSeconds: AvoidBeforeSeconds, AfterSeconds: AvoidAfterSeconds,
		HourQuiet: &[2]string{HourQuietFrom, HourQuietUntil}, Basis: "未取到采集频率，使用默认值",
	}
}

// SpaceFrequencies 是一个空间生效中的采集频率。
type SpaceFrequencies struct {
	SpaceID string
	// Freqs 是该空间采集函数服务的频率（可重复、可无序）。
	Freqs []string
	// FetchedAt 是 Freqs 取到的时间；为零表示从没成功取到过。
	FetchedAt time.Time
	// Failed 为真表示本次读取失败：Freqs 与 FetchedAt 是上一次成功的结果（没有时为空）。
	Failed bool
}

// ComputeAvoidance 由各空间的采集频率推出避让规则（纯函数）。spaces 只应包含有归档任务的空间。
//   - 低于 1h 的频率取分钟数（不足 1 分钟按 1 分钟），N = 它们与 60 的最大公约数；没有时不限秒。
//   - 有任何 ≥ 1h 的频率时加整点窗口 :58～:04。
//   - 读取失败的空间沿用上一次成功的频率；从没成功过的空间按默认值（每分钟触发、有整点）计入。
//   - 全部空间都从没成功过时整体使用默认值。
func ComputeAvoidance(spaces []SpaceFrequencies, now time.Time) Avoidance {
	now = now.UTC()
	if len(spaces) == 0 {
		return Avoidance{Basis: "没有归档任务，不避让", ComputedAt: now}
	}
	sorted := append([]SpaceFrequencies(nil), spaces...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].SpaceID < sorted[j].SpaceID })
	everFetched := false
	for _, sp := range sorted {
		if !sp.FetchedAt.IsZero() {
			everFetched = true
		}
	}
	if !everFetched {
		out := DefaultAvoidance()
		var failed []string
		for _, sp := range sorted {
			failed = append(failed, sp.SpaceID)
		}
		out.Basis = fmt.Sprintf("未取到采集频率，使用默认值（%s 读取失败）", strings.Join(failed, "、"))
		out.ComputedAt = now
		return out
	}
	gcd, hasMinute, hasHour := 60, false, false
	parts := make([]string, 0, len(sorted))
	for _, sp := range sorted {
		if sp.Failed && sp.FetchedAt.IsZero() {
			// 从没取到过：按默认值计入（每分钟触发，有整点）。
			gcd, hasMinute, hasHour = 1, true, true
			parts = append(parts, fmt.Sprintf("%s：读取失败，按默认值", sp.SpaceID))
			continue
		}
		freqs, minutes, hour := classifyFreqs(sp.Freqs)
		for _, m := range minutes {
			gcd, hasMinute = greatestCommonDivisor(gcd, m), true
		}
		hasHour = hasHour || hour
		part := sp.SpaceID + "："
		if len(freqs) == 0 {
			part += "没有生效的采集函数"
		} else {
			part += strings.Join(freqs, "、")
		}
		if sp.Failed {
			part += fmt.Sprintf("（读取失败，沿用 %s 的结果）", sp.FetchedAt.UTC().Format("01-02 15:04 UTC"))
		}
		parts = append(parts, part)
	}
	out := Avoidance{ComputedAt: now}
	var summary []string
	if hasMinute {
		out.EveryMinutes, out.BeforeSeconds, out.AfterSeconds = gcd, AvoidBeforeSeconds, AvoidAfterSeconds
		if gcd == 1 {
			summary = append(summary, "每分钟触发")
		} else {
			summary = append(summary, fmt.Sprintf("每 %d 分钟触发", gcd))
		}
	} else {
		summary = append(summary, "没有 1h 以下的频率，不限秒")
	}
	if hasHour {
		out.HourQuiet = &[2]string{HourQuietFrom, HourQuietUntil}
		summary = append(summary, "有 1h 及以上，整点避让")
	} else {
		summary = append(summary, "不避让整点")
	}
	out.Basis = strings.Join(parts, "；") + "（" + strings.Join(summary, "，") + "）"
	return out
}

// classifyFreqs 去重并按时长排序，返回显示用的频率、低于 1h 的分钟数，以及是否有 ≥ 1h 的频率。无法解析的频率忽略。
func classifyFreqs(raw []string) (display []string, minutes []int, hasHour bool) {
	type item struct {
		name string
		d    time.Duration
	}
	seen := map[string]bool{}
	var items []item
	for _, value := range raw {
		f, err := frequency.Parse(strings.TrimSpace(value))
		if err != nil || seen[string(f)] {
			continue
		}
		seen[string(f)] = true
		d := f.NominalDuration()
		if d <= 0 {
			continue
		}
		items = append(items, item{string(f), d})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].d < items[j].d })
	for _, it := range items {
		display = append(display, it.name)
		if it.d >= time.Hour {
			hasHour = true
			continue
		}
		// 不足 1 分钟的频率（如 30s）按每分钟触发计。
		m := int(it.d / time.Minute)
		if m < 1 {
			m = 1
		}
		minutes = append(minutes, m)
	}
	return display, minutes, hasHour
}

func greatestCommonDivisor(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// parseHourQuiet 把 "MM:SS" 区间解析成小时内的秒偏移。
func parseHourQuiet(q *[2]string) (from, until int, ok bool, err error) {
	if q == nil {
		return 0, 0, false, nil
	}
	if from, err = parseMinuteSecond(q[0]); err != nil {
		return 0, 0, false, err
	}
	if until, err = parseMinuteSecond(q[1]); err != nil {
		return 0, 0, false, err
	}
	return from, until, from != until, nil
}

func parseMinuteSecond(value string) (int, error) {
	minute, second, ok := strings.Cut(strings.TrimSpace(value), ":")
	m, err1 := strconv.Atoi(minute)
	s, err2 := strconv.Atoi(second)
	if !ok || err1 != nil || err2 != nil || m < 0 || m > 59 || s < 0 || s > 59 {
		return 0, fmt.Errorf("整点窗口 %q 必须是 MM:SS", value)
	}
	return m*60 + s, nil
}

// Validate 检查规则：N 为 0 或整除 60，前后秒数非负且合计小于 N 分钟，整点窗口格式正确。
func (a Avoidance) Validate() error {
	if a.EveryMinutes < 0 || a.EveryMinutes > 60 || (a.EveryMinutes > 0 && 60%a.EveryMinutes != 0) {
		return fmt.Errorf("避让的触发间隔 %d 分钟必须整除 60", a.EveryMinutes)
	}
	if a.BeforeSeconds < 0 || a.AfterSeconds < 0 {
		return fmt.Errorf("避让的前后秒数不能为负")
	}
	if a.EveryMinutes > 0 && a.BeforeSeconds+a.AfterSeconds >= a.EveryMinutes*60 {
		return fmt.Errorf("避让的前后秒数合计 %d 必须小于触发间隔 %d 分钟", a.BeforeSeconds+a.AfterSeconds, a.EveryMinutes)
	}
	_, _, _, err := parseHourQuiet(a.HourQuiet)
	return err
}
