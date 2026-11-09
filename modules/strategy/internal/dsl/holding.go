package dsl

import (
	"fmt"
	"strings"
	"time"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/marketcalendar"
	"gopkg.in/yaml.v3"
)

// holding.calendar 的取值：按交易日计数的周期、自然周、自然月。
const (
	CalendarTradingDays = "trading_days"
	CalendarWeek        = "week"
	CalendarMonth       = "month"

	// HoldingAtFirst、HoldingAtLast 是日内帧上建仓所在的那一根：交易日的第一根或最后一根（默认）。
	HoldingAtFirst = "first"
	HoldingAtLast  = "last"
)

// IsCalendar 报告 holding 是否按交易日历换仓。
func (h *Holding) IsCalendar() bool { return h != nil && h.Calendar != "" }

// BatchKeys 返回批次的键：bars 写法是 offsets，日历写法是 days 的值。
func (h *Holding) BatchKeys() []int {
	if h.IsCalendar() {
		return h.Days
	}
	return h.Offsets
}

// AtLast 报告日内帧是否在交易日的最后一根建仓。
func (h *Holding) AtLast() bool { return h.At != HoldingAtFirst }

// Anchor 是解析后的锚点：Date 非零表示写的是交易日期（该日第一根），否则 At 是 RFC3339 时刻（某根的 bar_start）。
type Anchor struct {
	Date marketcalendar.CivilDate
	At   time.Time
}

// ParseAnchor 解析 holding.anchor：RFC3339 时刻，或交易日期 YYYY-MM-DD。
func ParseAnchor(text string) (Anchor, error) {
	text = strings.TrimSpace(text)
	if date, err := marketcalendar.ParseCivilDate(text); err == nil {
		return Anchor{Date: date}, nil
	}
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return Anchor{}, fmt.Errorf("必须是 RFC3339 时刻（例如 2024-01-02T00:00:00Z）或交易日期 YYYY-MM-DD")
	}
	return Anchor{At: at.UTC()}, nil
}

func decodeHolding(node *yaml.Node, path string) (Holding, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return Holding{}, err
	}
	var holding Holding
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "bars":
			holding.Bars, err = scalarInt(f.value, fieldPath)
		case "offsets":
			holding.Offsets, err = intList(f.value, fieldPath)
		case "anchor":
			holding.Anchor, err = scalarRequiredText(f.value, fieldPath)
		case "calendar":
			holding.Calendar, err = scalarRequiredText(f.value, fieldPath)
		case "every":
			holding.Every, err = scalarInt(f.value, fieldPath)
		case "days":
			holding.Days, err = intList(f.value, fieldPath)
		case "at":
			holding.At, err = scalarRequiredText(f.value, fieldPath)
		default:
			return Holding{}, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return Holding{}, err
		}
	}
	return holding, nil
}

func validateHolding(prefix string, holding *Holding) error {
	if holding.Anchor != "" {
		if _, err := ParseAnchor(holding.Anchor); err != nil {
			return fmt.Errorf("%s 的 holding.anchor %s", prefix, err)
		}
	}
	if holding.IsCalendar() {
		return validateCalendarHolding(prefix, holding)
	}
	if holding.Every != 0 || holding.Days != nil || holding.At != "" {
		return fmt.Errorf("%s 的 holding.every、days、at 只能与 holding.calendar 一起写", prefix)
	}
	if holding.Bars <= 0 {
		return fmt.Errorf("%s 的 holding.bars 必须大于 0", prefix)
	}
	if holding.Bars > MaxCount {
		return fmt.Errorf("%s 的 holding.bars 不能超过 %d", prefix, MaxCount)
	}
	if len(holding.Offsets) == 0 {
		return fmt.Errorf("%s 的 holding.offsets 不能为空", prefix)
	}
	if len(holding.Offsets) > MaxHoldingOffsets {
		return fmt.Errorf("%s 的 holding.offsets 不能超过 %d 个", prefix, MaxHoldingOffsets)
	}
	seen := make(map[int]struct{}, len(holding.Offsets))
	for _, offset := range holding.Offsets {
		if offset < 0 || offset >= holding.Bars {
			return fmt.Errorf("%s 的 holding.offsets 必须在 0 到 bars-1 之间", prefix)
		}
		if _, exists := seen[offset]; exists {
			return fmt.Errorf("%s 的 holding.offsets 包含重复值 %d", prefix, offset)
		}
		seen[offset] = struct{}{}
	}
	return nil
}

