package utils

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"
)

// TradingCalendar A股交易日历管理器
type TradingCalendar struct {
	tradingDays map[string]bool // 交易日映射表，key为"YYYY-MM-DD"格式的日期
}

// NewTradingCalendar 创建新的交易日历管理器
func NewTradingCalendar() *TradingCalendar {
	return &TradingCalendar{
		tradingDays: make(map[string]bool),
	}
}

// LoadFromCSV 从CSV文件加载交易日历
// CSV文件格式：日期,是否交易日
// 示例：
// 2024-03-20,1
// 2024-03-21,1
// 2024-03-22,0
func (tc *TradingCalendar) LoadFromCSV(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("打开交易日历文件失败: %v", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return fmt.Errorf("读取CSV文件失败: %v", err)
	}

	// 跳过标题行
	for i := 1; i < len(records); i++ {
		record := records[i]
		if len(record) < 2 {
			continue
		}

		date := record[0]
		isTradingDay, err := strconv.ParseBool(record[1])
		if err != nil {
			continue
		}

		tc.tradingDays[date] = isTradingDay
	}

	return nil
}

// IsTradingDay 判断指定日期是否为交易日
func (tc *TradingCalendar) IsTradingDay(date time.Time) bool {
	dateStr := date.Format("2006-01-02")
	return tc.tradingDays[dateStr]
}

// GetNextTradingDay 获取下一个交易日
func (tc *TradingCalendar) GetNextTradingDay(date time.Time) time.Time {
	nextDay := date.Add(24 * time.Hour)
	for !tc.IsTradingDay(nextDay) {
		nextDay = nextDay.Add(24 * time.Hour)
	}
	return nextDay
}

// GetPrevTradingDay 获取上一个交易日
func (tc *TradingCalendar) GetPrevTradingDay(date time.Time) time.Time {
	prevDay := date.Add(-24 * time.Hour)
	for !tc.IsTradingDay(prevDay) {
		prevDay = prevDay.Add(-24 * time.Hour)
	}
	return prevDay
}

// GetTradingDaysInRange 获取指定日期范围内的所有交易日
func (tc *TradingCalendar) GetTradingDaysInRange(startDate, endDate time.Time) []time.Time {
	var tradingDays []time.Time
	currentDate := startDate

	for !currentDate.After(endDate) {
		if tc.IsTradingDay(currentDate) {
			tradingDays = append(tradingDays, currentDate)
		}
		currentDate = currentDate.Add(24 * time.Hour)
	}

	return tradingDays
}

// IsValidTradingTime 检查指定时间是否在交易时间内
func (tc *TradingCalendar) IsValidTradingTime(t time.Time) bool {
	// 检查是否是交易日
	if !tc.IsTradingDay(t) {
		return false
	}

	// 获取时间的小时和分钟
	hour := t.Hour()
	minute := t.Minute()

	// 上午交易时段：9:30-11:30
	if (hour == 9 && minute >= 30) || (hour == 10) || (hour == 11 && minute <= 30) {
		return true
	}

	// 下午交易时段：13:00-15:00
	if (hour == 13) || (hour == 14) || (hour == 15 && minute == 0) {
		return true
	}
	return false
}
