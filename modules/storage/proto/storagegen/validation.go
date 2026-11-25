package storagepb

import (
	"fmt"
	"strings"
)

func (r *QueryTimeSeriesRowsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.SpaceId) == "" || strings.TrimSpace(r.DatasetId) == "" {
		return fmt.Errorf("space_id 与 dataset_id 必填")
	}
	return nil
}

func (r *SearchRecordRowsReq) Validate() error {
	if r == nil || strings.TrimSpace(r.SpaceId) == "" || strings.TrimSpace(r.DatasetId) == "" {
		return fmt.Errorf("space_id 与 dataset_id 必填")
	}
	return nil
}
