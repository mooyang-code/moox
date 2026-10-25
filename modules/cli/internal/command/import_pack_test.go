package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/seriesformat"
	"github.com/mooyang-code/moox/packages/seriesimport/runner"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

var packNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// writePack 写一个导入包：每个标的 rows 行 5m 数据，最后一根在 now - lastAge。
func writePack(t *testing.T, subjects map[string]int, lastAge time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "5m"), 0o755); err != nil {
		t.Fatal(err)
	}
	pack := importPack{SpaceID: "crypto", DatasetID: "dataset_x", Freq: "5m", SeriesTag: "venue:binance|market:swap|source:swap_http"}
	last := packNow.Add(-lastAge).Truncate(5 * time.Minute)
	for subject, rows := range subjects {
		var body strings.Builder
		body.WriteString("data_time,close,volume\n")
		first := last.Add(-time.Duration(rows-1) * 5 * time.Minute)
		for i := 0; i < rows; i++ {
			fmt.Fprintf(&body, "%s,%d,%d\n", first.Add(time.Duration(i)*5*time.Minute).Format(time.RFC3339), 100+i, i)
		}
		path := filepath.Join(dir, "5m", subject+".csv")
		if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(body.String()))
		pack.Files = append(pack.Files, importPackFile{SubjectID: subject, File: "5m/" + subject + ".csv", Rows: rows,
			FirstUTC: first.Format(time.RFC3339), LastUTC: last.Format(time.RFC3339), SHA256: hex.EncodeToString(sum[:])})
	}
	raw, _ := json.Marshal(pack)
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// preflight 复现 runImportCommand 里的导入包预检：校验扫描 + 清单核对。
func preflight(t *testing.T, dir string, limits importPackLimits) []string {
	t.Helper()
	pack, err := loadImportPack(dir)
	if err != nil {
		t.Fatal(err)
	}
	problems := checkImportPackFiles(pack)
	summary, err := runner.Run(t.Context(), runner.Request{
		Space: pack.SpaceID, Dataset: pack.DatasetID, Freq: pack.Freq, Paths: pack.paths(), Format: "csv", Step: 5 * time.Minute, DryRun: true,
	}, runner.Meta{Fields: []seriesformat.Field{{Name: "close", Type: seriesformat.TypeDouble}, {Name: "volume", Type: seriesformat.TypeDouble}}}, nil, nil)
	if err != nil {
		t.Fatalf("校验扫描：%v", err)
	}
	return append(problems, checkImportPack(pack, summary, limits)...)
}

func thirtyDay() storagepolicy.Period { return storagepolicy.Period{Duration: 35 * 24 * time.Hour} }

func TestImportPackPreflightAcceptsAValidPack(t *testing.T) {
	dir := writePack(t, map[string]int{"BTC-USDT": 80, "ETH-USDT": 60}, 3*24*time.Hour)
	if problems := preflight(t, dir, importPackLimits{Now: packNow, Retention: thirtyDay(), RetentionKnown: true, MinRows: 50}); len(problems) != 0 {
		t.Fatalf("合格的包不应有问题：%v", problems)
	}
}

func TestImportPackPreflightEnforcesTheHardConstraints(t *testing.T) {
	cases := []struct {
		name    string
		age     time.Duration
		rows    int
		limits  importPackLimits
		wantErr string
	}{
		{"最后一根太新", 36 * time.Hour, 60, importPackLimits{MinRows: 50}, "不足"},
		{"缺口超过 30 天", 31 * 24 * time.Hour, 60, importPackLimits{MinRows: 50}, "缺口上限"},
		{"缺口超过保留期减一天", 20 * 24 * time.Hour, 60, importPackLimits{MinRows: 50, Retention: storagepolicy.Period{Duration: 8 * 24 * time.Hour}, RetentionKnown: true}, "缺口上限"},
		{"行数少于回看数量", 3 * 24 * time.Hour, 40, importPackLimits{MinRows: 50}, "回看数量"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writePack(t, map[string]int{"BTC-USDT": tc.rows}, tc.age)
			tc.limits.Now = packNow
			problems := preflight(t, dir, tc.limits)
			if len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), tc.wantErr) {
				t.Fatalf("应报“%s”：%v", tc.wantErr, problems)
			}
		})
	}
}

