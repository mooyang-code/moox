package planner

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// intervalRegex interval解析正则表达式
var intervalRegex = regexp.MustCompile(`^(\d+)([smhd])$`)

// ParseIntervalSeconds 解析interval字符串为秒数
// 支持格式：1s, 30s, 1m, 5m, 15m, 1h, 4h, 1d
func ParseIntervalSeconds(interval string) (int64, error) {
	if interval == "" || interval == "default" {
		return 60, nil // 默认1分钟
	}

	matches := intervalRegex.FindStringSubmatch(interval)
	if len(matches) != 3 {
		return 60, fmt.Errorf("invalid interval format: %s, using default 60s", interval)
	}

	// 提取数字和单位
	num, err := strconv.ParseInt(matches[1], 10, 64)
	if err != nil {
		return 60, fmt.Errorf("invalid interval number: %s, using default 60s", matches[1])
	}
	unit := matches[2]

	// 转换为秒
	var seconds int64
	switch unit {
	case "s": // 秒
		seconds = num
	case "m": // 分钟
		seconds = num * 60
	case "h": // 小时
		seconds = num * 3600
	case "d": // 天
		seconds = num * 86400
	default:
		return 60, fmt.Errorf("unsupported interval unit: %s, using default 60s", unit)
	}

	return seconds, nil
}

// CalculateExpectedExecTime 计算预估执行时间（下一个周期）
// 根据当前时间和interval，向下取整到时间边界，然后加上一个interval得到下次执行时间
// 例如：当前时间 14:32:47, interval=5m(300秒)
//      向下取整：14:30:00
//      下次执行：14:35:00
// 注意：此函数返回的是**下一个周期**的执行时间，用于判断任务"何时应该执行"
func CalculateExpectedExecTime(now time.Time, intervalSeconds int64) time.Time {
	if intervalSeconds <= 0 {
		return now
	}

	// 向下取整到interval边界
	timestamp := now.Unix()
	aligned := (timestamp / intervalSeconds) * intervalSeconds
	boundary := time.Unix(aligned, 0)

	// 下一个执行窗口
	nextExec := boundary.Add(time.Duration(intervalSeconds) * time.Second)

	return nextExec
}

// CalculateCurrentCycleBoundary 计算当前周期边界时间
// 根据当前时间和interval，向下取整到时间边界
// 例如：当前时间 14:32:47, interval=5m(300秒)
//      当前周期边界：14:30:00
// 用途：判断 Pending 任务是否超时（now > boundary + tolerance 则超时）
func CalculateCurrentCycleBoundary(now time.Time, intervalSeconds int64) time.Time {
	if intervalSeconds <= 0 {
		return now
	}

	// 向下取整到interval边界
	timestamp := now.Unix()
	aligned := (timestamp / intervalSeconds) * intervalSeconds
	boundary := time.Unix(aligned, 0)

	return boundary
}
