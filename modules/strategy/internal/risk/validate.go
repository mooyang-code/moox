package risk

import (
	"fmt"
	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"math/big"
)

type Policy struct {
	MaxGross  string
	MaxNet    string
	MaxSingle string
}

func Validate(targets []domain.TargetWeight, p Policy) error {
	gross := new(big.Rat)
	net := new(big.Rat)
	for _, t := range targets {
		v, ok := new(big.Rat).SetString(t.TargetWeight)
		if !ok {
			return fmt.Errorf("invalid target weight %q", t.TargetWeight)
		}
		abs := new(big.Rat).Abs(v)
		gross.Add(gross, abs)
		net.Add(net, v)
		if p.MaxSingle != "" {
			m, _ := new(big.Rat).SetString(p.MaxSingle)
			if abs.Cmp(m) > 0 {
				return fmt.Errorf("single target exceeds limit")
			}
		}
	}
	if p.MaxGross != "" {
		m, _ := new(big.Rat).SetString(p.MaxGross)
		if gross.Cmp(m) > 0 {
			return fmt.Errorf("gross exposure exceeds limit")
		}
	}
	if p.MaxNet != "" {
		m, _ := new(big.Rat).SetString(p.MaxNet)
		if new(big.Rat).Abs(net).Cmp(m) > 0 {
			return fmt.Errorf("net exposure exceeds limit")
		}
	}
	return nil
}
