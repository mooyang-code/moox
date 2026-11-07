// Package fundsettings 解析组合账户的资金设置（t_logical_accounts.c_fund_settings_json），
// 并给出由设置推出的排除资产。空对象表示全部取默认值。
package fundsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

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
	// StablecoinMargin 是稳定币保证金的资产集合；StablecoinTargets 是各稳定币的目标比例（和为 1，含 USDT）。
	StablecoinMargin  []string
	StablecoinTargets map[string]shared.Decimal
	// BNBDiscount 为真表示用 BNB 抵扣手续费（会话启动和设置变更时调用交易所的抵扣接口）。
	BNBDiscount bool
	// DustConvert 为真表示每隔 DustConvertInterval 把 MooX 交易留下的（受管的）、不在当前目标里的、非排除的、低于 5U 的现货零头兑成 BNB。
	DustConvert         bool
	DustConvertInterval time.Duration
	// MarginCoins 是保证金币及其配置的固定 U 值，按配置值计入权益。
	MarginCoins map[string]shared.Decimal
	// ArbitrageBaseAssets 是套利底仓资产：现货和对应永续都不参与策略持仓，按净值计入权益。
	ArbitrageBaseAssets []string
}

// rawDocument 和 Document 用同一组 json 键：宽松解析按字段单独处理，字段全是原始 JSON。
// 读取时键名匹配用 encoding/json（不区分大小写），两边完全一致，不会出现一边认得一边不认的情况；
// 写入设置时（RPC 传结构化文档）用 ParseDocumentForWrite 校验，键名由 proto 字段固定。
// 两个结构体的键集合必须相同，由测试锁住。
type rawDocument struct {
	ReserveRatio            json.RawMessage `json:"reserve_ratio"`
	BNBDiscount             json.RawMessage `json:"bnb_discount"`
	BNBReserveAmount        json.RawMessage `json:"bnb_reserve_amount"`
	DustConvertEnabled      json.RawMessage `json:"dust_convert_enabled"`
	DustConvertIntervalDays json.RawMessage `json:"dust_convert_interval_days"`
	StablecoinMargin        json.RawMessage `json:"stablecoin_margin"`
	MarginCoins             json.RawMessage `json:"margin_coins"`
	ArbitrageBaseAssets     json.RawMessage `json:"arbitrage_base_assets"`
}

// Document 是资金设置的 JSON 形态；数值用字符串，没写的键取默认值。
type Document struct {
	ReserveRatio            string            `json:"reserve_ratio,omitempty"`
	BNBDiscount             bool              `json:"bnb_discount,omitempty"`
	BNBReserveAmount        string            `json:"bnb_reserve_amount,omitempty"`
	DustConvertEnabled      bool              `json:"dust_convert_enabled,omitempty"`
	DustConvertIntervalDays int               `json:"dust_convert_interval_days,omitempty"`
	StablecoinMargin        map[string]string `json:"stablecoin_margin,omitempty"`
	MarginCoins             map[string]string `json:"margin_coins,omitempty"`
	ArbitrageBaseAssets     []string          `json:"arbitrage_base_assets,omitempty"`
}

// BNBAsset 是 BNB 保留涉及的资产。
const BNBAsset = "BNB"

// BNBReserveSlack 是 BNB 补买的容差：BNB 市值低于“保留金额 − 10”才补买。
var BNBReserveSlack = shared.MustDecimal("10")

const (
	defaultDustConvertIntervalDays = 15
	maxDustConvertIntervalDays     = 365
)

// ParseDocumentForWrite 校验 RPC 传来的结构化文档，返回有效设置和规范 JSON。
// settlementAsset 是组合的结算币：稳定币保证金必须含它，保证金币、套利底仓不能写它。
func ParseDocumentForWrite(doc Document, settlementAsset string) (Settings, string, error) {
	return doc.validateAndCanonical(settlementAsset)
}

func (d Document) validateAndCanonical(settlementAsset string) (Settings, string, error) {
	settings, err := d.validate(settlementAsset)
	if err != nil {
		return Settings{}, "", err
	}
	normalized := d
	normalized.StablecoinMargin = normalizeDecimalMap(d.StablecoinMargin)
	normalized.MarginCoins = normalizeDecimalMap(d.MarginCoins)
	normalized.ArbitrageBaseAssets = settings.ArbitrageBaseAssets
	if len(normalized.ArbitrageBaseAssets) == 0 {
		normalized.ArbitrageBaseAssets = nil
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return Settings{}, "", err
	}
	return settings, string(encoded), nil
}

