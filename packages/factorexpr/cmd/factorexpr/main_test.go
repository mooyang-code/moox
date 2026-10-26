package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLibrary = `- factor_id: ma
  name: 均值
  spec: |
    params: {window: {type: int, min: 1, max: 3, default: 2}}
    outputs:
      ma_2: ts_mean(close, 2)
      ma_3: ts_mean(close, 3)
      ma_w: ts_mean(close, window)
- factor_id: rank
  name: 截面排名
  spec: |
    output: cs_rank(close)
- factor_id: bad
  name: 写错的
  spec: |
    output: ts_mean(close)
`

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckReportsDerivedFieldsAndErrors(t *testing.T) {
	library := writeFile(t, "lib.yaml", testLibrary)
	var out bytes.Buffer
	err := run([]string{"check", "--library", library}, &out)
	if err == nil || !strings.Contains(err.Error(), "1 个因子编译失败") {
		t.Fatalf("err = %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	var ma, bad checkResult
	if err := json.Unmarshal([]byte(lines[0]), &ma); err != nil {
		t.Fatal(err)
	}
	if ma.OutputLookbacks["ma_2"] != 2 || ma.OutputLookbacks["ma_3"] != 3 || ma.OutputLookbacks["ma_w"] != 3 {
		t.Fatalf("output lookbacks = %+v", ma.OutputLookbacks)
	}
	if len(ma.Params) != 1 || ma.Params[0].Name != "window" || ma.Params[0].Default != 2 || ma.Params[0].Max != 3 {
		t.Fatalf("params = %+v", ma.Params)
	}
	if !ma.OK || ma.FactorType != "timeseries" || ma.Lookback != 3 || strings.Join(ma.Outputs, ",") != "ma_2,ma_3,ma_w" || strings.Join(ma.InputColumns, ",") != "close" {
		t.Fatalf("ma = %+v", ma)
	}
	if err := json.Unmarshal([]byte(lines[2]), &bad); err != nil {
		t.Fatal(err)
	}
	if bad.OK || bad.Error == nil || bad.Error.Path == "" {
		t.Fatalf("bad = %+v", bad)
	}
	out.Reset()
	if err := run([]string{"check", "--library", library, "--factor-id", "rank"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"factor_type":"cross_section"`) {
		t.Fatalf("rank = %s", out.String())
	}
}

func TestEvalTimeSeriesStrictWindowAndGrid(t *testing.T) {
	library := writeFile(t, "lib.yaml", testLibrary)
	// 00:03 缺一根：按 1m 网格补成 NaN；与流水线一样，因子回看窗口（3 根）覆盖空洞的时刻全部输出都屏蔽，
	// 包括窗口为 2 的 ma_2；结果只在有源行的时刻输出。
	bars := writeFile(t, "bars.csv", "data_time,close\n"+
		"2026-10-01T00:00:00Z,1\n2026-10-01T00:01:00Z,2\n2026-10-01T00:02:00Z,4\n2026-10-01T00:04:00Z,8\n2026-10-01T00:05:00Z,10\n")
	var out bytes.Buffer
	if err := run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars, "--freq", "1m"}, &out); err != nil {
		t.Fatal(err)
	}
	want := "data_time,subject_id,series_tag,ma_2,ma_3,ma_w\n" +
		"2026-10-01T00:00:00Z,S,,,,\n" +
		"2026-10-01T00:01:00Z,S,,1.5,,1.5\n" +
		"2026-10-01T00:02:00Z,S,,3,2.3333333333333335,3\n" +
		"2026-10-01T00:04:00Z,S,,,,\n" +
		"2026-10-01T00:05:00Z,S,,,,\n"
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
	// --param 改变参数取值：ma_w 按 window = 3 计算，与 ma_3 相同。
	out.Reset()
	if err := run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars, "--freq", "1m", "--param", "ma.window=3"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "2026-10-01T00:02:00Z,S,,3,2.3333333333333335,2.3333333333333335\n") {
		t.Fatalf("param eval = %s", out.String())
	}
	if err := run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars, "--freq", "1m", "--param", "ma.window=9"}, &out); err == nil ||
		!strings.Contains(err.Error(), "超出范围") {
		t.Fatalf("err = %v", err)
	}
}

func TestEvalCrossSectionGroupsByTimeAndTag(t *testing.T) {
	library := writeFile(t, "lib.yaml", testLibrary)
	bars := writeFile(t, "bars.csv", "data_time,subject_id,series_tag,close\n"+
		"2026-10-01T00:00:00Z,A,x,1\n2026-10-01T00:00:00Z,B,x,3\n2026-10-01T00:00:00Z,C,x,2\n"+
		"2026-10-01T00:00:00Z,A,y,5\n2026-10-01T00:00:00Z,B,y,\n")
	var out bytes.Buffer
	if err := run([]string{"eval", "--library", library, "--factor-id", "rank", "--input", bars, "--freq", "1m"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"2026-10-01T00:00:00Z,A,x,0.3333333333333333",
		"2026-10-01T00:00:00Z,B,x,1",
		"2026-10-01T00:00:00Z,C,x,0.6666666666666666",
		"2026-10-01T00:00:00Z,A,y,1",
		"2026-10-01T00:00:00Z,B,y,",
	} {
		if !strings.Contains(out.String(), line+"\n") {
			t.Fatalf("missing %q in\n%s", line, out.String())
		}
	}
}

func TestEvalRejectsMissingColumnAndUnknownFactor(t *testing.T) {
	library := writeFile(t, "lib.yaml", testLibrary)
	bars := writeFile(t, "bars.csv", "data_time,open\n2026-10-01T00:00:00Z,1\n")
	err := run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars, "--freq", "1m"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "缺少因子需要的列 close") {
		t.Fatalf("err = %v", err)
	}
	err = run([]string{"eval", "--library", library, "--factor-id", "Nope", "--input", bars, "--freq", "1m"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "没有 Nope") {
		t.Fatalf("err = %v", err)
	}
	err = run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars, "--freq", "2m"}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--freq 只支持") {
		t.Fatalf("err = %v", err)
	}
	err = run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--freq") {
		t.Fatalf("err = %v", err)
	}
}

// 新上市（第一根 K 线之前的头部）不算空洞：晚两根开始的标的只受严格窗口影响，不被屏蔽；日线频率可用。
func TestEvalListingHeadIsNotAGap(t *testing.T) {
	library := writeFile(t, "lib.yaml", testLibrary)
	bars := writeFile(t, "bars.csv", "data_time,subject_id,close\n"+
		"2026-10-01T00:00:00Z,A,1\n2026-10-02T00:00:00Z,A,2\n2026-10-03T00:00:00Z,A,3\n2026-10-04T00:00:00Z,A,4\n"+
		"2026-10-03T00:00:00Z,B,10\n2026-10-04T00:00:00Z,B,20\n")
	var out bytes.Buffer
	if err := run([]string{"eval", "--library", library, "--factor-id", "ma", "--input", bars, "--freq", "1d"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{
		"2026-10-03T00:00:00Z,B,,,,\n",
		"2026-10-04T00:00:00Z,B,,15,,15\n",
		"2026-10-04T00:00:00Z,A,,3.5,3,3.5\n",
	} {
		if !strings.Contains(out.String(), line) {
			t.Fatalf("missing %q in\n%s", line, out.String())
		}
	}
}

func TestFunctionsListsRegistry(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"functions", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var infos []functionInfo
	if err := json.Unmarshal(out.Bytes(), &infos); err != nil {
		t.Fatal(err)
	}
	names := map[string]functionInfo{}
	for _, info := range infos {
		names[info.Name] = info
	}
	if names["ts_mean"].Category != "timeseries" || names["cs_rank"].Category != "cross_section" || names["where"].Category != "elementwise" {
		t.Fatalf("infos = %+v", infos)
	}
	if arg := names["ts_mean"].Args[1]; arg.Kind != "window" || arg.Min == nil || *arg.Min != 1 {
		t.Fatalf("ts_mean args = %+v", names["ts_mean"].Args)
	}
}
