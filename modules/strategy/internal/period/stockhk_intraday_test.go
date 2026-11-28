package period

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/marketcalendar"
)

// hkAt 返回香港时间 2026 年的某一时刻。
func hkAt(t *testing.T, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	location, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	return time.Date(2026, month, day, hour, minute, 0, 0, location).UTC()
}

func hkPeriod(t *testing.T, start time.Time) PeriodBoundaries {
	t.Helper()
	period, err := FromStorageStart("hk_stock", "5m", start)
	if err != nil {
		t.Fatal(err)
	}
	return period
}

// 港股日历支持日线与整除两个交易时段（09:30-12:00、13:00-16:00）的日内周期；美股只是多登记一个市场，同样能用。
func TestHKCalendarSupportsIntraday(t *testing.T) {
	for _, bar := range []string{"1m", "5m", "15m", "30m", "1d"} {
		if err := CheckCalendar("hk_stock", bar); err != nil {
			t.Fatalf("港股应支持 %s：%v", bar, err)
		}
	}
	if err := CheckCalendar("hk_stock", "7m"); !errors.Is(err, ErrUnsupportedCalendar) || !strings.Contains(err.Error(), `"hk_stock"`) {
		t.Fatalf("港股不支持 7m，报错要写日历 ID：%v", err)
	}
	if err := CheckCalendar("us_stock", "5m"); err != nil {
		t.Fatalf("美股的日历查表即可用：%v", err)
	}
	if err := CheckCalendar("no_such", "5m"); !errors.Is(err, ErrUnsupportedCalendar) {
		t.Fatalf("没有登记的日历应拒绝：%v", err)
	}
	if !SessionCalendar("hk_stock") || !SessionCalendar("cn_stock") || SessionCalendar(DefaultCalendar) {
		t.Fatal("SessionCalendar 应只对股票市场的日历为真")
	}
	if bars := TradingDayBars("hk_stock", "5m"); bars != 66 {
		t.Fatalf("港股完整交易日 5m 是 66 根：%d", bars)
	}
	if bars := TradingDayBars("us_stock", "5m"); bars != 78 {
		t.Fatalf("美股完整交易日 5m 是 78 根：%d", bars)
	}
}

// 港股 5m 跨午休与隔夜：11:55 那根之后是 13:00 那根；15:55 那根之后是下一个交易日 09:30 那根，中间不占根数。
// 2026-11-17 是周二，11-18 是周三。
func TestHKIntradayAcrossLunchAndOvernight(t *testing.T) {
	morning := hkPeriod(t, hkAt(t, 11, 17, 11, 55))
	sameTime(t, "11:55 的 bar_end", morning.BarEnd, hkAt(t, 11, 17, 12, 0))
	sameTime(t, "11:55 的下一根 bar_end", morning.NextEnd, hkAt(t, 11, 17, 13, 5))
	afternoon := hkPeriod(t, hkAt(t, 11, 17, 13, 0))
	if afternoon.BarIndex != morning.BarIndex+1 {
		t.Fatalf("午休不占根数：%d → %d", morning.BarIndex, afternoon.BarIndex)
	}
	first := hkPeriod(t, hkAt(t, 11, 17, 9, 30))
	last := hkPeriod(t, hkAt(t, 11, 17, 15, 55))
	if last.BarIndex-first.BarIndex != 65 {
		t.Fatalf("完整交易日 66 根：%d → %d", first.BarIndex, last.BarIndex)
	}
	sameTime(t, "收盘那根的 bar_end", last.BarEnd, hkAt(t, 11, 17, 16, 0))
	sameTime(t, "收盘那根的下一根", last.NextEnd, hkAt(t, 11, 18, 9, 35))
	next := hkPeriod(t, hkAt(t, 11, 18, 9, 30))
	if next.BarIndex != last.BarIndex+1 {
		t.Fatalf("隔夜不占根数：%d → %d", last.BarIndex, next.BarIndex)
	}
	// 有效期往后 2 根：12:00 那根到 13:10，16:00 那根到次日 09:40。
	for name, tc := range map[string]struct{ end, want time.Time }{
		"午休前": {hkAt(t, 11, 17, 12, 0), hkAt(t, 11, 17, 13, 10)},
		"收盘":  {hkAt(t, 11, 17, 16, 0), hkAt(t, 11, 18, 9, 40)},
	} {
		valid, err := AdvanceBarEnd("hk_stock", "5m", tc.end, 2)
		if err != nil {
			t.Fatal(err)
		}
		sameTime(t, name+"的有效期", valid, tc.want)
	}
	for name, tc := range map[string]struct{ trigger, start time.Time }{
		"午休中":      {hkAt(t, 11, 17, 12, 30), hkAt(t, 11, 17, 11, 55)},
		"13:05 收盘": {hkAt(t, 11, 17, 13, 5), hkAt(t, 11, 17, 13, 0)},
		"收盘后":      {hkAt(t, 11, 17, 18, 0), hkAt(t, 11, 17, 15, 55)},
		"次日开盘前":    {hkAt(t, 11, 18, 9, 0), hkAt(t, 11, 17, 15, 55)},
		"周六":       {hkAt(t, 11, 21, 10, 0), hkAt(t, 11, 20, 15, 55)},
	} {
		closed, err := ClosedPeriod("hk_stock", "5m", tc.trigger)
		if err != nil {
			t.Fatal(err)
		}
		sameTime(t, name+"最近闭合的一根", closed.StorageStart, tc.start)
	}
	// 历史回看跨午休：以 13:00 为最后一根共 7 根，第一根是 11:30。
	start, err := HistoryStart("hk_stock", "5m", hkAt(t, 11, 17, 13, 0), 7)
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "跨午休的回看起点", start, hkAt(t, 11, 17, 11, 30))
	// A 股的时段在港股日历上不成立：港股 11:30 之后上午还有根，12:00 才午休。
	if _, err := FromStorageStart("hk_stock", "5m", hkAt(t, 11, 17, 12, 30)); err == nil || !strings.Contains(err.Error(), "港股交易时段") || !strings.Contains(err.Error(), "香港时间") {
		t.Fatalf("午休里的时刻不是 bar 起点，报错写港股与香港时间：%v", err)
	}
	if _, err := FromStorageStart("hk_stock", "5m", hkAt(t, 11, 17, 11, 30)); err != nil {
		t.Fatalf("港股 11:30 仍在上午时段：%v", err)
	}
}

