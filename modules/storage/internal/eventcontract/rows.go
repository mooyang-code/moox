package eventcontract

import (
	"fmt"

	localpb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	sharedpb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ToSharedRows converts the Storage module's RPC write model to the public
// event model at the publishing boundary. JSON is used only as a deliberate
// schema boundary because the two protobuf packages have different ownership
// and full names but intentionally share field semantics.
func ToSharedRows(in *localpb.RowsUpserted) (*sharedpb.DatasetRowsUpserted, error) {
	if in == nil {
		return nil, fmt.Errorf("storage rows event is nil")
	}
	if in.GetSpaceId() == "" || in.GetDatasetId() == "" || len(in.GetRows()) == 0 {
		return nil, fmt.Errorf("storage rows event identity and rows are required")
	}
	for i, row := range in.GetRows() {
		if row == nil || row.GetKey() == nil {
			return nil, fmt.Errorf("storage row %d key is required", i)
		}
		if row.GetKey().GetSpaceId() != in.GetSpaceId() || row.GetKey().GetDatasetId() != in.GetDatasetId() {
			return nil, fmt.Errorf("storage row %d identity mismatch", i)
		}
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal local rows event: %w", err)
	}
	out := new(sharedpb.DatasetRowsUpserted)
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("convert local rows event: %w", err)
	}
	return out, nil
}

// ToLocalRows converts a public Dataset delta back to the Storage module's
// internal write model at the consumption boundary.
func ToLocalRows(in *sharedpb.DatasetRowsUpserted) (*localpb.RowsUpserted, error) {
	if in == nil {
		return nil, fmt.Errorf("shared rows event is nil")
	}
	if in.GetSpaceId() == "" || in.GetDatasetId() == "" || len(in.GetRows()) == 0 {
		return nil, fmt.Errorf("shared rows event identity and rows are required")
	}
	for i, row := range in.GetRows() {
		if row == nil || row.GetKey() == nil {
			return nil, fmt.Errorf("shared row %d key is required", i)
		}
		if row.GetKey().GetSpaceId() != in.GetSpaceId() || row.GetKey().GetDatasetId() != in.GetDatasetId() {
			return nil, fmt.Errorf("shared row %d identity mismatch", i)
		}
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("marshal shared rows event: %w", err)
	}
	out := new(localpb.RowsUpserted)
	if err := protojson.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("convert shared rows event: %w", err)
	}
	for _, row := range out.GetRows() {
		if row != nil && row.GetOperation() == localpb.RowFieldOperation_ROW_FIELD_OPERATION_UNSPECIFIED {
			row.Operation = localpb.RowFieldOperation_ROW_FIELD_OPERATION_UPSERT
		}
	}
	return out, nil
}

var _ proto.Message = (*sharedpb.DatasetRowsUpserted)(nil)
