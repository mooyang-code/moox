package retinfo

import (
	"database/sql"
	"fmt"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestMetadataStoreCodeMapsMissingRowsToNotFound(t *testing.T) {
	err := fmt.Errorf("metadata row not found: %w", sql.ErrNoRows)

	if got := MetadataStoreCode(err); got != pb.ErrorCode_NOT_FOUND {
		t.Fatalf("MetadataStoreCode() = %v, want %v", got, pb.ErrorCode_NOT_FOUND)
	}
}
