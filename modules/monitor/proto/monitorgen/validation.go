package monitorpb

import "fmt"

func (r *CreateCheckReq) Validate() error {
	if r == nil || r.Check == nil {
		return fmt.Errorf("check is required")
	}
	return nil
}
