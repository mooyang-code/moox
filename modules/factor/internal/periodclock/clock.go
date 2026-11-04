// Package periodclock 提供因子任务的周期计算，以及真实时间与交易序号时间轴之间的换算。
//
// 交易序号时间轴：轴上时间 = 纪元 + barclock 交易序号 × 周期。在轴上，有交易时段的市场（A 股）的 K 线排成一条没有休市的
// 连续序列，引擎内部（本地存储、窗口、填充、长缺口、连续水位、跳过账本）全部按轴上时间用 Continuous 做周期数学；
// 7×24 市场的轴就是真实时间本身。真实时间只在边界（Storage 读写、事件入口、引擎对外接口）上换算。
package periodclock

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
)

var (
	ErrUnsupportedCalendar  = errors.New("unsupported space calendar")
	ErrUnsupportedFrequency = errors.New("unsupported period frequency")
)

// Clock 计算一个市场日历的周期边界、回看窗口，以及真实时间与轴上时间的换算。
// Duration、Align、Window 面向真实时间（管理端校验用户输入、按交易 K 线往回数）。
type Clock interface {
	Duration(freq string) (time.Duration, error)
	Align(t time.Time, freq string) (time.Time, error)
	Window(t time.Time, freq string, n int) ([]time.Time, error)
	// ToAxis 把真实的桶起点（data_time）换成轴上时间；t 不是该频率的桶起点时返回错误。
	ToAxis(t time.Time, freq string) (time.Time, error)
	// FromAxis 把轴上时间换回真实的桶起点。
	FromAxis(a time.Time, freq string) (time.Time, error)
	// AxisEnd 返回真实时刻 t 在轴上的排他终点：收盘不晚于 t 的最新一根的轴上时间 + 周期。
	// 用于“当前时刻”和各种排他终点。
	AxisEnd(t time.Time, freq string) (time.Time, error)
}

var sessions sync.Map // 空间 → *Session

// ForSpace 返回空间所用的时钟：7×24 市场是 Continuous，有交易时段的市场是按 barclock 实现的 Session。
// 未登记的空间报错。
func ForSpace(spaceID string) (Clock, error) {
	market, err := barclock.MarketFor(spaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedCalendar, spaceID)
	}
	if market.Alignment == barclock.AlignmentEpochUTC {
		return Continuous{}, nil
	}
	key := strings.ToLower(strings.TrimSpace(spaceID))
	if cached, ok := sessions.Load(key); ok {
		return cached.(*Session), nil
	}
	cached, _ := sessions.LoadOrStore(key, &Session{market: market})
	return cached.(*Session), nil
}

// IsContinuous 报告空间是否 7×24 市场（轴就是真实时间）；未登记的空间返回 false。
func IsContinuous(spaceID string) bool {
	clock, err := ForSpace(spaceID)
	if err != nil {
		return false
	}
	_, ok := clock.(Continuous)
	return ok
}
