// Command factorexpr 是因子表达式的离线工具：列出函数、编译检查定义、在 K 线 CSV 上试算。
// 它与引擎使用同一个编译器和执行器，用于把旧的 Python 因子转写成表达式之后逐值核对（见 skills/moox/references/factor-dsl.md）。
//
//	factorexpr functions [--json]
//	factorexpr check --library lib.yaml [--factor-id ID ...]
//	factorexpr eval --library lib.yaml --factor-id ID [--factor-id ID ...] --input bars.csv --freq 1m [--output out.csv]
package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
	"gopkg.in/yaml.v3"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("用法：factorexpr functions|check|eval [参数]")
	}
	switch args[0] {
	case "functions":
		return runFunctions(args[1:], out)
	case "check":
		return runCheck(args[1:], out)
	case "eval":
		return runEval(args[1:], out)
	default:
		return fmt.Errorf("未知子命令 %q（functions、check、eval）", args[0])
	}
}

type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			*l = append(*l, item)
		}
	}
	return nil
}

// ---------------------------------------------------------------- functions

type argInfo struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Optional bool     `json:"optional,omitempty"`
	Default  *float64 `json:"default,omitempty"`
}

type functionInfo struct {
	Name     string    `json:"name"`
	Category string    `json:"category"`
	Args     []argInfo `json:"args"`
	Result   string    `json:"result"`
	Lookback string    `json:"lookback"`
	Doc      string    `json:"doc"`
	Example  string    `json:"example"`
}

var argKinds = map[factorexpr.ArgKind]string{
	factorexpr.ArgNumber:     "number",
	factorexpr.ArgSeries:     "series",
	factorexpr.ArgBool:       "bool",
	factorexpr.ArgBoolSeries: "bool_series",
	factorexpr.ArgWindow:     "window",
	factorexpr.ArgInteger:    "integer",
	factorexpr.ArgConstant:   "constant",
}

func describeFunctions() []functionInfo {
	var infos []functionInfo
	for _, fn := range factorexpr.Functions() {
		info := functionInfo{Name: fn.Name, Category: fn.Category.String(), Result: "number", Lookback: fn.Lookback, Doc: fn.Doc, Example: fn.Example}
		if fn.Result == factorexpr.ResultBool {
			info.Result = "bool"
		}
		for _, arg := range fn.Args {
			a := argInfo{Name: arg.Name, Kind: argKinds[arg.Kind], Optional: arg.Optional}
			if arg.Kind == factorexpr.ArgWindow || arg.Kind == factorexpr.ArgInteger || arg.Kind == factorexpr.ArgConstant {
				lo, hi := arg.Min, arg.Max
				a.Min, a.Max = &lo, &hi
			}
			if arg.Optional {
				d := arg.Default
				a.Default = &d
			}
			info.Args = append(info.Args, a)
		}
		infos = append(infos, info)
	}
	return infos
}

func runFunctions(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("functions", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	infos := describeFunctions()
	if *asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(infos)
	}
	for _, info := range infos {
		var parts []string
		for _, a := range info.Args {
			part := a.Name + ":" + a.Kind
			if a.Min != nil {
				part += fmt.Sprintf("[%g,%g]", *a.Min, *a.Max)
			}
			if a.Optional {
				part += fmt.Sprintf("=%g", *a.Default)
			}
			parts = append(parts, part)
		}
		fmt.Fprintf(out, "%-16s %-13s (%s) 回看 %s；%s；例：%s\n", info.Name, info.Category, strings.Join(parts, ", "), info.Lookback, info.Doc, info.Example)
	}
	return nil
}

// ---------------------------------------------------------------- 因子库

// libraryEntry 与 modules/factor/factors/*.yaml 的格式相同：[{factor_id, name, spec, allow_partial_universe}]。
type libraryEntry struct {
	FactorID             string `yaml:"factor_id"`
	Name                 string `yaml:"name"`
	Spec                 string `yaml:"spec"`
	AllowPartialUniverse bool   `yaml:"allow_partial_universe"`
}

