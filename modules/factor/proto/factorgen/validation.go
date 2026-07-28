package factorpb

import "fmt"

func (r *CreateFactorReq) Validate() error {
	if r == nil || r.Factor == nil {
		return fmt.Errorf("factor is required")
	}
	return nil
}

func (r *DeleteFactorReq) Validate() error {
	if r == nil || r.FactorId == "" {
		return fmt.Errorf("factor_id is required")
	}
	return nil
}
