// Package fundsettings 解析组合账户的资金设置（t_logical_accounts.c_fund_settings_json），
// 并给出由设置推出的排除资产。空对象表示全部取默认值。
package fundsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// Settings 是资金设置里换算权益和受管资产要用到的部分；其余设置（BNB 抵扣、小额兑换等）由各自的任务读取。
type Settings struct {
	// ReserveRatio 是换算目标时从权益里扣掉的比例，0 到 1。
	ReserveRatio shared.Decimal
	// BNBReserveUnreadable 为真表示 bnb_reserve_amount 读不出来：按开启了 BNB 保留处理，BNB 仍是排除资产。
	BNBReserveUnreadable bool
	// BNBReserveAmount 大于 0 表示开启 BNB 保留：BNB 现货标的成为排除资产，权益只扣还差的部分。
	BNBReserveAmount shared.Decimal
	// StablecoinMargin 是稳定币保证金的资产集合。
	StablecoinMargin []string
	// MarginCoins 是保证金币及其配置的固定 U 值，按配置值计入权益。
	MarginCoins map[string]shared.Decimal
	// ArbitrageBaseAssets 是套利底仓资产：现货和对应永续都不参与策略持仓，按净值计入权益。
	ArbitrageBaseAssets []string
}

// rawDocument 和 document 用同一组 json 标签，字段全是原始 JSON：宽松解析按字段单独处理，
// 键名匹配和严格解析（encoding/json 解到结构体，键名不区分大小写）完全一致，不会出现一边认得一边不认的情况。
// 写入设置时（T22）应拒绝未知键和大小写不规范的键，这里只保证读的时候两边结论相同。
type rawDocument struct {
	ReserveRatio        json.RawMessage `json:"reserve_ratio"`
	BNBReserveAmount    json.RawMessage `json:"bnb_reserve_amount"`
	StablecoinMargin    json.RawMessage `json:"stablecoin_margin"`
	MarginCoins         json.RawMessage `json:"margin_coins"`
	ArbitrageBaseAssets json.RawMessage `json:"arbitrage_base_assets"`
}

type document struct {
	ReserveRatio        string            `json:"reserve_ratio"`
	BNBReserveAmount    string            `json:"bnb_reserve_amount"`
	StablecoinMargin    map[string]string `json:"stablecoin_margin"`
	MarginCoins         map[string]string `json:"margin_coins"`
	ArbitrageBaseAssets []string          `json:"arbitrage_base_assets"`
}

// BNBAsset 是 BNB 保留涉及的资产。
const BNBAsset = "BNB"

// Parse 解析资金设置 JSON；空串和空对象返回全部默认值。
func Parse(raw string) (Settings, error) {
	settings := Settings{ReserveRatio: shared.Zero(), BNBReserveAmount: shared.Zero()}
	if strings.TrimSpace(raw) == "" {
		return settings, nil
	}
	var doc document
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return Settings{}, fmt.Errorf("资金设置不是合法 JSON：%w", err)
	}
	var err error
	if settings.ReserveRatio, err = parseOptional(doc.ReserveRatio, "资金预留比例"); err != nil {
		return Settings{}, err
	}
	if settings.ReserveRatio.IsNegative() || settings.ReserveRatio.Cmp(shared.MustDecimal("1")) >= 0 {
		return Settings{}, fmt.Errorf("资金预留比例必须在 0 到 1 之间（不含 1）")
	}
	if settings.BNBReserveAmount, err = parseOptional(doc.BNBReserveAmount, "BNB 保留金额"); err != nil {
		return Settings{}, err
	}
	if settings.BNBReserveAmount.IsNegative() {
		return Settings{}, fmt.Errorf("BNB 保留金额不能为负")
	}
	for asset := range doc.StablecoinMargin {
		settings.StablecoinMargin = append(settings.StablecoinMargin, strings.ToUpper(asset))
	}
	sort.Strings(settings.StablecoinMargin)
	if len(doc.MarginCoins) > 0 {
		settings.MarginCoins = make(map[string]shared.Decimal, len(doc.MarginCoins))
	}
	for asset, value := range doc.MarginCoins {
		parsed, err := parseOptional(value, "保证金币 "+asset)
		if err != nil {
			return Settings{}, err
		}
		if parsed.IsNegative() {
			return Settings{}, fmt.Errorf("保证金币 %s 的配置值不能为负", asset)
		}
		settings.MarginCoins[strings.ToUpper(asset)] = parsed
	}
	for _, asset := range doc.ArbitrageBaseAssets {
		settings.ArbitrageBaseAssets = append(settings.ArbitrageBaseAssets, strings.ToUpper(asset))
	}
	sort.Strings(settings.ArbitrageBaseAssets)
	return settings, nil
}

