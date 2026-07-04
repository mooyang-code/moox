package dao

import "testing"

func TestNotDeletedUsesBoolSoftDelete(t *testing.T) {
	if got := notDeleted(); got != "c_is_deleted = 0" {
		t.Fatalf("notDeleted() = %q, want bool soft-delete predicate", got)
	}
}
