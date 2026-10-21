package seriesstore

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPerformance 复测设计 3.5 的读写性能（带覆盖元数据）。默认跳过；
// SERIESSTORE_PERF=1 打开，SERIESSTORE_SUBJECTS / SERIESSTORE_BARS 调整规模（默认 200 个标的 × 1 万根 1m）。
func TestPerformance(t *testing.T) {
	if os.Getenv("SERIESSTORE_PERF") == "" {
		t.Skip("设置 SERIESSTORE_PERF=1 运行")
	}
	subjects, bars := envInt("SERIESSTORE_SUBJECTS", 200), envInt("SERIESSTORE_BARS", 10000)
	s, err := Open(Options{Dir: t.TempDir(), BlockCacheBytes: 512 << 20})
	require.NoError(t, err)
	defer s.Close()
	ctx := context.Background()
	names := make([]string, subjects)
	for i := range names {
		names[i] = fmt.Sprintf("SYM%04dUSDT", i)
	}

	start := time.Now()
	for _, name := range names {
		c := NewColumns(testFields, bars)
		for m := 0; m < bars; m++ {
			bar(c, m, "", float64(100+m%50))
		}
		_, err := s.WriteRange(ctx, testSrc, SubjectWrite{Subject: name, From: at(0), To: at(bars), Rows: c}, WriteOptions{})
		require.NoError(t, err)
	}
	took := time.Since(start)
	rows := subjects * bars
	t.Logf("导入 %d 行：%s，%.0f 万行/秒", rows, took.Round(time.Millisecond), float64(rows)/took.Seconds()/10000)

	var total, worst time.Duration
	const periods = 20
	for p := 0; p < periods; p++ {
		writes := make([]SubjectWrite, 0, subjects)
		for _, name := range names {
			c := NewColumns(testFields, 1)
			bar(c, bars+p, "", float64(100+p))
			writes = append(writes, SubjectWrite{Subject: name, From: at(bars + p), To: at(bars + p + 1), Rows: c})
		}
		begin := time.Now()
		_, err := s.WriteBatch(ctx, testSrc, writes, WriteOptions{FillGaps: true, Cursor: []byte(strconv.Itoa(p))})
		require.NoError(t, err)
		d := time.Since(begin)
		total += d
		worst = max(worst, d)
	}
	t.Logf("每期追加 %d 根（一批、同步落盘）：平均 %s，最慢 %s", subjects, (total / periods).Round(time.Millisecond), worst.Round(time.Millisecond))

	begin := time.Now()
	got, err := s.ReadRange(ctx, testSrc, names, at(0), at(bars+periods))
	require.NoError(t, err)
	require.Len(t, got, subjects)
	t.Logf("读 %d × %d 根：%s", subjects, bars, time.Since(begin).Round(time.Millisecond))
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}