// ParseLenient 逐字段宽松解析，给清仓、收敛、受管资产这类必须能执行的路径用：
//   - 数值字段（资金预留比例、保证金币的配置值）解析失败取默认值，原因放进 warning；
//   - bnb_reserve_amount 解析失败按“开启了 BNB 保留”处理（金额按 0），BNB 仍是排除资产；
//   - 排除集合（稳定币保证金、保证金币、套利底仓）只要读得出键就保留，数值坏了不影响排除；
//   - 排除字段本身的类型坏了、整份不是合法 JSON：无从知道排除哪些资产，返回 err，调用方不能下单。
func ParseLenient(raw string) (settings Settings, warning error, err error) {
	settings = Settings{ReserveRatio: shared.Zero(), BNBReserveAmount: shared.Zero()}
	if strings.TrimSpace(raw) == "" {
		return settings, nil, nil
	}
	var fields rawDocument
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return Settings{}, nil, fmt.Errorf("资金设置不是合法 JSON，无法确定排除资产：%w", err)
	}
	var warnings []error
	if value := fields.ReserveRatio; len(value) > 0 {
		ratio, err := decimalField(value, "资金预留比例")
		if err == nil && (ratio.IsNegative() || ratio.Cmp(shared.MustDecimal("1")) >= 0) {
			err = fmt.Errorf("资金预留比例必须在 0 到 1 之间（不含 1）")
		}
		if err != nil {
			warnings = append(warnings, err)
		} else {
			settings.ReserveRatio = ratio
		}
	}
	if value := fields.BNBReserveAmount; len(value) > 0 {
		amount, err := decimalField(value, "BNB 保留金额")
		if err == nil && amount.IsNegative() {
			err = fmt.Errorf("BNB 保留金额不能为负")
		}
		if err != nil {
			warnings = append(warnings, fmt.Errorf("%w（按开启 BNB 保留处理）", err))
			settings.BNBReserveUnreadable = true
		} else {
			settings.BNBReserveAmount = amount
		}
	}
	if value := fields.StablecoinMargin; len(value) > 0 {
		var assets map[string]json.RawMessage
		if err := json.Unmarshal(value, &assets); err != nil {
			return Settings{}, nil, fmt.Errorf("资金设置里稳定币保证金（stablecoin_margin）的格式不对，无法确定排除资产：%w", err)
		}
		for asset := range assets {
			settings.StablecoinMargin = append(settings.StablecoinMargin, strings.ToUpper(asset))
		}
		sort.Strings(settings.StablecoinMargin)
	}
	if value := fields.MarginCoins; len(value) > 0 {
		var coins map[string]json.RawMessage
		if err := json.Unmarshal(value, &coins); err != nil {
			return Settings{}, nil, fmt.Errorf("资金设置里保证金币（margin_coins）的格式不对，无法确定排除资产：%w", err)
		}
		if len(coins) > 0 {
			settings.MarginCoins = make(map[string]shared.Decimal, len(coins))
		}
		for asset, raw := range coins {
			parsed, err := decimalField(raw, "保证金币 "+asset)
			if err == nil && parsed.IsNegative() {
				err = fmt.Errorf("保证金币 %s 的配置值不能为负", asset)
			}
			if err != nil {
				warnings = append(warnings, err)
				parsed = shared.Zero()
			}
			settings.MarginCoins[strings.ToUpper(asset)] = parsed
		}
	}
	if value := fields.ArbitrageBaseAssets; len(value) > 0 {
		var assets []string
		if err := json.Unmarshal(value, &assets); err != nil {
			return Settings{}, nil, fmt.Errorf("资金设置里套利底仓（arbitrage_base_assets）的格式不对，无法确定排除资产：%w", err)
		}
		for _, asset := range assets {
			settings.ArbitrageBaseAssets = append(settings.ArbitrageBaseAssets, strings.ToUpper(asset))
		}
		sort.Strings(settings.ArbitrageBaseAssets)
	}
	return settings, errors.Join(warnings...), nil
}

// decimalField 把 JSON 字段按字符串小数解析。
func decimalField(raw json.RawMessage, name string) (shared.Decimal, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return shared.Zero(), fmt.Errorf("%s不是字符串形式的数字：%w", name, err)
	}
	return parseOptional(text, name)
}

func parseOptional(raw, name string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		return shared.Zero(), nil
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero(), fmt.Errorf("%s不是合法数字：%w", name, err)
	}
	return value, nil
}

// HasExclusions 报告是否有任何排除资产：没有时调用方可以跳过逐个标的的排除检查。
func (s Settings) HasExclusions() bool {
	return s.BNBReserveEnabled() || len(s.StablecoinMargin) > 0 || len(s.MarginCoins) > 0 || len(s.ArbitrageBaseAssets) > 0
}

// BNBReserveEnabled 报告是否开启了 BNB 保留。
func (s Settings) BNBReserveEnabled() bool {
	return s.BNBReserveUnreadable || s.BNBReserveAmount.Cmp(shared.Zero()) > 0
}

// IsArbitrageBase 报告资产是否是套利底仓；底仓的现货和对应永续都不参与策略持仓。
func (s Settings) IsArbitrageBase(asset string) bool {
	for _, base := range s.ArbitrageBaseAssets {
		if strings.EqualFold(base, asset) {
			return true
		}
	}
	return false
}

// ExcludedFromManaged 报告资产是否是排除资产：BNB（开启保留时）、稳定币保证金、保证金币、底仓。
// 排除资产不是受管现货，策略目标里出现它们时只跳过这一项。
func (s Settings) ExcludedFromManaged(asset string) bool {
	asset = strings.ToUpper(asset)
	if asset == BNBAsset && s.BNBReserveEnabled() {
		return true
	}
	for _, stable := range s.StablecoinMargin {
		if stable == asset {
			return true
		}
	}
	if _, ok := s.MarginCoins[asset]; ok {
		return true
	}
	return s.IsArbitrageBase(asset)
}

// MarginCoinValue 返回保证金币配置值之和，按配置值计入权益。
func (s Settings) MarginCoinValue() shared.Decimal {
	total := shared.Zero()
	for _, value := range s.MarginCoins {
		total = total.Add(value)
	}
	return total
}
