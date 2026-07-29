package shared

import (
	"errors"
	"math/big"
	"regexp"
	"strings"
)

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

var ErrInvalidDecimal = errors.New("trade: invalid decimal")

// Decimal is an exact base-10 value. Its zero value is numeric zero.
type Decimal struct{ v *big.Rat }

func ParseDecimal(raw string) (Decimal, error) {
	if !decimalPattern.MatchString(raw) {
		return Decimal{}, ErrInvalidDecimal
	}
	r := new(big.Rat)
	if _, ok := r.SetString(raw); !ok {
		return Decimal{}, ErrInvalidDecimal
	}
	return Decimal{v: r}, nil
}

func MustDecimal(raw string) Decimal {
	v, err := ParseDecimal(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func Zero() Decimal { return MustDecimal("0") }

func (d Decimal) rat() *big.Rat {
	if d.v == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(d.v)
}

func (d Decimal) Add(o Decimal) Decimal { return Decimal{v: new(big.Rat).Add(d.rat(), o.rat())} }
func (d Decimal) Sub(o Decimal) Decimal { return Decimal{v: new(big.Rat).Sub(d.rat(), o.rat())} }
func (d Decimal) Mul(o Decimal) Decimal { return Decimal{v: new(big.Rat).Mul(d.rat(), o.rat())} }
func (d Decimal) Div(o Decimal) Decimal { return Decimal{v: new(big.Rat).Quo(d.rat(), o.rat())} }
func (d Decimal) Neg() Decimal          { return Decimal{v: new(big.Rat).Neg(d.rat())} }
func (d Decimal) Abs() Decimal          { return Decimal{v: new(big.Rat).Abs(d.rat())} }
func (d Decimal) Cmp(o Decimal) int     { return d.rat().Cmp(o.rat()) }
func (d Decimal) IsZero() bool          { return d.Cmp(Zero()) == 0 }
func (d Decimal) IsNegative() bool      { return d.Cmp(Zero()) < 0 }
func (d Decimal) IsInteger() bool       { return d.rat().IsInt() }

func (d Decimal) String() string {
	r := d.rat()
	if r.IsInt() {
		return r.Num().String()
	}
	// Domain values are finite base-10 decimals by construction. Find the
	// shortest exact representation rather than emitting a rounded float.
	for scale := 1; scale <= 36; scale++ {
		s := r.FloatString(scale)
		p, err := ParseDecimal(s)
		if err == nil && p.Cmp(d) == 0 {
			return strings.TrimRight(strings.TrimRight(s, "0"), ".")
		}
	}
	return r.RatString()
}

func (d Decimal) Scale() int {
	s := d.String()
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return len(s) - i - 1
	}
	return 0
}

func (d Decimal) Validate(maxScale int, allowNegative bool) error {
	if (!allowNegative && d.IsNegative()) || d.Scale() > maxScale {
		return ErrInvalidDecimal
	}
	return nil
}
