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

func (r *CreateRunnerReq) Validate() error {
	if r == nil || r.Runner == nil {
		return fmt.Errorf("runner is required")
	}
	return required(r.Runner.RunnerId, "runner_id")
}

func (r *GetRunnerReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	return required(r.RunnerId, "runner_id")
}

func (r *UpdateRunnerReq) Validate() error {
	if r == nil || r.Runner == nil {
		return fmt.Errorf("runner is required")
	}
	return required(r.Runner.RunnerId, "runner_id")
}

func (r *SetRunnerStatusReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if err := required(r.RunnerId, "runner_id"); err != nil {
		return err
	}
	return required(r.Status, "status")
}

func (r *RunOnceReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if err := required(r.RunnerId, "runner_id"); err != nil {
		return err
	}
	if err := required(r.TriggerBarTime, "trigger_bar_time"); err != nil {
		return err
	}
	return required(r.DataJson, "data_json")
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
	return required(r.RunnerId, "runner_id")
}
