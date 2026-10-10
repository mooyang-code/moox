package quant

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

const scaleDigits = 18

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

var ErrInvalidDecimal = errors.New("无效的定点数")

var scale = new(big.Int).Exp(big.NewInt(10), big.NewInt(scaleDigits), nil)

type Decimal struct {
	units *big.Int
}

// literalPattern 是 DSL 数字字面量允许的写法：可选符号、整数或小数（可省略整数部分）、可选指数（第 3 组）。
var literalPattern = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE]([+-]?\d+))?$`)

// DSL 数字字面量的量级限制：权重、杠杆等取值远小于这个范围；不加限制时 1e999999 会展开成百万位整数拖垮进程。
const (
	maxLiteralLength        = 64
	maxLiteralExponent      = 30
	maxLiteralIntegerDigits = 9
)

var maxLiteralUnits = new(big.Int).Mul(new(big.Int).Exp(big.NewInt(10), big.NewInt(maxLiteralIntegerDigits), nil), scale)

// ParseLiteral 解析 DSL 中的数字字面量：除 Parse 接受的写法外，还接受 YAML 合法的 .5、+0.5、2e-1 等；
// 精度不能超过定点小数位数，绝对值必须小于 10^9。
func ParseLiteral(raw string) (Decimal, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > maxLiteralLength {
		return Decimal{}, fmt.Errorf("%w：数字写法不能超过 %d 个字符", ErrInvalidDecimal, maxLiteralLength)
	}
	match := literalPattern.FindStringSubmatch(raw)
	if match == nil {
		return Decimal{}, ErrInvalidDecimal
	}
	if exponent := match[3]; exponent != "" {
		if value, err := strconv.Atoi(exponent); err != nil || value > maxLiteralExponent || value < -maxLiteralExponent {
			return Decimal{}, fmt.Errorf("%w：指数的绝对值不能超过 %d", ErrInvalidDecimal, maxLiteralExponent)
		}
	}
	value, ok := new(big.Rat).SetString(raw)
	if !ok {
		return Decimal{}, ErrInvalidDecimal
	}
	units := value.Mul(value, new(big.Rat).SetInt(scale))
	if !units.IsInt() {
		return Decimal{}, fmt.Errorf("%w：小数位数不能超过 %d 位", ErrInvalidDecimal, scaleDigits)
	}
	if new(big.Int).Abs(units.Num()).Cmp(maxLiteralUnits) >= 0 {
		return Decimal{}, fmt.Errorf("%w：绝对值必须小于 10^%d", ErrInvalidDecimal, maxLiteralIntegerDigits)
	}
	return Decimal{units: new(big.Int).Set(units.Num())}, nil
}

func Parse(raw string) (Decimal, error) {
	if !decimalPattern.MatchString(raw) {
		return Decimal{}, ErrInvalidDecimal
	}
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	parts := strings.SplitN(raw, ".", 2)
	whole := new(big.Int)
	if _, ok := whole.SetString(parts[0], 10); !ok {
		return Decimal{}, ErrInvalidDecimal
	}
	whole.Mul(whole, scale)
	if len(parts) == 2 {
		fraction := parts[1]
		if len(fraction) > scaleDigits {
			return Decimal{}, ErrInvalidDecimal
		}
		fraction += strings.Repeat("0", scaleDigits-len(fraction))
		part := new(big.Int)
		if _, ok := part.SetString(fraction, 10); !ok {
			return Decimal{}, ErrInvalidDecimal
		}
		whole.Add(whole, part)
	}
	if negative {
		whole.Neg(whole)
	}
	return Decimal{units: whole}, nil
}

func Must(raw string) Decimal {
	value, err := Parse(raw)
	if err != nil {
		panic(err)
	}
	return value
}

func Zero() Decimal { return Decimal{units: new(big.Int)} }
func One() Decimal  { return Decimal{units: new(big.Int).Set(scale)} }

func (d Decimal) normalized() *big.Int {
	if d.units == nil {
		return new(big.Int)
	}
	return new(big.Int).Set(d.units)
}

func (d Decimal) Add(other Decimal) Decimal {
	return Decimal{units: new(big.Int).Add(d.normalized(), other.normalized())}
}

func (d Decimal) Sub(other Decimal) Decimal {
	return Decimal{units: new(big.Int).Sub(d.normalized(), other.normalized())}
}

// Mul 计算两个定点数的乘积并截断回策略的小数位数；操作数都会被复制，调用方可以安全复用。
func (d Decimal) Mul(other Decimal) Decimal {
	product := new(big.Int).Mul(d.normalized(), other.normalized())
	product.Quo(product, scale)
	return Decimal{units: product}
}

// Div 计算两个定点数的商并向零截断；除数为 0 时返回 0，需要拒绝时由调用方先检查。
func (d Decimal) Div(other Decimal) Decimal {
	divisor := other.normalized()
	if divisor.Sign() == 0 {
		return Zero()
	}
	numerator := new(big.Int).Mul(d.normalized(), scale)
	numerator.Quo(numerator, divisor)
	return Decimal{units: numerator}
}

func (d Decimal) Neg() Decimal          { return Decimal{units: new(big.Int).Neg(d.normalized())} }
func (d Decimal) Cmp(other Decimal) int { return d.normalized().Cmp(other.normalized()) }
func (d Decimal) IsZero() bool          { return d.normalized().Sign() == 0 }
func (d Decimal) IsNegative() bool      { return d.normalized().Sign() < 0 }

func (d Decimal) String() string {
	units := d.normalized()
	if units.Sign() == 0 {
		return "0"
	}
	negative := units.Sign() < 0
	if negative {
		units.Neg(units)
	}
	whole := new(big.Int).Quo(units, scale)
	fraction := new(big.Int).Mod(units, scale).String()
	fraction = strings.Repeat("0", scaleDigits-len(fraction)) + fraction
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		if negative {
			return "-" + whole.String()
		}
		return whole.String()
	}
	result := whole.String() + "." + fraction
	if negative {
		return "-" + result
	}
	return result
}

// TruncateTo 向零截断到 places 位小数（0 到 scaleDigits）：绝对值只会变小。目标权重用它保存精度规范：截断不会让权重之和、
// 杠杆、单标的上限等约束从满足变成不满足，多出来的零头留作现金。
func (d Decimal) TruncateTo(places int) Decimal {
	step := d.placeStep(places)
	if step == nil {
		return d
	}
	units := d.normalized()
	return Decimal{units: new(big.Int).Mul(new(big.Int).Quo(units, step), step)}
}

// RoundTo 四舍五入（远离零）到 places 位小数，用于不参与约束的展示量（例如换手）。
func (d Decimal) RoundTo(places int) Decimal {
	step := d.placeStep(places)
	if step == nil {
		return d
	}
	units := d.normalized()
	half := new(big.Int).Rsh(step, 1)
	rounded := new(big.Int).Abs(units)
	rounded.Add(rounded, half)
	rounded.Quo(rounded, step)
	rounded.Mul(rounded, step)
	if units.Sign() < 0 {
		rounded.Neg(rounded)
	}
	return Decimal{units: rounded}
}

// placeStep 返回 places 位小数对应的最小单位（以内部定点单位计）；places 超出 0..scaleDigits 时返回 nil 表示不处理。
func (d Decimal) placeStep(places int) *big.Int {
	if places < 0 || places >= scaleDigits {
		return nil
	}
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scaleDigits-places)), nil)
}

func DivideStable(total Decimal, orderedKeys []string) map[string]Decimal {
	result := make(map[string]Decimal, len(orderedKeys))
	if len(orderedKeys) == 0 {
		return result
	}
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(total.normalized(), big.NewInt(int64(len(orderedKeys))), remainder)
	for i, key := range orderedKeys {
		units := new(big.Int).Set(quotient)
		if int64(i) < remainder.Int64() {
			units.Add(units, big.NewInt(1))
		}
		result[key] = Decimal{units: units}
	}
	return result
}
