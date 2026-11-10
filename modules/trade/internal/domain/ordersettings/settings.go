// Package ordersettings 解析和校验组合账户的下单设置（t_logical_accounts.c_order_settings_json）。
// 空对象表示全部取默认值；数据库里只存 Document.Canonical 产出的规范 JSON。
package ordersettings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// ThresholdMode 是调仓阈值模式。
type ThresholdMode string

const (
	// ThresholdTargetRatio：差额不超过目标数量 × r 就不调（默认，等于一期的行为）。
	ThresholdTargetRatio ThresholdMode = "TARGET_RATIO"
	// ThresholdPositionRatio：调仓金额低于当前持仓金额 × r 就不调。
	ThresholdPositionRatio ThresholdMode = "POSITION_RATIO"
	// ThresholdEquityRatio：调仓金额低于组合权益 × r 就不调。
	ThresholdEquityRatio ThresholdMode = "EQUITY_RATIO"
	// ThresholdAlways：每期都调。
	ThresholdAlways ThresholdMode = "ALWAYS"
)

// OrderMode 是下单方式。
type OrderMode string

const (
	OrderModeMarket       OrderMode = "MARKET"
	OrderModePseudoMarket OrderMode = "PSEUDO_MARKET"
)

const (
	// MaxChildNotionalCeiling 是单笔上限能设置的最大值，也是下单校验的全局硬上限。
	MaxChildNotionalCeiling = "1000000"

	defaultThresholdRatio   = "0.05"
	defaultMinNotionalSpot  = "10"
	defaultMinNotionalSwap  = "5"
	defaultMaxChildNotional = "100000"
	defaultSlippageStep     = "0.015"
	defaultSlippageMax      = "0.06"
	defaultMaxAttempts      = 4
	maxMaxAttempts          = 10
	maxBatchIntervalSeconds = 3600
	maxRestingTTLSeconds    = 7 * 24 * 3600
)

// Document 是设置的 JSON 形态：字段全是字符串、布尔、整数和字符串映射，没写的键取默认值。
// 前端、RPC、数据库用同一组键名；写入时用 shared.DecodeStrictObject 拒绝未知键和大小写不规范的键。
type Document struct {
	RebalanceThresholdMode  string            `json:"rebalance_threshold_mode,omitempty"`
	RebalanceThresholdRatio string            `json:"rebalance_threshold_ratio,omitempty"`
	MinNotionalSpot         string            `json:"min_notional_spot,omitempty"`
	MinNotionalSwap         string            `json:"min_notional_swap,omitempty"`
	HourlyCapSpot           map[string]string `json:"hourly_cap_spot,omitempty"`
	HourlyCapSwap           map[string]string `json:"hourly_cap_swap,omitempty"`
	MaxChildNotional        string            `json:"max_child_notional,omitempty"`
	RandomizeSplit          bool              `json:"randomize_split,omitempty"`
	// BatchIntervalSeconds 为 0 表示用执行器的默认批间隔（实盘 1 秒，模拟盘 0.3 秒）。
	BatchIntervalSeconds int    `json:"batch_interval_seconds,omitempty"`
	OrderMode            string `json:"order_mode,omitempty"`
	SlippageStep         string `json:"slippage_step,omitempty"`
	SlippageMax          string `json:"slippage_max,omitempty"`
	MaxAttempts          int    `json:"max_attempts,omitempty"`
	RestingRatio         string `json:"resting_ratio,omitempty"`
	// RestingTTLSeconds 为 0 表示挂单保留到下一份目标到达为止。
	RestingTTLSeconds int `json:"resting_ttl_seconds,omitempty"`
}

// Settings 是校验后的有效设置，没写的项已经填上默认值。
type Settings struct {
	ThresholdMode   ThresholdMode
	ThresholdRatio  shared.Decimal
	MinNotionalSpot shared.Decimal
	MinNotionalSwap shared.Decimal
	// HourlyCapSpot、HourlyCapSwap：键是内部标的 ID 或 "*"（默认值），值是每个自然小时累计下单金额的上限；没有条目表示不限。
	HourlyCapSpot    map[string]shared.Decimal
	HourlyCapSwap    map[string]shared.Decimal
	MaxChildNotional shared.Decimal
	RandomizeSplit   bool
	// BatchInterval 在 BatchIntervalSet 为真时覆盖执行器的默认批间隔。
	BatchInterval    time.Duration
	BatchIntervalSet bool
	OrderMode        OrderMode
	SlippageStep     shared.Decimal
	SlippageMax      shared.Decimal
	MaxAttempts      int
	RestingRatio     shared.Decimal
	// RestingTTL 为 0 表示挂单保留到下一份目标到达为止。
	RestingTTL time.Duration
}

