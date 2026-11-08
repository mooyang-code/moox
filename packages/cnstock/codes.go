// Package cnstock 是 A 股公共行情的抓取与解析：腾讯实时快照、东方财富标的列表，以及代码与标的 ID 的互转。
// 只依赖标准库，Collector 和 Trade 共用。
package cnstock

import (
	"errors"
	"fmt"
	"strings"
)

// 交易所后缀，与采集、策略使用的标的 ID 一致（600519.XSHG）。
const (
	ExchangeShanghai = "XSHG"
	ExchangeShenzhen = "XSHE"
	ExchangeBeijing  = "XBSE"
)

// ErrInvalidCode 表示证券代码或标的 ID 无法识别。
var ErrInvalidCode = errors.New("cnstock: 无法识别的证券代码")

// InstrumentID 按代码首位推出标的 ID：6 开头是上交所，0、2、3 开头是深交所，4、8、9 开头是北交所。
func InstrumentID(code string) (string, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return "", fmt.Errorf("%w：%q 不是六位数字", ErrInvalidCode, code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%w：%q 不是六位数字", ErrInvalidCode, code)
		}
	}
	switch code[0] {
	case '6':
		return code + "." + ExchangeShanghai, nil
	case '0', '2', '3':
		return code + "." + ExchangeShenzhen, nil
	case '4', '8', '9':
		return code + "." + ExchangeBeijing, nil
	default:
		return "", fmt.Errorf("%w：%q 不属于沪深北三所", ErrInvalidCode, code)
	}
}

// SplitInstrumentID 把 600519.XSHG 拆成代码和交易所，并核对代码与交易所对得上。
func SplitInstrumentID(instrumentID string) (code, exchange string, err error) {
	code, exchange, ok := strings.Cut(strings.ToUpper(strings.TrimSpace(instrumentID)), ".")
	if !ok {
		return "", "", fmt.Errorf("%w：标的 ID %q 缺少交易所后缀", ErrInvalidCode, instrumentID)
	}
	expected, err := InstrumentID(code)
	if err != nil {
		return "", "", err
	}
	if expected != code+"."+exchange {
		return "", "", fmt.Errorf("%w：代码 %s 不属于交易所 %s", ErrInvalidCode, code, exchange)
	}
	return code, exchange, nil
}

// TencentSymbol 把标的 ID 换成腾讯行情代码：600519.XSHG → sh600519，000001.XSHE → sz000001，北交所是 bj。
func TencentSymbol(instrumentID string) (string, error) {
	code, exchange, err := SplitInstrumentID(instrumentID)
	if err != nil {
		return "", err
	}
	switch exchange {
	case ExchangeShanghai:
		return "sh" + code, nil
	case ExchangeShenzhen:
		return "sz" + code, nil
	default:
		return "bj" + code, nil
	}
}

// FromTencentSymbol 是 TencentSymbol 的反向：sh600519 → 600519.XSHG。
func FromTencentSymbol(symbol string) (string, error) {
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	if len(symbol) != 8 {
		return "", fmt.Errorf("%w：腾讯代码 %q", ErrInvalidCode, symbol)
	}
	exchange := map[string]string{"sh": ExchangeShanghai, "sz": ExchangeShenzhen, "bj": ExchangeBeijing}[symbol[:2]]
	if exchange == "" {
		return "", fmt.Errorf("%w：腾讯代码 %q 的前缀不对", ErrInvalidCode, symbol)
	}
	instrumentID, err := InstrumentID(symbol[2:])
	if err != nil {
		return "", err
	}
	if instrumentID != symbol[2:]+"."+exchange {
		return "", fmt.Errorf("%w：腾讯代码 %q 的前缀与代码不符", ErrInvalidCode, symbol)
	}
	return instrumentID, nil
}

// Board 是板块，决定整手规则。
type Board string

const (
	BoardMain    Board = "MAIN"    // 沪深主板
	BoardChiNext Board = "CHINEXT" // 创业板（300、301）
	BoardSTAR    Board = "STAR"    // 科创板（688、689）
	BoardBeijing Board = "BEIJING" // 北交所
)

// BoardOf 按代码前缀判断板块。
func BoardOf(instrumentID string) (Board, error) {
	code, exchange, err := SplitInstrumentID(instrumentID)
	if err != nil {
		return "", err
	}
	switch {
	case exchange == ExchangeBeijing:
		return BoardBeijing, nil
	case strings.HasPrefix(code, "688") || strings.HasPrefix(code, "689"):
		return BoardSTAR, nil
	case strings.HasPrefix(code, "300") || strings.HasPrefix(code, "301"):
		return BoardChiNext, nil
	default:
		return BoardMain, nil
	}
}

// LotRule 是一个板块的下单数量规则（股）：买入数量不少于 Minimum 且是 Step 的整数倍。
type LotRule struct {
	Minimum int64
	Step    int64
}

// LotRuleFor 返回板块的数量规则：科创板最少 200 股、按 1 股递增；北交所最少 100 股、按 1 股递增；其余按 100 股一手。
func LotRuleFor(board Board) LotRule {
	switch board {
	case BoardSTAR:
		return LotRule{Minimum: 200, Step: 1}
	case BoardBeijing:
		return LotRule{Minimum: 100, Step: 1}
	default:
		return LotRule{Minimum: 100, Step: 100}
	}
}
