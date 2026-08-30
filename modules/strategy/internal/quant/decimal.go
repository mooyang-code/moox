package quant

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
)

const scaleDigits = 18

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

var ErrInvalidDecimal = errors.New("strategy: invalid decimal")

var scale = new(big.Int).Exp(big.NewInt(10), big.NewInt(scaleDigits), nil)

type Decimal struct {
	units *big.Int
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

// Mul multiplies two fixed-scale decimals and truncates the result back to the
// strategy scale. All operands are copied, so callers can safely reuse them.
func (d Decimal) Mul(other Decimal) Decimal {
	product := new(big.Int).Mul(d.normalized(), other.normalized())
	product.Quo(product, scale)
	return Decimal{units: product}
}

// Div divides two fixed-scale decimals and truncates toward zero. A zero
// divisor returns zero; callers that need to reject it should check first.
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

func NormalizeStable(values []Decimal) ([]Decimal, error) {
	if len(values) == 0 {
		return nil, ErrInvalidDecimal
	}
	total := new(big.Int)
	for _, value := range values {
		if value.IsNegative() {
			return nil, ErrInvalidDecimal
		}
		total.Add(total, value.normalized())
	}
	if total.Sign() <= 0 {
		return nil, ErrInvalidDecimal
	}
	result := make([]Decimal, len(values))
	remainders := make([]*big.Int, len(values))
	allocated := new(big.Int)
	for i, value := range values {
		numerator := new(big.Int).Mul(value.normalized(), scale)
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(numerator, total, remainder)
		result[i] = Decimal{units: quotient}
		remainders[i] = remainder
		allocated.Add(allocated, quotient)
	}
	remaining := new(big.Int).Sub(scale, allocated).Int64()
	for remaining > 0 {
		best := 0
		for i := 1; i < len(remainders); i++ {
			if remainders[i].Cmp(remainders[best]) > 0 {
				best = i
			}
		}
		result[best].units.Add(result[best].units, big.NewInt(1))
		remainders[best].SetInt64(-1)
		remaining--
	}
	return result, nil
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