// Default 返回全部取默认值的设置。
func Default() Settings {
	settings, err := Document{}.Validate()
	if err != nil {
		panic(err)
	}
	return settings
}

// Parse 严格解析设置 JSON：未知键、大小写不规范的键、类型不对和越界的值都报错。空串和空对象返回默认值。
func Parse(raw string) (Settings, error) {
	var document Document
	if err := shared.DecodeStrictObject(raw, &document); err != nil {
		return Settings{}, fmt.Errorf("下单设置：%w", err)
	}
	return document.Validate()
}

// Validate 校验并换算为有效设置。
func (d Document) Validate() (Settings, error) {
	settings := Settings{}
	switch mode := ThresholdMode(d.RebalanceThresholdMode); mode {
	case "":
		settings.ThresholdMode = ThresholdTargetRatio
	case ThresholdTargetRatio, ThresholdPositionRatio, ThresholdEquityRatio, ThresholdAlways:
		settings.ThresholdMode = mode
	default:
		return Settings{}, fmt.Errorf("调仓阈值模式 %q 不支持，可选 TARGET_RATIO、POSITION_RATIO、EQUITY_RATIO、ALWAYS", d.RebalanceThresholdMode)
	}
	var err error
	if settings.ThresholdRatio, err = decimalOr(d.RebalanceThresholdRatio, defaultThresholdRatio, "调仓阈值比例"); err != nil {
		return Settings{}, err
	}
	if settings.ThresholdRatio.IsNegative() || settings.ThresholdRatio.Cmp(shared.MustDecimal("1")) > 0 {
		return Settings{}, fmt.Errorf("调仓阈值比例必须在 0 到 1 之间")
	}
	if settings.MinNotionalSpot, err = nonNegative(d.MinNotionalSpot, defaultMinNotionalSpot, "现货最小下单额"); err != nil {
		return Settings{}, err
	}
	if settings.MinNotionalSwap, err = nonNegative(d.MinNotionalSwap, defaultMinNotionalSwap, "合约最小下单额"); err != nil {
		return Settings{}, err
	}
	if settings.HourlyCapSpot, err = hourlyCaps(d.HourlyCapSpot, "SPOT", "现货", true); err != nil {
		return Settings{}, err
	}
	if settings.HourlyCapSwap, err = hourlyCaps(d.HourlyCapSwap, "SWAP", "合约", false); err != nil {
		return Settings{}, err
	}
	if settings.MaxChildNotional, err = decimalOr(d.MaxChildNotional, defaultMaxChildNotional, "单笔上限"); err != nil {
		return Settings{}, err
	}
	if settings.MaxChildNotional.Cmp(shared.Zero()) <= 0 || settings.MaxChildNotional.Cmp(shared.MustDecimal(MaxChildNotionalCeiling)) > 0 {
		return Settings{}, fmt.Errorf("单笔上限必须大于 0 且不超过 %s", MaxChildNotionalCeiling)
	}
	settings.RandomizeSplit = d.RandomizeSplit
	if d.BatchIntervalSeconds < 0 || d.BatchIntervalSeconds > maxBatchIntervalSeconds {
		return Settings{}, fmt.Errorf("批间隔必须在 0 到 %d 秒之间（0 表示用默认值）", maxBatchIntervalSeconds)
	}
	if d.BatchIntervalSeconds > 0 {
		settings.BatchInterval, settings.BatchIntervalSet = time.Duration(d.BatchIntervalSeconds)*time.Second, true
	}
	switch mode := OrderMode(d.OrderMode); mode {
	case "":
		settings.OrderMode = OrderModePseudoMarket
	case OrderModeMarket, OrderModePseudoMarket:
		settings.OrderMode = mode
	default:
		return Settings{}, fmt.Errorf("下单方式 %q 不支持，可选 MARKET、PSEUDO_MARKET", d.OrderMode)
	}
	if settings.SlippageStep, err = decimalOr(d.SlippageStep, defaultSlippageStep, "伪市价滑点"); err != nil {
		return Settings{}, err
	}
	if settings.SlippageMax, err = decimalOr(d.SlippageMax, defaultSlippageMax, "伪市价滑点上限"); err != nil {
		return Settings{}, err
	}
	half := shared.MustDecimal("0.5")
	if settings.SlippageStep.Cmp(shared.Zero()) <= 0 || settings.SlippageStep.Cmp(half) >= 0 {
		return Settings{}, fmt.Errorf("伪市价滑点必须大于 0 且小于 0.5")
	}
	if settings.SlippageMax.Cmp(settings.SlippageStep) < 0 || settings.SlippageMax.Cmp(half) >= 0 {
		return Settings{}, fmt.Errorf("伪市价滑点上限必须不小于滑点且小于 0.5")
	}
	switch {
	case d.MaxAttempts == 0:
		settings.MaxAttempts = defaultMaxAttempts
	case d.MaxAttempts < 0 || d.MaxAttempts > maxMaxAttempts:
		return Settings{}, fmt.Errorf("伪市价最多尝试次数必须在 1 到 %d 之间", maxMaxAttempts)
	default:
		settings.MaxAttempts = d.MaxAttempts
	}
	if settings.RestingRatio, err = nonNegative(d.RestingRatio, "0", "挂单比例"); err != nil {
		return Settings{}, err
	}
	if settings.RestingRatio.Cmp(shared.MustDecimal("1")) > 0 {
		return Settings{}, fmt.Errorf("挂单比例必须在 0 到 1 之间")
	}
	if d.RestingTTLSeconds < 0 || d.RestingTTLSeconds > maxRestingTTLSeconds {
		return Settings{}, fmt.Errorf("挂单寿命必须在 0 到 %d 秒之间（0 表示保留到下一份目标到达）", maxRestingTTLSeconds)
	}
	settings.RestingTTL = time.Duration(d.RestingTTLSeconds) * time.Second
	return settings, nil
}