// validate 校验并换算为有效设置；settlementAsset 非空时再检查结算币相关的规则。
func (d Document) validate(settlementAsset string) (Settings, error) {
	settlementAsset = strings.ToUpper(strings.TrimSpace(settlementAsset))
	settings := Settings{ReserveRatio: shared.Zero(), BNBReserveAmount: shared.Zero(), BNBDiscount: d.BNBDiscount, DustConvert: d.DustConvertEnabled}
	var err error
	if settings.ReserveRatio, err = parseOptional(d.ReserveRatio, "资金预留比例"); err != nil {
		return Settings{}, err
	}
	if settings.ReserveRatio.IsNegative() || settings.ReserveRatio.Cmp(shared.MustDecimal("1")) >= 0 {
		return Settings{}, fmt.Errorf("资金预留比例必须在 0 到 1 之间（不含 1）")
	}
	if settings.BNBReserveAmount, err = parseOptional(d.BNBReserveAmount, "BNB 保留金额"); err != nil {
		return Settings{}, err
	}
	if settings.BNBReserveAmount.IsNegative() {
		return Settings{}, fmt.Errorf("BNB 保留金额不能为负")
	}
	// 市值低于“保留金额 − 10”才补买：保留金额不大于 10 时永远不会补买，换算目标时却一直扣着缺口。
	if amount := settings.BNBReserveAmount; amount.Cmp(shared.Zero()) > 0 && amount.Cmp(BNBReserveSlack) <= 0 {
		return Settings{}, fmt.Errorf("BNB 保留金额要大于 %s：市值低于保留金额减 %s 才会补买，不大于 %s 的设置永远不会补买", BNBReserveSlack, BNBReserveSlack, BNBReserveSlack)
	}
	days := d.DustConvertIntervalDays
	switch {
	case days == 0:
		days = defaultDustConvertIntervalDays
	case days < 0 || days > maxDustConvertIntervalDays:
		return Settings{}, fmt.Errorf("小额兑换 BNB 的间隔必须在 1 到 %d 天之间", maxDustConvertIntervalDays)
	}
	settings.DustConvertInterval = time.Duration(days) * 24 * time.Hour
	owners := make(map[string]string)
	claim := func(asset, group string) error {
		if !assetPattern.MatchString(asset) {
			return fmt.Errorf("资产名 %q 不合法（只允许字母和数字）", asset)
		}
		if settlementAsset != "" && asset == settlementAsset && group != "稳定币保证金" {
			return fmt.Errorf("%s 是这个组合的结算币，不能写进%s", settlementAsset, group)
		}
		if previous, taken := owners[asset]; taken {
			if previous == group {
				return fmt.Errorf("%s 里的资产 %s 重复（资产名不区分大小写）", group, asset)
			}
			return fmt.Errorf("资产 %s 同时出现在%s和%s里", asset, previous, group)
		}
		owners[asset] = group
		return nil
	}
	if len(d.StablecoinMargin) > 0 {
		settings.StablecoinTargets = make(map[string]shared.Decimal, len(d.StablecoinMargin))
		sum := shared.Zero()
		for asset, value := range d.StablecoinMargin {
			asset = strings.ToUpper(strings.TrimSpace(asset))
			if err := claim(asset, "稳定币保证金"); err != nil {
				return Settings{}, err
			}
			ratio, err := shared.ParseDecimal(value)
			if err != nil || ratio.Cmp(shared.Zero()) <= 0 || ratio.Cmp(shared.MustDecimal("1")) > 0 {
				return Settings{}, fmt.Errorf("稳定币 %s 的目标比例必须是大于 0 且不超过 1 的数字", asset)
			}
			settings.StablecoinTargets[asset] = ratio
			settings.StablecoinMargin = append(settings.StablecoinMargin, asset)
			sum = sum.Add(ratio)
		}
		sort.Strings(settings.StablecoinMargin)
		if _, hasSettlement := settings.StablecoinTargets[settlementAsset]; settlementAsset != "" && !hasSettlement {
			return Settings{}, fmt.Errorf("稳定币保证金必须包含结算币 %s", settlementAsset)
		}
		if sum.Cmp(shared.MustDecimal("1")) != 0 {
			return Settings{}, fmt.Errorf("稳定币保证金的目标比例之和必须等于 1，现在是 %s", sum.String())
		}
	}
	if len(d.MarginCoins) > 0 {
		settings.MarginCoins = make(map[string]shared.Decimal, len(d.MarginCoins))
	}
	for asset, value := range d.MarginCoins {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if err := claim(asset, "保证金币"); err != nil {
			return Settings{}, err
		}
		parsed, err := parseOptional(value, "保证金币 "+asset)
		if err != nil {
			return Settings{}, err
		}
		if parsed.IsNegative() {
			return Settings{}, fmt.Errorf("保证金币 %s 的配置值不能为负", asset)
		}
		settings.MarginCoins[asset] = parsed
	}
	for _, asset := range d.ArbitrageBaseAssets {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if err := claim(asset, "套利底仓"); err != nil {
			return Settings{}, err
		}
		settings.ArbitrageBaseAssets = append(settings.ArbitrageBaseAssets, asset)
	}
	sort.Strings(settings.ArbitrageBaseAssets)
	// BNB 保留开着时 BNB 是补买专用的排除资产，不能再同时当保证金币、底仓或稳定币。
	if settings.BNBReserveAmount.Cmp(shared.Zero()) > 0 {
		if group, taken := owners[BNBAsset]; taken {
			return Settings{}, fmt.Errorf("开启 BNB 保留时，%s 不能同时出现在%s里", BNBAsset, group)
		}
	}
	return settings, nil
}

