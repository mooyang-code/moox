// gencn 按国务院办公厅公布的节假日安排，给内嵌的 A 股交易日历追加一整年的交易日，并重写清单。
//
// AkShare 的日历要等交易所公告后才更新，而 Strategy 会提前两个交易日判定日历过期，等不起；
// 国办通知每年 11 月前后发布，交易所休市安排与之一致：节假日的工作日休市，调休补班的周末照样休市。
// 交易所 12 月发布休市安排后要核对一遍，有出入以交易所为准，改 closures 后用同一条命令重新生成：
// 数据里已有这一年时，先去掉这一年的交易日再按 closures 重算。
//
// 用法（在 packages/marketcalendar 目录下）：go run ./internal/gencn -year 2027
// 交易所公布的年度日历已进 AkShare 时，也可以改用 scripts/marketdata/update-calendar.sh 整体替换。
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
	"strings"
	"time"
)

// closures 是各年份落在工作日（周一到周五）上的休市日，来源写在 sources 里。
var closures = map[int][]string{
	2027: {
		"2027-01-01",                                                                       // 元旦
		"2027-02-05", "2027-02-08", "2027-02-09", "2027-02-10", "2027-02-11", "2027-02-12", // 春节
		"2027-04-05",                             // 清明节
		"2027-05-03", "2027-05-04", "2027-05-05", // 劳动节
		"2027-06-09",                                                         // 端午节
		"2027-09-15",                                                         // 中秋节
		"2027-10-01", "2027-10-04", "2027-10-05", "2027-10-06", "2027-10-07", // 国庆节
	},
}

var sources = map[int]string{
	2027: "国务院办公厅关于2027年部分节假日安排的通知（国办发明电〔2026〕10号）",
}

type calendarFile struct {
	CalendarID  string   `json:"calendar_id"`
	TradingDays []string `json:"trading_days"`
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
	year := flag.Int("year", 0, "要生成的年份：紧接现有数据的下一年，或者现有数据的最后一年（重新生成）")
	dir := flag.String("data", "data", "日历数据目录")
	flag.Parse()
	if err := run(*year, *dir); err != nil {
		fmt.Fprintln(os.Stderr, "生成失败：", err)
		os.Exit(1)
	}
}

func run(year int, dir string) error {
	closed, ok := closures[year]
	if !ok {
		return fmt.Errorf("没有登记 %d 年的休市日，先把国办通知里的节假日写进 closures", year)
	}
	dataPath := filepath.Join(dir, "cn_trading_days.json")
	manifestPath := filepath.Join(dir, "manifest.json")

	var data calendarFile
	if err := readJSON(dataPath, &data); err != nil {
		return err
	}
	var meta manifest
	if err := readJSON(manifestPath, &meta); err != nil {
		return err
	}
	if len(data.TradingDays) == 0 {
		return fmt.Errorf("%s 没有交易日", dataPath)
	}
	last, err := time.Parse(time.DateOnly, data.TradingDays[len(data.TradingDays)-1])
	if err != nil {
		return fmt.Errorf("最后一个交易日格式不对：%w", err)
	}
	switch last.Year() {
	case year - 1:
	case year:
		// 重新生成：去掉这一年已有的交易日，前一年必须还有数据。
		prefix := fmt.Sprintf("%04d-", year)
		kept := data.TradingDays[:0]
		for _, day := range data.TradingDays {
			if !strings.HasPrefix(day, prefix) {
				kept = append(kept, day)
			}
		}
		data.TradingDays = kept
		if len(data.TradingDays) == 0 {
			return fmt.Errorf("去掉 %d 年后没有剩余的交易日", year)
		}
	default:
		return fmt.Errorf("现有数据止于 %s，只能追加 %d 年或重新生成 %d 年", last.Format(time.DateOnly), last.Year()+1, last.Year())
	}

	closedSet := make(map[string]struct{}, len(closed))
	for _, day := range closed {
		parsed, err := time.Parse(time.DateOnly, day)
		if err != nil || parsed.Year() != year {
			return fmt.Errorf("休市日 %q 不是 %d 年的日期", day, year)
		}
		if weekday := parsed.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
			return fmt.Errorf("休市日 %s 是周末，周末本来就休市，不用登记", day)
		}
		closedSet[day] = struct{}{}
	}

	var added []string
	for day := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC); day.Year() == year; day = day.AddDate(0, 0, 1) {
		if weekday := day.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
			continue
		}
		text := day.Format(time.DateOnly)
		if _, skip := closedSet[text]; skip {
			continue
		}
		added = append(added, text)
	}
	sort.Strings(added)
	data.TradingDays = append(data.TradingDays, added...)

	dataRaw, err := marshal(data)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(dataRaw)
	meta.Source = fmt.Sprintf("%s；%d 年按%s生成", baseSource(meta.Source), year, sources[year])
	meta.DataVersion = time.Now().Format(time.DateOnly)
	meta.ValidThrough = data.TradingDays[len(data.TradingDays)-1]
	meta.SHA256 = "sha256:" + hex.EncodeToString(sum[:])
	manifestRaw, err := marshal(meta)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dataPath, dataRaw, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, manifestRaw, 0o644); err != nil {
		return err
	}
	fmt.Printf("追加 %d 年 %d 个交易日，日历止于 %s\n", year, len(added), meta.ValidThrough)
	return nil
}

// baseSource 去掉上一次生成时追加的说明，只保留最初的数据来源，免得每年越拼越长。
func baseSource(source string) string {
	if index := strings.IndexRune(source, '；'); index >= 0 {
		return source[:index]
	}
	return source
}

func readJSON(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("解析 %s：%w", path, err)
	}
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
