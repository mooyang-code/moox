package command

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepolicy"
	"github.com/stretchr/testify/require"
)

func writeCSV(t *testing.T, dir, name string, times ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("data_time,open,close\n")
	for _, at := range times {
		b.WriteString(at + ",1,2\n")
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(b.String()), 0o600))
	return path
}

// 早于「现在 − 保留期」的行给出警告：文件名、行数、最早时间、保留期、怎么改都要在里面；没有过期行、永久保留、读不出时间列都不警告。
func TestWarnImportRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	retention, err := storagepolicy.ParsePeriod("420d") // 截止 2025-08-29
	require.NoError(t, err)

	stale := writeCSV(t, dir, "BTC-USDT.csv", "2019-09-08T17:00:00Z", "2024-01-01T00:00:00Z", "2025-08-28T23:00:00Z", "2025-09-01T00:00:00Z", "2026-10-22T23:00:00Z")
	fresh := writeCSV(t, dir, "ETH-USDT.csv", "2025-10-01T00:00:00Z", "2026-10-22T23:00:00Z")

	var out bytes.Buffer
	warnImportRetention(&out, []string{stale, fresh}, "data_time", "crypto", "1h", retention, now)
	text := out.String()
	require.Contains(t, text, "BTC-USDT.csv 有 3 行早于保留期（最早 2019-09-08T17:00:00Z）")
	require.NotContains(t, text, "ETH-USDT.csv")
	require.Contains(t, text, "crypto/1h 按 moox.toml 的 [storage_retention] 保留期是 10080h")
	require.Contains(t, text, "早于 2025-08-29T00:00:00Z 的行共 3 行（1 个文件）")
	require.Contains(t, text, "spaces.crypto.1h")

	// 传目录：展开其中的 .csv。
	out.Reset()
	warnImportRetention(&out, []string{dir}, "data_time", "crypto", "1h", retention, now)
	require.Contains(t, out.String(), "BTC-USDT.csv")

	// 没有过期行、永久保留、时间列不存在：都不警告。
	out.Reset()
	warnImportRetention(&out, []string{fresh}, "data_time", "crypto", "1h", retention, now)
	warnImportRetention(&out, []string{stale}, "data_time", "crypto", "1h", storagepolicy.Period{Forever: true}, now)
	warnImportRetention(&out, []string{stale}, "candle_begin_time", "crypto", "1h", retention, now)
	require.Empty(t, out.String())
}

// 过期文件很多时只列前 5 个，其余合并成一行。
func TestWarnImportRetentionCapsPerFileLines(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		paths = append(paths, writeCSV(t, dir, name+".csv", "2020-01-01T00:00:00Z"))
	}
	retention, _ := storagepolicy.ParsePeriod("10d")
	var out bytes.Buffer
	warnImportRetention(&out, paths, "data_time", "crypto", "1h", retention, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	require.Equal(t, 5, strings.Count(out.String(), "行早于保留期（最早"))
	require.Contains(t, out.String(), "另有 2 个文件也有早于保留期的行")
	require.Contains(t, out.String(), "共 7 行（7 个文件）")
}