// 保留期未知（因子机上没有 moox.toml）时只按 30 天上限，不做保留期相关的检查。
func TestImportPackGapLimitFallsBackWithoutRetention(t *testing.T) {
	dir := writePack(t, map[string]int{"BTC-USDT": 60}, 20*24*time.Hour)
	if problems := preflight(t, dir, importPackLimits{Now: packNow}); len(problems) != 0 {
		t.Fatalf("保留期未知时 20 天缺口应放行：%v", problems)
	}
	if got := (importPackLimits{Now: packNow}).tailCutoff(); !got.IsZero() {
		t.Fatalf("保留期未知时没有尾部下限：%v", got)
	}
}

func TestImportPackCatchesManifestMismatches(t *testing.T) {
	dir := writePack(t, map[string]int{"BTC-USDT": 60}, 3*24*time.Hour)
	manifestPath := filepath.Join(dir, "manifest.json")
	raw, _ := os.ReadFile(manifestPath)
	var pack importPack
	if err := json.Unmarshal(raw, &pack); err != nil {
		t.Fatal(err)
	}
	pack.Files[0].Rows = 61
	pack.Files[0].LastUTC = "2026-01-01T00:00:00Z"
	pack.Files[0].SHA256 = strings.Repeat("0", 64)
	raw, _ = json.Marshal(pack)
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(preflight(t, dir, importPackLimits{Now: packNow}), "\n")
	for _, want := range []string{"sha256", "行数 60 与清单 61", "last_utc"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("应报出 %q：\n%s", want, joined)
		}
	}
}

func TestImportPackRejectsBrokenManifestsAndNameMismatch(t *testing.T) {
	if _, err := loadImportPack(t.TempDir()); err == nil {
		t.Fatal("没有 manifest.json 应报错")
	}
	dir := t.TempDir()
	for body, want := range map[string]string{
		`{"space_id":"crypto","dataset_id":"d","freq":"5m","files":[]}`:                 "没有文件",
		`{"space_id":"crypto","dataset_id":"d","freq":"5m","files":[{}],"surprise":1}`:  "格式不对",
		`{"dataset_id":"d","freq":"5m","files":[{"subject_id":"A","file":"5m/A.csv"}]}`: "缺少 space_id",
	} {
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := loadImportPack(dir); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s 应报“%s”：%v", body, want, err)
		}
	}
	packDir := writePack(t, map[string]int{"BTC-USDT": 60}, 3*24*time.Hour)
	pack, _ := loadImportPack(packDir)
	pack.Files[0].SubjectID = "ETH-USDT"
	if problems := checkImportPackFiles(pack); len(problems) == 0 || !strings.Contains(problems[0], "不一致") {
		t.Fatalf("清单标的与文件名不一致应报错：%v", problems)
	}
}

func TestApplyImportPackFillsOptionsAndRejectsConflicts(t *testing.T) {
	dir := writePack(t, map[string]int{"BTC-USDT": 60}, 3*24*time.Hour)
	opts := importOptions{Pack: dir}
	if _, err := applyImportPack(&opts); err != nil {
		t.Fatal(err)
	}
	if opts.Space != "crypto" || opts.Dataset != "dataset_x" || opts.Freq != "5m" || opts.SeriesTag == "" || len(opts.Paths) != 1 {
		t.Fatalf("没有从清单补全：%+v", opts)
	}
	for _, conflict := range []importOptions{{Pack: dir, Freq: "1h"}, {Pack: dir, Dataset: "other"}, {Pack: dir, Paths: []string{"x.csv"}}, {Pack: dir, Subject: "A"}} {
		if _, err := applyImportPack(&conflict); err == nil {
			t.Fatalf("%+v 应报冲突", conflict)
		}
	}
}

// 尾部窗口下限 = 现在 − 保留期 + 1 天。
func TestImportPackTailCutoffUsesRetentionWithMargin(t *testing.T) {
	limits := importPackLimits{Now: packNow, Retention: thirtyDay(), RetentionKnown: true}
	want := packNow.Add(-35*24*time.Hour + 24*time.Hour)
	if got := limits.tailCutoff(); !got.Equal(want) {
		t.Fatalf("尾部下限 %v，期望 %v", got, want)
	}
	forever := importPackLimits{Now: packNow, Retention: storagepolicy.Period{Forever: true}, RetentionKnown: true}
	if !forever.tailCutoff().IsZero() || forever.maxGap() != importPackMaxGap {
		t.Fatal("永久保留没有尾部下限，缺口上限仍是 30 天")
	}
}
