package strategypb

import (
	"fmt"
	"strings"
)

func (r *CreateStrategyReq) Validate() error {
	if r == nil || r.Strategy == nil {
		return fmt.Errorf("strategy is required")
	}
	return nil
}

func (r *RunOnceReq) Validate() error {
	if r == nil || strings.TrimSpace(r.BindingId) == "" {
		return fmt.Errorf("binding_id is required")
	}
	return nil
}
