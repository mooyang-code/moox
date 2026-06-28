package response

import (
	"errors"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestMetadataStoreCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want pb.ErrorCode
	}{
		{name: "nil", err: nil, want: pb.ErrorCode_SUCCESS},
		{name: "required", err: errors.New("space_id is required"), want: pb.ErrorCode_INVALID_PARAM},
		{name: "must", err: errors.New("dataset_id must be lowercase snake_case"), want: pb.ErrorCode_INVALID_PARAM},
		{name: "chinese", err: errors.New("dataset name must contain Chinese characters"), want: pb.ErrorCode_INVALID_PARAM},
		{name: "db", err: errors.New("database is locked"), want: pb.ErrorCode_INNER_ERR},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetadataStoreCode(tt.err); got != tt.want {
				t.Fatalf("MetadataStoreCode() = %v, want %v", got, tt.want)
			}
		})
	}
}
