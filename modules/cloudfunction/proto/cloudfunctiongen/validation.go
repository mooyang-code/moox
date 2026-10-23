package cloudnodepb

import (
	"fmt"
	"strings"
)

func (r *InvokeFunctionReq) Validate() error {
	if r == nil || strings.TrimSpace(r.NodeId) == "" {
		return fmt.Errorf("node_id is required")
	}
	return nil
}
