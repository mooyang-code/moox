package service

import "testing"

func TestPageNormalizeAllowsSyncPageSize(t *testing.T) {
	got := (Page{PageNo: 1, PageSize: 500}).Normalize()
	if got.PageSize != 500 {
		t.Fatalf("PageSize=%d, want 500", got.PageSize)
	}
}
