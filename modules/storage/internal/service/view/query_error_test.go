package view

import (
	"errors"
	"fmt"
	"os"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestQueryErrorCodeRetryable(t *testing.T) {
	if got := queryErrorCode(fmt.Errorf("view index %q is not prepared: %w", "x", errViewIndexNotReady)); got != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("wrapped not-ready => %v", got)
	}
	if got := queryErrorCode(os.ErrNotExist); got != pb.ErrorCode_VIEW_NOT_READY {
		t.Fatalf("ErrNotExist => %v", got)
	}
	if got := queryErrorCode(errors.New("disk full")); got != pb.ErrorCode_INNER_ERR {
		t.Fatalf("other => %v", got)
	}
}