// 半日市（2026-12-24 圣诞前夕，12:00 收市）只有上午 30 根：最后一根是 11:55，之后直接到下一个交易日的 09:30；
// 交易日轴按当天实际的根数换算，半日市之后的交易日不会被错位。
func TestHKHalfDayBars(t *testing.T) {
	first := hkPeriod(t, hkAt(t, 12, 24, 9, 30))
	last := hkPeriod(t, hkAt(t, 12, 24, 11, 55))
	if last.BarIndex-first.BarIndex != 29 {
		t.Fatalf("半日市只有 30 根：%d → %d", first.BarIndex, last.BarIndex)
	}
	sameTime(t, "半日市最后一根的 bar_end", last.BarEnd, hkAt(t, 12, 24, 12, 0))
	if _, err := FromStorageStart("hk_stock", "5m", hkAt(t, 12, 24, 13, 0)); err == nil {
		t.Fatal("半日市下午没有 bar")
	}
	next, err := FromBarEnd("hk_stock", "5m", last.NextEnd)
	if err != nil {
		t.Fatal(err)
	}
	if next.BarIndex != last.BarIndex+1 {
		t.Fatalf("半日市收市后到下一个交易日不占根数：%d → %d", last.BarIndex, next.BarIndex)
	}
	location, _ := time.LoadLocation("Asia/Hong_Kong")
	if local := next.StorageStart.In(location); local.Hour() != 9 || local.Minute() != 30 || !local.After(hkAt(t, 12, 24, 23, 59)) {
		t.Fatalf("半日市之后的下一根应是之后某个交易日的 09:30：%s", local)
	}
	// 下午收到的触发：最近闭合的一根仍是 11:55。
	closed, err := ClosedPeriod("hk_stock", "5m", hkAt(t, 12, 24, 15, 0))
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "半日市下午最近闭合的一根", closed.StorageStart, hkAt(t, 12, 24, 11, 55))

	// 交易日轴：半日市当天与其后一个交易日的第一根、最后一根序号都按实际根数取。
	axis, err := dayAxisFor("hk_stock", "5m")
	if err != nil {
		t.Fatal(err)
	}
	_, halfIndex, err := axis.dayOf(last)
	if err != nil {
		t.Fatal(err)
	}
	if axis.firstBar(halfIndex) != first.BarIndex || axis.lastBar(halfIndex) != last.BarIndex {
		t.Fatalf("半日市当天的首末根：%d、%d，期望 %d、%d", axis.firstBar(halfIndex), axis.lastBar(halfIndex), first.BarIndex, last.BarIndex)
	}
	date, nextIndex, err := axis.dayOf(next)
	if err != nil || nextIndex != halfIndex+1 {
		t.Fatalf("半日市后的第一根属于下一个交易日：%s %d（半日市是 %d）err=%v", date, nextIndex, halfIndex, err)
	}
	if axis.firstBar(nextIndex) != next.BarIndex || axis.lastBar(nextIndex) != next.BarIndex+65 {
		t.Fatalf("半日市后一个交易日的首末根：%d、%d，期望 %d、%d", axis.firstBar(nextIndex), axis.lastBar(nextIndex), next.BarIndex, next.BarIndex+65)
	}
	// 没有半日市的 A 股：按实际根数取与“交易日序号 × 每日根数”一致。
	cn, err := dayAxisFor("cn_stock", "5m")
	if err != nil {
		t.Fatal(err)
	}
	if cn.firstBar(10) != 480 || cn.lastBar(10) != 527 || cn.firstBar(-1) != -48 {
		t.Fatalf("A 股的首末根不变：%d、%d、%d", cn.firstBar(10), cn.lastBar(10), cn.firstBar(-1))
	}
}

