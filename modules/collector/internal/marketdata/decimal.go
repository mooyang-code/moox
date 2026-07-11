package marketdata

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

var decimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

// Decimal stores a finite base-10 number without going through a binary float.
// A nil rational is the zero value and is intentionally equivalent to zero.
type Decimal struct{ rat *big.Rat }

func ParseDecimal(input string) (Decimal, error) {
	if !decimalPattern.MatchString(input) {
		return Decimal{}, fmt.Errorf("invalid decimal %q", input)
	}
	sign := ""
	digits := input
	if strings.HasPrefix(digits, "-") {
		sign, digits = "-", digits[1:]
	}
	parts := strings.SplitN(digits, ".", 2)
	scale := 0
	if len(parts) == 2 {
		scale = len(parts[1])
	}
	integer := parts[0]
	if len(parts) == 2 {
		integer += parts[1]
	}
	numerator := new(big.Int)
	if _, ok := numerator.SetString(sign+integer, 10); !ok {
		return Decimal{}, fmt.Errorf("invalid decimal %q", input)
	}
	if numerator.Sign() == 0 {
		return Decimal{}, nil
	}
	denominator := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil)
	return Decimal{rat: new(big.Rat).SetFrac(numerator, denominator)}, nil
}

func MustDecimal(input string) Decimal {
	d, err := ParseDecimal(input)
	if err != nil {
		panic(err)
	}
	return d
}

func (d Decimal) String() string {
	if d.rat == nil || d.rat.Sign() == 0 {
		return "0"
	}
	scale := decimalScale(d.rat.Denom())
	value := d.rat.FloatString(scale)
	if strings.Contains(value, ".") {
		value = strings.TrimRight(value, "0")
		value = strings.TrimRight(value, ".")
	}
	return value
}

func (d Decimal) Cmp(other Decimal) int {
	return d.rational().Cmp(other.rational())
}

func (d Decimal) IsNegative() bool { return d.rational().Sign() < 0 }

// Validate enforces the field-level scale and sign policy. A negative maxScale
// disables the scale limit.
func (d Decimal) Validate(maxScale int, allowNegative bool) error {
	if d.IsNegative() && !allowNegative {
		return errors.New("negative decimal is not allowed")
	}
	if maxScale >= 0 && decimalScale(d.rational().Denom()) > maxScale {
		return fmt.Errorf("decimal scale exceeds %d", maxScale)
	}
	return nil
}

func (d Decimal) rational() *big.Rat {
	if d.rat == nil {
		return new(big.Rat)
	}
	return d.rat
}

func decimalScale(denominator *big.Int) int {
	if denominator.Sign() < 0 {
		denominator = new(big.Int).Neg(denominator)
	}
	if denominator.Cmp(big.NewInt(1)) == 0 {
		return 0
	}
	remaining := new(big.Int).Set(denominator)
	scale := 0
	for remaining.Cmp(big.NewInt(1)) != 0 {
		divided := false
		for _, factor := range []int64{2, 5} {
			q, r := new(big.Int).QuoRem(remaining, big.NewInt(factor), new(big.Int))
			if r.Sign() == 0 {
				remaining = q
				scale++
				divided = true
				break
			}
		}
		if !divided {
			return scale + 1
		}
	}
	return scale
}
