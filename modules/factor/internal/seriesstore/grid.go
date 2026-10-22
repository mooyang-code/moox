package seriesstore

import (
	"fmt"
	"time"
)

// grid 是一个频率的时间网格：固定步长、锚定 Unix 纪元，和因子时钟 periodclock.Continuous 一致。
// 有交易时段的市场由调用方在写入时用 FillGaps 把非交易时段写成填充行前自行决定是否允许缺期，
// 本期因子时钟只支持连续市场（设计 4.5）。
type grid struct {
	freq string
	step int64 // 秒
}

func gridFor(freq string) (grid, error) {
	var step time.Duration
	switch freq {
	case "1m":
		step = time.Minute
	case "5m":
		step = 5 * time.Minute
	case "15m":
		step = 15 * time.Minute
	case "30m":
		step = 30 * time.Minute
	case "1h":
		step = time.Hour
	case "4h":
		step = 4 * time.Hour
	case "1d":
		step = 24 * time.Hour
	default:
		return grid{}, fmt.Errorf("本地时序存储不支持频率 %q（支持 1m 5m 15m 30m 1h 4h 1d）", freq)
	}
	return grid{freq: freq, step: int64(step / time.Second)}, nil
}

// Step 返回频率对应的周期时长。
func Step(freq string) (time.Duration, error) {
	g, err := gridFor(freq)
	if err != nil {
		return 0, err
	}
	return time.Duration(g.step) * time.Second, nil
}

func (g grid) aligned(sec int64) bool { return sec%g.step == 0 }

// blockStart 返回包含 sec 的块的起点（UTC 秒）：1m 按小时，5m 按 6 小时，15m 与 30m 按天，
// 1h 按周（锚定纪元），4h 按月，1d 按年。
func (g grid) blockStart(sec int64) int64 {
	switch g.freq {
	case "1m":
		return floorDiv(sec, 3600) * 3600
	case "5m":
		return floorDiv(sec, 6*3600) * 6 * 3600
	case "15m", "30m":
		return floorDiv(sec, 86400) * 86400
	case "1h":
		return floorDiv(sec, 7*86400) * 7 * 86400
	case "4h":
		t := time.Unix(sec, 0).UTC()
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC).Unix()
	default: // 1d
		t := time.Unix(sec, 0).UTC()
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
}

// nextBlock 返回 start 之后的下一个块的起点。
func (g grid) nextBlock(start int64) int64 {
	switch g.freq {
	case "1m":
		return start + 3600
	case "5m":
		return start + 6*3600
	case "15m", "30m":
		return start + 86400
	case "1h":
		return start + 7*86400
	case "4h":
		return time.Unix(start, 0).UTC().AddDate(0, 1, 0).Unix()
	default:
		return time.Unix(start, 0).UTC().AddDate(1, 0, 0).Unix()
	}
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
