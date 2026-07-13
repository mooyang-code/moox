package storagepb

import (
	"fmt"
	"strings"
)

func (r *QueryTimeSeriesRowsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.SpaceId) == "" || strings.TrimSpace(r.ViewId) == "" {
		return fmt.Errorf("space_id and view_id are required")
	}
	return nil
}

func (r *SearchRecordRowsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.SpaceId) == "" || strings.TrimSpace(r.ViewId) == "" {
		return fmt.Errorf("space_id and view_id are required")
	}
	return nil
}

func (r *WriteTimeSeriesRowsReq) Validate() error {
	if r == nil || len(r.Rows) == 0 {
		return fmt.Errorf("rows are required")
	}
	return nil
}

func (r *WriteRecordRowsReq) Validate() error {
	if r == nil || len(r.Rows) == 0 {
		return fmt.Errorf("rows are required")
	}
	return nil
}
