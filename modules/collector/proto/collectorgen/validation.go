package collectorpb

import "fmt"

func (r *CreateTaskReq) Validate() error {
	if r == nil || r.Task == nil {
		return fmt.Errorf("task is required")
	}
	return nil
}