// Canonical 返回写进数据库的规范 JSON：按固定顺序，标的键大写，只含写了的项。
func (d Document) Canonical() (string, error) {
	if _, err := d.Validate(); err != nil {
		return "", err
	}
	normalized := d
	normalized.HourlyCapSpot = upperKeys(d.HourlyCapSpot)
	normalized.HourlyCapSwap = upperKeys(d.HourlyCapSwap)
	// 小数规范化（"0.050" 存成 "0.05"），和资金设置一致。
	for _, field := range []*string{
		&normalized.RebalanceThresholdRatio, &normalized.MinNotionalSpot, &normalized.MinNotionalSwap,
		&normalized.MaxChildNotional, &normalized.SlippageStep, &normalized.SlippageMax, &normalized.RestingRatio,
	} {
		if value, err := shared.ParseDecimal(strings.TrimSpace(*field)); err == nil {
			*field = value.String()
		}
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// Document 返回有效设置的完整文档形态（默认值也写出来），给界面显示当前生效的值。
func (s Settings) Document() Document {
	document := Document{
		RebalanceThresholdMode:  string(s.ThresholdMode),
		RebalanceThresholdRatio: s.ThresholdRatio.String(),
		MinNotionalSpot:         s.MinNotionalSpot.String(),
		MinNotionalSwap:         s.MinNotionalSwap.String(),
		HourlyCapSpot:           capsDocument(s.HourlyCapSpot),
		HourlyCapSwap:           capsDocument(s.HourlyCapSwap),
		MaxChildNotional:        s.MaxChildNotional.String(),
		RandomizeSplit:          s.RandomizeSplit,
		OrderMode:               string(s.OrderMode),
		SlippageStep:            s.SlippageStep.String(),
		SlippageMax:             s.SlippageMax.String(),
		MaxAttempts:             s.MaxAttempts,
		RestingRatio:            s.RestingRatio.String(),
		RestingTTLSeconds:       int(s.RestingTTL / time.Second),
	}
	if s.BatchIntervalSet {
		document.BatchIntervalSeconds = int(s.BatchInterval / time.Second)
	}
	return document
}

// usesSpotItems 报告市场用设置里的“现货”项：持仓来自余额的市场（现货、A 股股票）；合约用“合约”项。
func usesSpotItems(market string) bool {
	return exchange.RuleFor(strings.ToUpper(market)).PositionFromBalance
}

// HourlyCap 返回标的的每小时累计下单金额上限：先找标的自己的设置，再找 "*"；没有设置返回 false（不限）。
// 现货和股票用现货项，合约用合约项。
func (s Settings) HourlyCap(market, instrumentID string) (shared.Decimal, bool) {
	caps := s.HourlyCapSwap
	if usesSpotItems(market) {
		caps = s.HourlyCapSpot
	}
	if limit, ok := caps[strings.ToUpper(instrumentID)]; ok {
		return limit, true
	}
	limit, ok := caps["*"]
	return limit, ok
}

// HasHourlyCaps 报告是否配置了任何每小时上限；没配置时调用方不必查订单表。
func (s Settings) HasHourlyCaps() bool {
	return len(s.HourlyCapSpot) > 0 || len(s.HourlyCapSwap) > 0
}

// MinNotional 返回市场的最小下单额配置值：现货和股票用现货项，合约用合约项。
func (s Settings) MinNotional(market string) shared.Decimal {
	if usesSpotItems(market) {
		return s.MinNotionalSpot
	}
	return s.MinNotionalSwap
}

// Slippage 返回第 attempt 次尝试（从 0 开始）的滑点：首次 s，每次重试再加 s，直到上限。
func (s Settings) Slippage(attempt int) shared.Decimal {
	slippage := s.SlippageStep.Mul(shared.MustDecimal(fmt.Sprint(attempt + 1)))
	if slippage.Cmp(s.SlippageMax) > 0 {
		return s.SlippageMax
	}
	return slippage
}

func decimalOr(raw, fallback, name string) (shared.Decimal, error) {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return shared.Zero(), fmt.Errorf("%s不是合法数字：%q", name, raw)
	}
	return value, nil
}

func nonNegative(raw, fallback, name string) (shared.Decimal, error) {
	value, err := decimalOr(raw, fallback, name)
	if err != nil {
		return shared.Zero(), err
	}
	if value.IsNegative() {
		return shared.Zero(), fmt.Errorf("%s不能为负", name)
	}
	return value, nil
}

// stockCode 是股票内部标的 ID 的格式：代码加交易所后缀，如 600519.XSHG、000001.XSHE、430047.XBSE。
var stockCode = regexp.MustCompile(`^[0-9A-Z]+\.[A-Z]+$`)

// hourlyCaps 校验每小时上限：键必须是 "*" 或本市场的内部标的 ID（大小写不限，统一转大写后不能重复），值大于 0。
// allowStock 为真时（现货那一组，股票也用它）还接受股票代码格式的键。
func hourlyCaps(raw map[string]string, market, label string, allowStock bool) (map[string]shared.Decimal, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	caps := make(map[string]shared.Decimal, len(raw))
	for key, value := range raw {
		normalized := strings.ToUpper(strings.TrimSpace(key))
		validKey := normalized == "*" || strings.HasSuffix(normalized, "-"+market) || allowStock && stockCode.MatchString(normalized)
		if !validKey {
			hint := "BTC-USDT-" + market
			if allowStock {
				hint += " 或 600519.XSHG"
			}
			return nil, fmt.Errorf("%s每小时上限的键 %q 必须是 * 或形如 %s 的内部标的 ID", label, key, hint)
		}
		if _, duplicated := caps[normalized]; duplicated {
			return nil, fmt.Errorf("%s每小时上限的键 %q 重复（标的 ID 不区分大小写）", label, key)
		}
		limit, err := shared.ParseDecimal(value)
		if err != nil || limit.Cmp(shared.Zero()) <= 0 {
			return nil, fmt.Errorf("%s每小时上限 %s 必须是大于 0 的数字", label, key)
		}
		caps[normalized] = limit
	}
	return caps, nil
}

func upperKeys(raw map[string]string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(raw))
	for key, value := range raw {
		decimal, _ := shared.ParseDecimal(value)
		normalized[strings.ToUpper(strings.TrimSpace(key))] = decimal.String()
	}
	return normalized
}

func capsDocument(caps map[string]shared.Decimal) map[string]string {
	if len(caps) == 0 {
		return nil
	}
	keys := make([]string, 0, len(caps))
	for key := range caps {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	document := make(map[string]string, len(caps))
	for _, key := range keys {
		document[key] = caps[key].String()
	}
	return document
}