var assetPattern = regexp.MustCompile(`^[A-Z0-9]{1,20}$`)

// normalizeDecimalMap 把资产键转大写、数值转规范小数，写进数据库的形态统一。
func normalizeDecimalMap(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(raw))
	for asset, value := range raw {
		parsed, _ := shared.ParseDecimal(value)
		normalized[strings.ToUpper(strings.TrimSpace(asset))] = parsed.String()
	}
	return normalized
}

// ParseLenient 逐字段宽松解析，给清仓、收敛、受管资产这类必须能执行的路径用：
//   - 数值字段（资金预留比例、保证金币的配置值）解析失败取默认值，原因放进 warning；
//   - bnb_reserve_amount 解析失败按“开启了 BNB 保留”处理（金额按 0），BNB 仍是排除资产；
//   - 排除集合（稳定币保证金、保证金币、套利底仓）只要读得出键就保留，数值坏了不影响排除；
//   - 排除字段本身的类型坏了、整份不是合法 JSON：无从知道排除哪些资产，返回 err，调用方不能下单。
func ParseLenient(raw string) (settings Settings, warning error, err error) {
	settings = Settings{ReserveRatio: shared.Zero(), BNBReserveAmount: shared.Zero(), DustConvertInterval: defaultDustConvertIntervalDays * 24 * time.Hour}
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
	if value := fields.BNBDiscount; len(value) > 0 {
		if err := json.Unmarshal(value, &settings.BNBDiscount); err != nil {
			warnings = append(warnings, fmt.Errorf("BNB 抵扣开关不是布尔值，按关闭处理：%w", err))
			settings.BNBDiscount = false
		}
	}
	if value := fields.DustConvertEnabled; len(value) > 0 {
		if err := json.Unmarshal(value, &settings.DustConvert); err != nil {
			warnings = append(warnings, fmt.Errorf("小额兑换开关不是布尔值，按关闭处理：%w", err))
			settings.DustConvert = false
		}
	}
	if value := fields.DustConvertIntervalDays; len(value) > 0 {
		var days int
		if err := json.Unmarshal(value, &days); err != nil || days < 0 || days > maxDustConvertIntervalDays {
			warnings = append(warnings, fmt.Errorf("小额兑换间隔不合法，按 %d 天处理", defaultDustConvertIntervalDays))
		} else if days > 0 {
			settings.DustConvertInterval = time.Duration(days) * 24 * time.Hour
		}
	}
	if value := fields.StablecoinMargin; len(value) > 0 {
		var assets map[string]json.RawMessage
		if err := json.Unmarshal(value, &assets); err != nil {
			return Settings{}, nil, fmt.Errorf("资金设置里稳定币保证金（stablecoin_margin）的格式不对，无法确定排除资产：%w", err)
		}
		targets := make(map[string]shared.Decimal, len(assets))
		var targetErr error
		for asset, raw := range assets {
			asset = strings.ToUpper(asset)
			settings.StablecoinMargin = append(settings.StablecoinMargin, asset)
			ratio, err := decimalField(raw, "稳定币 "+asset+" 的目标比例")
			if err == nil && (ratio.Cmp(shared.Zero()) <= 0 || ratio.Cmp(shared.MustDecimal("1")) > 0) {
				err = fmt.Errorf("稳定币 %s 的目标比例必须大于 0 且不超过 1", asset)
			}
			if err != nil {
				targetErr = err
				continue
			}
			targets[asset] = ratio
		}
		sort.Strings(settings.StablecoinMargin)
		// 目标比例坏了只影响调平：排除集合照常保留，不做调平。
		if targetErr != nil {
			warnings = append(warnings, fmt.Errorf("%w（稳定币保证金不做调平）", targetErr))
		} else {
			settings.StablecoinTargets = targets
		}
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

// Document 返回有效设置的完整文档形态（默认值也写出来），给界面显示当前生效的值。
func (s Settings) Document() Document {
	document := Document{
		ReserveRatio:            s.ReserveRatio.String(),
		BNBDiscount:             s.BNBDiscount,
		BNBReserveAmount:        s.BNBReserveAmount.String(),
		DustConvertEnabled:      s.DustConvert,
		DustConvertIntervalDays: int(s.DustConvertInterval / (24 * time.Hour)),
		ArbitrageBaseAssets:     append([]string(nil), s.ArbitrageBaseAssets...),
	}
	if len(s.StablecoinTargets) > 0 {
		document.StablecoinMargin = make(map[string]string, len(s.StablecoinTargets))
		for asset, ratio := range s.StablecoinTargets {
			document.StablecoinMargin[asset] = ratio.String()
		}
	}
	if len(s.MarginCoins) > 0 {
		document.MarginCoins = make(map[string]string, len(s.MarginCoins))
		for asset, value := range s.MarginCoins {
			document.MarginCoins[asset] = value.String()
		}
	}
	return document
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
