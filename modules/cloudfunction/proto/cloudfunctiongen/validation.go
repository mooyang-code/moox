package cloudfunctionpb

import (
	"fmt"
	"strings"
)

func (r *InvokeFunctionReq) Validate() error {
	if r == nil || strings.TrimSpace(r.InstanceId) == "" {
		return fmt.Errorf("instance_id is required")
	}
	return nil
}
