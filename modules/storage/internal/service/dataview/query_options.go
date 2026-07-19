//go:build legacy_storage

package view

import (
	"errors"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

const (
	DefaultTimeSeriesViewPageSize     uint32 = 25
	DefaultTimeSeriesViewPreviewLimit uint32 = 1000
	MaxTimeSeriesViewQueryLimit       uint32 = 5000
)

func ValidateTimeSeriesQueryOptions(req *pb.QueryTimeSeriesRowsReq) error {
	if req == nil {
		return errors.New("query request is required")
	}
	if pageHasCursor(req.GetPage()) {
		return errors.New("page cursor is not supported for time series view queries")
	}
	limit := req.GetLimit()
	if limit == 0 {
		return nil
	}
	if limit > MaxTimeSeriesViewQueryLimit {
		return fmt.Errorf("limit must be <= %d", MaxTimeSeriesViewQueryLimit)
	}
	return nil
}

func pageHasCursor(page *pb.Page) bool {
	if page == nil {
		return false
	}
	return page.GetCursor() != ""
}