// 港股日线：bar_start 是日期键，bar_end 是香港 16:00 收盘；半日市是 12:00。
func TestHKDailyCloses(t *testing.T) {
	key := func(month time.Month, day int) time.Time { return time.Date(2026, month, day, 0, 0, 0, 0, time.UTC) }
	normal, err := FromStorageStart("hk_stock", "1d", key(11, 17))
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "普通交易日的收盘", normal.BarEnd, hkAt(t, 11, 17, 16, 0))
	half, err := FromStorageStart("hk_stock", "1d", key(12, 24))
	if err != nil {
		t.Fatal(err)
	}
	sameTime(t, "半日市的收盘", half.BarEnd, hkAt(t, 12, 24, 12, 0))
	// 半日市 12:30 时当天的日线已经闭合；普通交易日 12:30 时最近闭合的是上一个交易日。
	closed, err := ClosedPeriod("hk_stock", "1d", hkAt(t, 12, 24, 12, 30))
	if err != nil || !closed.StorageStart.Equal(key(12, 24)) {
		t.Fatalf("半日市收市后当天日线已闭合：%+v err=%v", closed, err)
	}
	closed, err = ClosedPeriod("hk_stock", "1d", hkAt(t, 11, 17, 12, 30))
	if err != nil || !closed.StorageStart.Equal(key(11, 16)) {
		t.Fatalf("普通交易日午间最近闭合的是上一个交易日：%+v err=%v", closed, err)
	}
	if _, err := FromStorageStart("hk_stock", "1d", key(11, 21)); err == nil || !strings.Contains(err.Error(), "不是港股交易日") {
		t.Fatalf("周六不是港股交易日：%v", err)
	}
	if _, err := FromStorageStart("hk_stock", "1d", time.Date(2030, 1, 3, 0, 0, 0, 0, time.UTC)); !errors.Is(err, marketcalendar.ErrOutOfCoverage) || !strings.Contains(err.Error(), "港股内嵌交易日历") {
		t.Fatalf("超出内嵌日历要说明是港股的日历：%v", err)
	}
}

// 周期标识写市场当地时间；回放区间按交易时段枚举：一个完整交易日加一个半日市共 96 根。
func TestHKLabelsAndReplayBars(t *testing.T) {
	if label := PeriodLabel("hk_stock", "5m", hkAt(t, 11, 17, 13, 0)); label != "2026-11-17 13:00（香港时间）" {
		t.Fatalf("日内的周期标识写香港时间：%s", label)
	}
	if label := BarEndLabel("hk_stock", "1d", hkAt(t, 11, 17, 16, 0)); !strings.HasSuffix(label, "（香港 2026-11-17 收盘）") {
		t.Fatalf("日线的 bar_end 注明香港的交易日收盘：%s", label)
	}
	bars, err := ReplayBars("hk_stock", "5m", hkAt(t, 12, 23, 0, 0), hkAt(t, 12, 25, 0, 0), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 66+30 {
		t.Fatalf("12-23（完整）与 12-24（半日市）共 96 根：%d", len(bars))
	}
	for i := 1; i < len(bars); i++ {
		if bars[i].BarIndex != bars[i-1].BarIndex+1 {
			t.Fatalf("回放的 bar 序号应连续：第 %d 根 %d → %d", i, bars[i-1].BarIndex, bars[i].BarIndex)
		}
	}
	if err := StockCalendarReadiness("hk_stock", hkAt(t, 11, 17, 10, 0), 0, 2); err != nil {
		t.Fatalf("港股日历在 2026-11 可用：%v", err)
	}
	if err := StockCalendarReadiness(DefaultCalendar, hkAt(t, 11, 17, 10, 0), 0, 2); err != nil {
		t.Fatalf("crypto 没有内嵌日历，不检查：%v", err)
	}
}
