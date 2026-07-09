package view

import (
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestValidateTimeSeriesQueryOptionsAllowsPreviewLimit(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{Limit: 1000})
	if err != nil {
		t.Fatalf("ValidateTimeSeriesQueryOptions returned %v, want nil", err)
	}
}

func TestValidateTimeSeriesQueryOptionsAllowsLimitWithPageSize(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{
		Limit: 1000,
		Page:  &pb.Page{Page: 1, Size: 25},
	})
	if err != nil {
		t.Fatalf("ValidateTimeSeriesQueryOptions returned %v, want nil", err)
	}
}

func TestValidateTimeSeriesQueryOptionsAllowsLimitWithEmptyPage(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{
		Limit: 1000,
		Page:  &pb.Page{},
	})
	if err != nil {
		t.Fatalf("ValidateTimeSeriesQueryOptions returned %v, want nil", err)
	}
}

func TestValidateTimeSeriesQueryOptionsRejectsOversizedLimit(t *testing.T) {
	err := ValidateTimeSeriesQueryOptions(&pb.QueryTimeSeriesRowsReq{Limit: MaxTimeSeriesViewQueryLimit + 1})
	if err == nil || !strings.Contains(err.Error(), "limit must be <= 5000") {
		t.Fatalf("error = %v, want max limit error", err)
	}
}
