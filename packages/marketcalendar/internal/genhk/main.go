// genhk 按港交所公布的假期表生成内嵌的港股交易日历 hk_stock：数据文件与它自己的清单，别的日历的文件不动。
//
// 交易日 = 周一到周五，去掉 closures 里的休市日；earlyCloses 是只开上午市的半日市
// （圣诞前夕、新年前夕、农历新年前夕，12:00 收市）。台风、暴雨等临时停市不登记。
// 数据从 firstYear 年的第一个交易日到 closures 里登记的最后一年年底，每次整份重新生成。
//
// 每年港交所公布下一年的假期表后（来源见 source），把休市日和半日市补进下面两张表，
// 在 packages/marketcalendar 目录下运行：go run ./internal/genhk
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
	calendarID = "hk_stock"
	firstYear  = 2026
	// source 是休市日与半日市的出处，写进清单。
	source = "港交所 Trading Calendar and Holiday Schedule 的 Holiday Schedule 表（2026、2027 年，2026-11-12 取）" +
		" https://www.hkex.com.hk/Services/Trading/Derivatives/Overview/Trading-Calendar-and-Holiday-Schedule?sc_lang=en ；" +
		"半日市只开上午市（09:30～12:00）见" +
		" https://www.hkex.com.hk/Services/Trading-hours-and-Severe-Weather-Arrangements/Trading-Hours/Securities-Market?sc_lang=en"
)

// closures 是各年份落在工作日（周一到周五）上的休市日。
var closures = map[int][]string{
	2026: {
		"2026-01-01",                             // 元旦
		"2026-02-17", "2026-02-18", "2026-02-19", // 农历年初一至初三
		"2026-04-03", // 耶稣受难节
		"2026-04-06", // 清明节翌日
		"2026-04-07", // 复活节星期一翌日
		"2026-05-01", // 劳动节
		"2026-05-25", // 佛诞翌日
		"2026-06-19", // 端午节
		"2026-07-01", // 香港特别行政区成立纪念日
		"2026-10-01", // 国庆日
		"2026-10-19", // 重阳节翌日
		"2026-12-25", // 圣诞节
	},
	2027: {
		"2027-01-01",               // 元旦
		"2027-02-08", "2027-02-09", // 农历年初三、初四（初一、初二是周末）
		"2027-03-26", // 耶稣受难节
		"2027-03-29", // 复活节星期一
		"2027-04-05", // 清明节
		"2027-05-13", // 佛诞
		"2027-06-09", // 端午节
		"2027-07-01", // 香港特别行政区成立纪念日
		"2027-09-16", // 中秋节翌日
		"2027-10-01", // 国庆日
		"2027-10-08", // 重阳节
		"2027-12-27", // 圣诞节后第一个工作日（圣诞节是周六）
	},
}

// earlyCloses 是半日市：日期 → 当天的收市时刻。
var earlyCloses = map[string]string{
	"2026-02-16": "12:00", // 农历新年前夕
	"2026-12-24": "12:00", // 圣诞前夕
	"2026-12-31": "12:00", // 新年前夕
	"2027-02-05": "12:00", // 农历新年前夕
	"2027-12-24": "12:00", // 圣诞前夕
	"2027-12-31": "12:00", // 新年前夕
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
	// 数据没有变化时不动清单，免得只改了生成日期。
	if existing, err := os.ReadFile(dataPath); err == nil && string(existing) == string(dataRaw) {
		if _, err := os.Stat(manifestPath); err == nil {
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