// validateCalendarHolding 校验日历写法：calendar 与 bars、offsets 互斥；days 按周期给出范围（-1 是最后一个交易日）。
func validateCalendarHolding(prefix string, holding *Holding) error {
	if holding.Bars != 0 || holding.Offsets != nil {
		return fmt.Errorf("%s 的 holding.calendar 与 bars、offsets 只能写一种", prefix)
	}
	limit := 0
	switch holding.Calendar {
	case CalendarTradingDays:
		if holding.Every <= 0 || holding.Every > MaxCount {
			return fmt.Errorf("%s 的 holding.every 必须在 1 到 %d 之间（calendar: trading_days 时必填）", prefix, MaxCount)
		}
		limit = holding.Every
	case CalendarWeek, CalendarMonth:
		if holding.Every != 0 {
			return fmt.Errorf("%s 的 holding.every 只能与 calendar: trading_days 一起写", prefix)
		}
		if holding.Anchor != "" {
			return fmt.Errorf("%s 的 holding.anchor 不能与 calendar: %s 一起写：周、月的起点是固定的", prefix, holding.Calendar)
		}
		limit = 7
		if holding.Calendar == CalendarMonth {
			limit = 31
		}
	default:
		return fmt.Errorf("%s 的 holding.calendar 只能是 trading_days、week 或 month", prefix)
	}
	if holding.At != "" && holding.At != HoldingAtFirst && holding.At != HoldingAtLast {
		return fmt.Errorf("%s 的 holding.at 只能是 first 或 last", prefix)
	}
	if len(holding.Days) == 0 {
		return fmt.Errorf("%s 的 holding.days 不能为空", prefix)
	}
	if len(holding.Days) > MaxHoldingOffsets {
		return fmt.Errorf("%s 的 holding.days 不能超过 %d 个", prefix, MaxHoldingOffsets)
	}
	seen := make(map[int]struct{}, len(holding.Days))
	for _, day := range holding.Days {
		valid := day >= 1 && day <= limit
		if holding.Calendar != CalendarTradingDays {
			valid = valid || (day <= -1 && day >= -limit)
		}
		if !valid {
			if holding.Calendar == CalendarTradingDays {
				return fmt.Errorf("%s 的 holding.days 必须在 1 到 every=%d 之间", prefix, limit)
			}
			return fmt.Errorf("%s 的 holding.days 必须在 1 到 %d 之间，或 -1 到 -%d（倒数）", prefix, limit, limit)
		}
		if _, exists := seen[day]; exists {
			return fmt.Errorf("%s 的 holding.days 包含重复值 %d", prefix, day)
		}
		seen[day] = struct{}{}
	}
	return nil
}

// validateAnchorAlignment 在保存时尽早检查锚点：DSL 写了 bar 且写了 market（加密，股票不允许写 market）时，
// RFC3339 锚点必须落在这个周期的 bar_start 上。其余情况（股票、没写 bar）在启用时由输入层检查。
func validateAnchorAlignment(s *Strategy) error {
	if !s.MarketDeclared || s.Bar == "" {
		return nil
	}
	bar, err := frequencypkg.Parse(s.Bar)
	if err != nil {
		return nil
	}
	duration := bar.Duration()
	for _, rule := range s.Rules {
		if rule.Holding == nil || rule.Holding.Anchor == "" {
			continue
		}
		anchor, err := ParseAnchor(rule.Holding.Anchor)
		if err != nil || anchor.At.IsZero() {
			continue
		}
		if anchor.At.UnixNano()%duration.Nanoseconds() != 0 {
			return fmt.Errorf("规则 %s 的 holding.anchor %s 不是 %s 周期的 bar 起点", rule.ID, rule.Holding.Anchor, s.Bar)
		}
	}
	return nil
}
