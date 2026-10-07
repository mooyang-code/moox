package strategypb

import (
	"fmt"
	"strings"
)

func required(value, name string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", name)
	}
	return nil
}

func (r *CreateStrategyReq) Validate() error {
	if r == nil || r.Strategy == nil {
		return fmt.Errorf("strategy is required")
	}
	return required(r.Strategy.StrategyId, "strategy_id")
}

func (r *GetStrategyReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return required(r.StrategyId, "strategy_id")
}

func (r *GetStrategyResultReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return required(r.ResultId, "result_id")
}

func (r *ListStrategyTargetsReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return required(r.InstanceId, "instance_id")
}
