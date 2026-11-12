// genus 按纽交所公布的假期表生成内嵌的美股交易日历 us_stock：数据文件与它自己的清单，别的日历的文件不动。
//
// 交易日 = 周一到周五，去掉 closures 里的休市日；earlyCloses 是 13:00 提前收市的半日市
// （感恩节次日、平安夜等，以纽交所公布为准）。休市日与半日市都照假期页逐年登记，
// 不套“节假日逢周六提前一天补休”之类的规则：2027-12-31 照常交易。
// 夏令时由 barclock 按纽约时区换算，日历里不用管。
// 数据从 firstYear 年的第一个交易日到 closures 里登记的最后一年年底，每次整份重新生成。
//
// 每年纽交所公布下一年的假期表后（来源见 source），把休市日和半日市补进下面两张表，
// 在 packages/marketcalendar 目录下运行：go run ./internal/genus
//
// 补登或改动半日市会让之后的交易序号整体平移，因子本地存储要重建。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	calendarID = "us_stock"
	firstYear  = 2026
	// source 是休市日与半日市的出处，写进清单。
	source = "纽约证券交易所假期与提前收市日 https://www.nyse.com/trade/hours-calendars ；" +
		"2026 年另与纳斯达克交易所公告 https://www.nasdaqtrader.com/Trader.aspx?id=Calendar 核对一致，" +
		"2027 年另与 https://upstox.com/learning-center/us-stocks/us-stock-market-holidays/article-1947/ 核对一致"
)

// closures 是各年份落在工作日（周一到周五）上的休市日。
var closures = map[int][]string{
	2026: {
		"2026-01-01", // 元旦
		"2026-01-19", // 马丁·路德·金纪念日
		"2026-02-16", // 总统日（华盛顿诞辰）
		"2026-04-03", // 耶稣受难节
		"2026-05-25", // 阵亡将士纪念日
		"2026-06-19", // 六月节
		"2026-07-03", // 独立日（7-4 是周六，周五补休）
		"2026-09-07", // 劳动节
		"2026-11-26", // 感恩节
		"2026-12-25", // 圣诞节
	},
	2027: {
		"2027-01-01", // 元旦
		"2027-01-18", // 马丁·路德·金纪念日
		"2027-02-15", // 总统日（华盛顿诞辰）
		"2027-03-26", // 耶稣受难节
		"2027-05-31", // 阵亡将士纪念日
		"2027-06-18", // 六月节（6-19 是周六，周五补休）
		"2027-07-05", // 独立日（7-4 是周日，周一补休）
		"2027-09-06", // 劳动节
		"2027-11-25", // 感恩节
		"2027-12-24", // 圣诞节（12-25 是周六，周五补休）
	},
}

// earlyCloses 是半日市：日期 → 当天的收市时刻。
var earlyCloses = map[string]string{
	"2026-11-27": "13:00", // 感恩节次日
	"2026-12-24": "13:00", // 平安夜
	"2027-11-26": "13:00", // 感恩节次日
}

type calendarFile struct {
	CalendarID  string            `json:"calendar_id"`
	TradingDays []string          `json:"trading_days"`
	EarlyCloses map[string]string `json:"early_closes"`
}

type manifest struct {
	CalendarID   string `json:"calendar_id"`
	Source       string `json:"source"`
	Version      int    `json:"version"`
	DataVersion  string `json:"data_version"`
	ValidFrom    string `json:"valid_from"`
	ValidThrough string `json:"valid_through"`
	SHA256       string `json:"sha256"`
}

func main() {
	dir := flag.String("data", "data", "日历数据目录")
	flag.Parse()
	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "生成失败：", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	years := make([]int, 0, len(closures))
	for year := range closures {
		years = append(years, year)
	}
	sort.Ints(years)
	for i, year := range years {
		if year != firstYear+i {
			return fmt.Errorf("closures 的年份必须从 %d 年起连续，缺 %d 年", firstYear, firstYear+i)
		}
	}

	var days []string
	traded := make(map[string]struct{})
	for _, year := range years {
		closedSet := make(map[string]struct{}, len(closures[year]))
		for _, day := range closures[year] {
			parsed, err := time.Parse(time.DateOnly, day)
			if err != nil || parsed.Year() != year {
				return fmt.Errorf("休市日 %q 不是 %d 年的日期", day, year)
			}
			if weekday := parsed.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
				return fmt.Errorf("休市日 %s 是周末，周末本来就休市，不用登记", day)
			}
			closedSet[day] = struct{}{}
		}
		for day := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); day.Year() == year; day = day.AddDate(0, 0, 1) {
			if weekday := day.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
				continue
			}
			text := day.Format(time.DateOnly)
			if _, skip := closedSet[text]; skip {
				continue
			}
			days = append(days, text)
			traded[text] = struct{}{}
		}
	}
	for day := range earlyCloses {
		if _, ok := traded[day]; !ok {
			return fmt.Errorf("半日市 %s 不是交易日", day)
		}
	}

	dataRaw, err := marshal(calendarFile{CalendarID: calendarID, TradingDays: days, EarlyCloses: earlyCloses})
	if err != nil {
		return err
	}
	dataPath := filepath.Join(dir, calendarID+".json")
	manifestPath := filepath.Join(dir, calendarID+".manifest.json")
	// 数据和来源都没有变化时不动清单，免得只改了生成日期。
	if existing, err := os.ReadFile(dataPath); err == nil && string(existing) == string(dataRaw) {
		var old manifest
		if raw, err := os.ReadFile(manifestPath); err == nil && json.Unmarshal(raw, &old) == nil && old.Source == source {
			fmt.Printf("%s 没有变化\n", dataPath)
			return nil
		}
	}
	sum := sha256.Sum256(dataRaw)
	manifestRaw, err := marshal(manifest{
		CalendarID:   calendarID,
		Source:       source,
		Version:      1,
		DataVersion:  time.Now().Format(time.DateOnly),
		ValidFrom:    days[0],
		ValidThrough: days[len(days)-1],
		SHA256:       "sha256:" + hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return err
	}
	if err := os.WriteFile(dataPath, dataRaw, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, manifestRaw, 0o644); err != nil {
		return err
	}
	fmt.Printf("生成 %s：%d 个交易日（%s 至 %s），%d 个半日市\n", calendarID, len(days), days[0], days[len(days)-1], len(earlyCloses))
	return nil
}

// marshal 按现有文件的排版输出：两个空格缩进，文件以换行结尾。
func marshal(value any) ([]byte, error) {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}
