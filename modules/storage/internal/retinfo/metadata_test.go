package retinfo

import (
	"database/sql"
	"fmt"
	"testing"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func TestMetadataStoreCodeMapsMissingRowsToNotFound(t *testing.T) {
	err := fmt.Errorf("metadata row not found: %w", sql.ErrNoRows)

	if got := MetadataStoreCode(err); got != pb.ErrorCode_NOT_FOUND {
		t.Fatalf("MetadataStoreCode() = %v, want %v", got, pb.ErrorCode_NOT_FOUND)
	}
}

func TestMetadataStoreCodeMapsViewIndexConflict(t *testing.T) {
	if got := MetadataStoreCode(metadatastore.ErrViewIndexConflict); got != pb.ErrorCode_CONFLICT {
		t.Fatalf("MetadataStoreCode() = %v, want %v", got, pb.ErrorCode_CONFLICT)
	}
}

func TestMetadataStoreCodeMapsDatasetLifecycleErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want pb.ErrorCode
	}{
		{name: "revision conflict", err: fmt.Errorf("dataset revision conflict"), want: pb.ErrorCode_CONFLICT},
		{name: "must be disabled", err: fmt.Errorf("dataset must be disabled"), want: pb.ErrorCode_INVALID_PARAM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetadataStoreCode(tt.err); got != tt.want {
				t.Fatalf("MetadataStoreCode() = %v, want %v", got, tt.want)
			}
		})
	}
}
