package collectorpb

import "fmt"

func (r *CreateTaskRuleReq) Validate() error {
	if r == nil || r.Rule == nil {
		return fmt.Errorf("rule is required")
	}
	return nil
}