func readLibrary(path string) ([]libraryEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var entries []libraryEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("解析因子库 %s：%w", path, err)
	}
	seen := map[string]bool{}
	for i, entry := range entries {
		if strings.TrimSpace(entry.FactorID) == "" || strings.TrimSpace(entry.Name) == "" || strings.TrimSpace(entry.Spec) == "" {
			return nil, fmt.Errorf("因子库 %s 第 %d 项缺少 factor_id、name 或 spec", path, i+1)
		}
		if seen[entry.FactorID] {
			return nil, fmt.Errorf("因子库 %s 重复定义 %s", path, entry.FactorID)
		}
		seen[entry.FactorID] = true
	}
	return entries, nil
}

func selectEntries(entries []libraryEntry, ids []string) ([]libraryEntry, error) {
	if len(ids) == 0 {
		return entries, nil
	}
	byID := map[string]libraryEntry{}
	for _, entry := range entries {
		byID[entry.FactorID] = entry
	}
	var selected []libraryEntry
	for _, id := range ids {
		entry, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("因子库里没有 %s", id)
		}
		selected = append(selected, entry)
	}
	return selected, nil
}

// ---------------------------------------------------------------- check

type checkError struct {
	Path    string `json:"path,omitempty"`
	DocLine int    `json:"doc_line,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

type checkResult struct {
	FactorID     string   `json:"factor_id"`
	OK           bool     `json:"ok"`
	FactorType   string   `json:"factor_type,omitempty"`
	InputColumns []string `json:"input_columns,omitempty"`
	Outputs      []string `json:"outputs,omitempty"`
	Lookback     int      `json:"lookback,omitempty"`
	// OutputLookbacks 是每个输出列自己的回看；Lookback 是其中的最大值，也是引擎做空洞检查用的窗口。
	OutputLookbacks map[string]int `json:"output_lookbacks,omitempty"`
	Canonical       string         `json:"canonical,omitempty"`
	Error           *checkError    `json:"error,omitempty"`
}

func runCheck(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	library := fs.String("library", "", "YAML 因子库文件（[{factor_id, name, spec}]）")
	var ids listFlag
	fs.Var(&ids, "factor-id", "只检查这些因子（可重复或逗号分隔）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *library == "" {
		return errors.New("--library 必填")
	}
	entries, err := readLibrary(*library)
	if err != nil {
		return err
	}
	if entries, err = selectEntries(entries, ids); err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	failed := 0
	for _, entry := range entries {
		result := checkResult{FactorID: entry.FactorID}
		factor, err := factorexpr.Compile(entry.Spec)
		if err != nil {
			failed++
			result.Error = &checkError{Message: err.Error()}
			var fe *factorexpr.Error
			if errors.As(err, &fe) {
				result.Error = &checkError{Path: fe.Path, DocLine: fe.DocLine, Line: fe.Line, Column: fe.Column, Message: fe.Error()}
			}
		} else {
			result.OK = true
			result.FactorType = string(factor.Type)
			result.InputColumns = factor.InputColumns
			result.Outputs = factor.OutputNames()
			result.Lookback = factor.Lookback
			result.OutputLookbacks = map[string]int{}
			for _, output := range factor.Outputs {
				result.OutputLookbacks[output.Name] = max(output.Root.Lookback, 1)
			}
			result.Canonical = factor.CanonicalText()
		}
		if err := encoder.Encode(result); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d 个因子编译失败", failed)
	}
	return nil
}

// ---------------------------------------------------------------- eval

type csvRow struct {
	time   time.Time
	key    exec.SeriesKey
	values map[string]float64
}

// readBars 读取 K 线 CSV：data_time 必填（RFC3339），subject_id、series_tag 可选，其余列按数值读取，空值、null、NaN 记为 NaN。
func readBars(path string) ([]csvRow, []string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	header, err := reader.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("读取 %s 表头：%w", path, err)
	}
	index := map[string]int{}
	var valueColumns []string
	for i, name := range header {
		name = strings.TrimSpace(name)
		index[name] = i
		if name != "data_time" && name != "subject_id" && name != "series_tag" {
			valueColumns = append(valueColumns, name)
		}
	}
	if _, ok := index["data_time"]; !ok {
		return nil, nil, fmt.Errorf("%s 缺少 data_time 列", path)
	}
	var rows []csvRow
	seen := map[string]bool{}
	for line := 2; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%s 第 %d 行：%w", path, line, err)
		}
		at, err := parseTime(record[index["data_time"]])
		if err != nil {
			return nil, nil, fmt.Errorf("%s 第 %d 行 data_time：%w", path, line, err)
		}
		row := csvRow{time: at, key: exec.SeriesKey{Subject: "S"}, values: map[string]float64{}}
		if i, ok := index["subject_id"]; ok {
			row.key.Subject = record[i]
		}
		if i, ok := index["series_tag"]; ok {
			row.key.Tag = record[i]
		}
		identity := fmt.Sprintf("%s\x00%s\x00%d", row.key.Subject, row.key.Tag, at.UnixNano())
		if seen[identity] {
			return nil, nil, fmt.Errorf("%s 第 %d 行与前面的行身份重复（subject_id、series_tag、data_time）", path, line)
		}
		seen[identity] = true
		for _, name := range valueColumns {
			raw := strings.TrimSpace(record[index[name]])
			if raw == "" || strings.EqualFold(raw, "null") || strings.EqualFold(raw, "nan") {
				row.values[name] = math.NaN()
				continue
			}
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("%s 第 %d 行列 %s 不是数值：%q", path, line, name, raw)
			}
			row.values[name] = v
		}
		rows = append(rows, row)
	}
	return rows, valueColumns, nil
}

func parseTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006-01-02"} {
		if at, err := time.Parse(layout, raw); err == nil {
			return at.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q（用 RFC3339）", raw)
}

// frequencies 与因子模块（modules/factor/internal/domain.ParseFrequency）支持的频率一致。
var frequencies = map[string]time.Duration{
	"1m": time.Minute, "5m": 5 * time.Minute, "15m": 15 * time.Minute, "30m": 30 * time.Minute,
	"1h": time.Hour, "4h": 4 * time.Hour, "1d": 24 * time.Hour,
}

// grid 返回从最早到最晚按 freq 等距的时间网格（与引擎一致）：缺失的 K 线在网格上读作 NaN。
func grid(rows []csvRow, freq time.Duration) ([]time.Time, error) {
	first, last := rows[0].time, rows[0].time
	for _, row := range rows {
		if row.time.Before(first) {
			first = row.time
		}
		if row.time.After(last) {
			last = row.time
		}
	}
	if count := last.Sub(first)/freq + 1; count > 10_000_000 {
		return nil, errors.New("网格超过一千万个时刻，请缩小数据范围")
	}
	for _, row := range rows {
		if row.time.Sub(first)%freq != 0 {
			return nil, fmt.Errorf("时刻 %s 不在以 %s 为起点、步长 %s 的网格上", row.time.Format(time.RFC3339), first.Format(time.RFC3339), freq)
		}
	}
	var times []time.Time
	for at := first; !at.After(last); at = at.Add(freq) {
		times = append(times, at)
	}
	return times, nil
}

// gapMask 复现流水线的空洞检查（modules/factor/internal/pipeline/gapcheck.go）：因子在某标的某时刻的回看窗口
// （按因子的回看，从该标的第一根 K 线起算，之前的头部不算空洞）里，该标的任一序列缺了 K 线，这个因子在该标的该时刻的全部输出都屏蔽。
// 返回 masked[因子][标的] 为需要屏蔽的网格下标集合。
func gapMask(panel *exec.Panel, lookbacks map[string]int, pad, length int) map[string]map[string]map[int]bool {
	series := panel.Series()
	firstOf := map[string]int{}
	prefix := make([][]int, len(series))
	for s, key := range series {
		prefix[s] = make([]int, length+1)
		for t := 0; t < length; t++ {
			present := 0
			if panel.Present(s, t+pad) {
				present = 1
				if first, ok := firstOf[key.Subject]; !ok || t < first {
					firstOf[key.Subject] = t
				}
			}
			prefix[s][t+1] = prefix[s][t] + present
		}
	}
	masked := map[string]map[string]map[int]bool{}
	for factorID, lookback := range lookbacks {
		masked[factorID] = map[string]map[int]bool{}
		for s, key := range series {
			first, ok := firstOf[key.Subject]
			if !ok {
				continue
			}
			for t := first; t < length; t++ {
				lo := max(t-lookback+1, first)
				if prefix[s][t+1]-prefix[s][lo] != t-lo+1 {
					if masked[factorID][key.Subject] == nil {
						masked[factorID][key.Subject] = map[int]bool{}
					}
					masked[factorID][key.Subject][t] = true
				}
			}
		}
	}
	return masked
}

func runEval(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("eval", flag.ContinueOnError)
	library := fs.String("library", "", "YAML 因子库文件")
	input := fs.String("input", "", "K 线 CSV（data_time、可选 subject_id 与 series_tag、各输入列）")
	output := fs.String("output", "", "输出 CSV（默认标准输出）")
	freqText := fs.String("freq", "", "源数据频率（1m、5m、15m、30m、1h、4h、1d，必填）")
	workers := fs.Int("workers", 4, "并行度")
	var ids listFlag
	fs.Var(&ids, "factor-id", "要试算的因子（可重复或逗号分隔，必填）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *library == "" || *input == "" || len(ids) == 0 || *freqText == "" {
		return errors.New("--library、--input、--factor-id、--freq 必填")
	}
	freq, ok := frequencies[*freqText]
	if !ok {
		return fmt.Errorf("--freq 只支持 1m、5m、15m、30m、1h、4h、1d，得到 %q", *freqText)
	}
	entries, err := readLibrary(*library)
	if err != nil {
		return err
	}
	if entries, err = selectEntries(entries, ids); err != nil {
		return err
	}
	var inputs []factorexpr.ProgramInput
	lookbacks := map[string]int{}
	for _, entry := range entries {
		factor, err := factorexpr.Compile(entry.Spec)
		if err != nil {
			return fmt.Errorf("编译 %s：%w", entry.FactorID, err)
		}
		inputs = append(inputs, factorexpr.ProgramInput{FactorID: entry.FactorID, Factor: factor})
		lookbacks[entry.FactorID] = factor.Lookback
	}
	program, err := factorexpr.NewProgram(inputs)
	if err != nil {
		return err
	}
	rows, columns, err := readBars(*input)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return errors.New("输入没有数据行")
	}
	for _, need := range program.Columns {
		found := false
		for _, column := range columns {
			found = found || column == need
		}
		if !found {
			return fmt.Errorf("输入 CSV 缺少因子需要的列 %s", need)
		}
	}
	times, err := grid(rows, freq)
	if err != nil {
		return err
	}
	position := make(map[int64]int, len(times))
	for i, at := range times {
		position[at.UnixNano()] = i
	}
	keySet := map[exec.SeriesKey]bool{}
	var keys []exec.SeriesKey
	for _, row := range rows {
		if !keySet[row.key] {
			keySet[row.key] = true
			keys = append(keys, row.key)
		}
	}
	// 网格前面补 lookback−1 个空位：每个有源行的时刻都能作为目标，窗口不满的结果自然是 NaN（与引擎的严格窗口一致）。
	pad := program.Lookback - 1
	panel, err := exec.NewPanel(len(times)+pad, program.Columns, keys)
	if err != nil {
		return err
	}
	for _, row := range rows {
		s, _ := panel.SeriesIndex(row.key)
		t := position[row.time.UnixNano()] + pad
		for c, name := range program.Columns {
			panel.Set(c, s, t, row.values[name])
		}
		panel.MarkPresent(s, t)
	}
	result, err := exec.Run(context.Background(), program, panel, exec.Options{Start: pad, End: pad + len(times) - 1, Workers: *workers})
	if err != nil {
		return err
	}
	masked := gapMask(panel, lookbacks, pad, len(times))
	writer := out
	if *output != "" {
		file, err := os.Create(*output)
		if err != nil {
			return err
		}
		defer file.Close()
		writer = file
	}
	w := csv.NewWriter(writer)
	header := []string{"data_time", "subject_id", "series_tag"}
	for _, column := range result.Columns {
		header = append(header, column.Name)
	}
	if err := w.Write(header); err != nil {
		return err
	}
	for s, key := range result.Series {
		for t := result.Start; t <= result.End; t++ {
			if !result.Present(s, t) {
				continue
			}
			record := []string{times[t-pad].Format(time.RFC3339), key.Subject, key.Tag}
			for c := range result.Columns {
				v := result.Value(c, s, t)
				if masked[result.Columns[c].FactorID][key.Subject][t-pad] {
					v = math.NaN()
				}
				if math.IsNaN(v) || math.IsInf(v, 0) {
					record = append(record, "")
				} else {
					record = append(record, strconv.FormatFloat(v, 'g', -1, 64))
				}
			}
			if err := w.Write(record); err != nil {
				return err
			}
		}
	}
	w.Flush()
	return w.Error()
}
