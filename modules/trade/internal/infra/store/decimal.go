package store

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

type decimalSign uint8

const (
	decimalSigned decimalSign = iota
	decimalNonNegative
	decimalPositive
)

func canonicalDecimal(raw string, label string, sign decimalSign) (string, error) {
	value, err := shared.ParseDecimal(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidRecord, label)
	}
	switch sign {
	case decimalNonNegative:
		if value.IsNegative() {
			return "", fmt.Errorf("%w: negative %s", ErrInvalidRecord, label)
		}
	case decimalPositive:
		if value.Cmp(shared.Zero()) <= 0 {
			return "", fmt.Errorf("%w: non-positive %s", ErrInvalidRecord, label)
		}
	}
	return value.String(), nil
}

func canonicalDefaultZero(raw string, label string, sign decimalSign) (string, error) {
	if raw == "" {
		raw = "0"
	}
	return canonicalDecimal(raw, label, sign)
}

func canonicalFiniteDecimal(value shared.Decimal, label string) (string, error) {
	raw := value.String()
	roundTrip, err := shared.ParseDecimal(raw)
	if err != nil || roundTrip.Cmp(value) != 0 {
		return "", fmt.Errorf("%w: non-finite %s", ErrInvalidRecord, label)
	}
	return raw, nil
}
