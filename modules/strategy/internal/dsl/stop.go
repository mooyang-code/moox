package dsl

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// 止损止盈（设计 4.1）：rank、signal 规则写在规则上，tranche 写在子策略模板里。按 K 线收盘判断，触发后平掉该持仓单位。
const (
	StopPriceClose   = "close"   // 判断与更新最有利价格都用本期 close
	StopPriceExtreme = "extreme" // 止损看不利方向的极值、止盈与最有利价格看有利方向的极值（多头 low/high，空头 high/low）

	maxStopProfit = 10
)

// Stop 是一组止损止盈参数；比例为 0 表示不启用该项。
type Stop struct {
	Loss         float64 `json:"loss,omitempty"`
	Trailing     float64 `json:"trailing,omitempty"`
	Profit       float64 `json:"profit,omitempty"`
	Price        string  `json:"price"`
	CooldownBars int     `json:"cooldown_bars,omitempty"`
}

// Columns 返回判断止损需要读取的列：入场价总是 close，extreme 时还要 high 与 low。
func (s *Stop) Columns() []string {
	if s == nil {
		return nil
	}
	if s.Price == StopPriceExtreme {
		return []string{"close", "high", "low"}
	}
	return []string{"close"}
}

func decodeStop(node *yaml.Node, path string) (*Stop, error) {
	fields, err := mappingFields(node, path)
	if err != nil {
		return nil, err
	}
	stop := &Stop{Price: StopPriceClose}
	for _, f := range fields {
		fieldPath := path + "." + f.key
		switch f.key {
		case "loss":
			stop.Loss, err = scalarFloat(f.value, fieldPath)
		case "trailing":
			stop.Trailing, err = scalarFloat(f.value, fieldPath)
		case "profit":
			stop.Profit, err = scalarFloat(f.value, fieldPath)
		case "price":
			stop.Price, err = scalarRequiredText(f.value, fieldPath)
		case "cooldown_bars":
			stop.CooldownBars, err = scalarInt(f.value, fieldPath)
		default:
			return nil, fmt.Errorf("%s 包含未知字段 %q", path, f.key)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := validateStop(path, stop); err != nil {
		return nil, err
	}
	return stop, nil
}

func validateStop(path string, stop *Stop) error {
	if stop.Loss == 0 && stop.Trailing == 0 && stop.Profit == 0 {
		return fmt.Errorf("%s 至少要写 loss、trailing、profit 中的一项", path)
	}
	if stop.Loss < 0 || stop.Loss >= 1 {
		return fmt.Errorf("%s.loss 必须在 (0, 1) 之间", path)
	}
	if stop.Trailing < 0 || stop.Trailing >= 1 {
		return fmt.Errorf("%s.trailing 必须在 (0, 1) 之间", path)
	}
	if stop.Profit < 0 || stop.Profit > maxStopProfit {
		return fmt.Errorf("%s.profit 必须在 (0, %d] 之间", path, maxStopProfit)
	}
	if stop.Price != StopPriceClose && stop.Price != StopPriceExtreme {
		return fmt.Errorf("%s.price 只能是 %s 或 %s", path, StopPriceClose, StopPriceExtreme)
	}
	if stop.CooldownBars < 0 || stop.CooldownBars > MaxCount {
		return fmt.Errorf("%s.cooldown_bars 必须在 0 到 %d 之间", path, MaxCount)
	}
	return nil
}

// StopColumns 返回规则的止损需要读取的列（可选列：缺值不算缺数，只是本期不判止损）。
func (r Rule) StopColumns() []string {
	set := make(map[string]struct{})
	if r.Stop != nil {
		for _, column := range r.Stop.Columns() {
			set[column] = struct{}{}
		}
	}
	if r.Tranche != nil {
		for _, sub := range r.Tranche.Subs {
			for _, column := range sub.Stop.Columns() {
				set[column] = struct{}{}
			}
		}
	}
	return sortedKeys(set)
}
