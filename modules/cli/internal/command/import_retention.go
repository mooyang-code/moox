package command

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// 导入的历史数据受 Storage 保留策略约束：早于「现在 − 保留期」的行写进去也会在下一次清理（每小时一次）时被删掉，
// 导入命令却会照常报告成功，用户以为历史已经在库里。导入前按 moox.toml 的 [storage_retention] 核对，早于保留期的行给出警告。

// loadImportRetention 读取 moox.toml 解析目标数据集（空间 × 频率）的保留期；读不到配置时 ok 为 false（不影响导入）。
var loadImportRetention = func(manifestPath, space, freq string) (storagepolicy.Period, bool) {
	absolute, err := filepath.Abs(defaultFlag(manifestPath, "./moox.toml"))
	if err != nil {
		return storagepolicy.Period{}, false
	}
	snapshot, err := setupconfig.Load(absolute, filepath.Dir(absolute))
	if err != nil {
		return storagepolicy.Period{}, false
	}
	defer clearSetupSecrets(snapshot)
	period, _, err := snapshot.Manifest.StoragePolicy().Retention.Resolve(space, freq)
	if err != nil {
		return storagepolicy.Period{}, false
	}
	return period, true
}

// warnImportRetention 对每个 CSV 文件统计早于保留期截止时间的行数并警告。
// 导入文件按时间升序，所以只读到第一行不早于截止时间的为止，大文件也很快；时间列解析不了就跳过该文件。
func warnImportRetention(out io.Writer, paths []string, timeColumn, space, freq string, retention storagepolicy.Period, now time.Time) {
	if retention.Forever || retention.Duration <= 0 {
		return
	}
	cutoff := now.UTC().Add(-retention.Duration)
	files, err := importCSVFiles(paths)
	if err != nil {
		return
	}
	var staleFiles, staleRows int
	for _, file := range files {
		stale, first, ok := staleRowsBefore(file, timeColumn, cutoff)
		if !ok || stale == 0 {
			continue
		}
		staleFiles++
		staleRows += stale
		if staleFiles <= 5 {
			fmt.Fprintf(out, "警告：%s 有 %d 行早于保留期（最早 %s）\n", filepath.Base(file), stale, first.Format(time.RFC3339))
		}
	}
	if staleRows == 0 {
		return
	}
	if staleFiles > 5 {
		fmt.Fprintf(out, "警告：……另有 %d 个文件也有早于保留期的行\n", staleFiles-5)
	}
	fmt.Fprintf(out,
		"警告：数据集 %s/%s 按 moox.toml 的 [storage_retention] 保留期是 %s（线上实际生效的是 Storage 单元里的 storage-policy.json，两者应一致），早于 %s 的行共 %d 行（%d 个文件）写入后会在下一次清理（每小时一次）时被删除。"+
			"需要保留更久的历史，先在 moox.toml 的 [storage_retention] 调大 spaces.%s.%s（或 defaults.%s）并随 Storage 单元安装生效，再导入\n",
		space, freq, retention.String(), cutoff.Format(time.RFC3339), staleRows, staleFiles, space, freq, freq)
}

// importCSVFiles 展开文件与目录（目录下的 .csv 文件）。
func importCSVFiles(paths []string) ([]string, error) {
	var files []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			files = append(files, path)
			continue
		}
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.EqualFold(filepath.Ext(p), ".csv") {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return files, nil
}

// staleRowsBefore 返回文件里早于 cutoff 的行数和最早一行的时间。
func staleRowsBefore(path, timeColumn string, cutoff time.Time) (stale int, first time.Time, ok bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, time.Time{}, false
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return 0, time.Time{}, false
	}
	column := -1
	for i, name := range header {
		if strings.TrimSpace(strings.TrimPrefix(name, "\xef\xbb\xbf")) == timeColumn {
			column = i
		}
	}
	if column < 0 {
		return 0, time.Time{}, false
	}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return stale, first, true
		}
		if err != nil || column >= len(record) {
			return stale, first, stale > 0
		}
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(record[column]))
		if err != nil {
			return stale, first, stale > 0
		}
		if !at.Before(cutoff) {
			return stale, first, true
		}
		if stale == 0 {
			first = at.UTC()
		}
		stale++
	}
}
