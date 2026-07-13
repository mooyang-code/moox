package eventbuspb

import (
	"fmt"
	"strings"
)

func (r *GetConsumerReq) Validate() error {
	if r == nil || strings.TrimSpace(r.Stream) == "" || strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("stream and name are required")
	}
	return nil
}
