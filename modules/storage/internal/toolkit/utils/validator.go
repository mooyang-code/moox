package utils

import (
	"fmt"
	"strconv"
	"time"
)

// CheckTimeSeriesOrder 检查时序数据的时间顺序是否正确
// prevTime 和 currentTime 的格式为 YYYY-MM-DD HH:MM:SS
func CheckTimeSeriesOrder(freq string, prevTime, currentTime string) (bool, error) {
	// 解析时间字符串
	prev, err := time.Parse("2006-01-02 15:04:05", prevTime)
	if err != nil {
		return false, fmt.Errorf("invalid prevTime format: %v", err)
	}
	curr, err := time.Parse("2006-01-02 15:04:05", currentTime)
	if err != nil {
		return false, fmt.Errorf("invalid currentTime format: %v", err)
	}

	// 检查时间顺序
	if !curr.After(prev) {
		return false, fmt.Errorf("invalid time series order: current time %s is not after previous time %s",
			currentTime, prevTime)
	}

	// 解析频率
	var interval int
	var unit string
	if len(freq) > 1 {
		// 尝试解析数字前缀
		for i := 0; i < len(freq); i++ {
			if freq[i] >= '0' && freq[i] <= '9' {
				continue
			}
			interval = 1
			if i > 0 {
				interval, err = strconv.Atoi(freq[:i])
				if err != nil {
					return false, fmt.Errorf("invalid frequency format: %s", freq)
				}
			}
			unit = freq[i:]
			break
		}
	} else {
		interval = 1
		unit = freq
	}

	// 根据频率单位判断时间间隔
	switch unit {
	case "s": // 秒
		return curr.Sub(prev) >= time.Duration(interval)*time.Second, nil
	case "m": // 分钟
		return curr.Sub(prev) >= time.Duration(interval)*time.Minute, nil
	case "H": // 小时
		return curr.Sub(prev) >= time.Duration(interval)*time.Hour, nil
	case "D": // 天
		return curr.Sub(prev) >= time.Duration(interval)*24*time.Hour, nil
	case "W": // 周
		return curr.Sub(prev) >= time.Duration(interval)*7*24*time.Hour, nil
	case "M": // 月
		// 检查是否跨月
		monthsDiff := (curr.Year()-prev.Year())*12 + int(curr.Month()-prev.Month())
		return monthsDiff >= interval, nil
	case "Y": // 年
		return curr.Year()-prev.Year() >= interval, nil
	default:
		return false, fmt.Errorf("unsupported frequency unit: %s", unit)
	}
}
