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

func (r *UpsertFieldsReq) Validate() error {
	if r == nil || len(r.Rows) == 0 {
		return fmt.Errorf("rows must not be empty")
	}
	return nil
}

func (r *ReadRowsReq) Validate() error {
	if r == nil || len(r.Keys) == 0 || (len(r.ColumnNames) == 0 && len(r.AttributeKeys) == 0) {
		return fmt.Errorf("keys and column_names must not be empty")
	}
	return nil
}
