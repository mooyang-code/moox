package primarystore

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type pagedMetadataReader struct {
	columns []*pb.DatasetColumn
}

func (r *pagedMetadataReader) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, nil
}

func (r *pagedMetadataReader) ListDatasetColumns(_ context.Context, _, _ string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	pageNo, size := uint32(1), uint32(1000)
	if page != nil {
		pageNo, size = page.GetPage(), page.GetSize()
	}
	start := int((pageNo - 1) * size)
	if start > len(r.columns) {
		start = len(r.columns)
	}
	end := start + int(size)
	if end > len(r.columns) {
		end = len(r.columns)
	}
	return r.columns[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(r.columns)), HasMore: end < len(r.columns)}, nil
}

func TestMetadataValidatorReadsAllDatasetColumnPages(t *testing.T) {
	columns := make([]*pb.DatasetColumn, 1001)
	for i := range columns {
		columns[i] = &pb.DatasetColumn{ColumnName: fmt.Sprintf("field_%04d", i), OriginId: fmt.Sprintf("field_%04d", i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"}
	}
	validator := NewMetadataValidator(&pagedMetadataReader{columns: columns})
	err := validator.ValidateRow(context.Background(), &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{
			FieldId: "field_1000",
			Value:   &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

type snapshotOnlyMetadataReader struct {
	snapshot metadata.RequestSnapshot
}

func (*snapshotOnlyMetadataReader) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	panic("persistent metadata store accessed")
}

func (*snapshotOnlyMetadataReader) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	panic("persistent metadata store accessed")
}

func (r *snapshotOnlyMetadataReader) RequestSnapshot() metadata.RequestSnapshot {
	return r.snapshot
}

type validatorSnapshot struct{}

func (validatorSnapshot) GetDataset(string, string) (*pb.Dataset, bool) {
	return &pb.Dataset{SpaceId: "space", DatasetId: "dataset", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, true
}

func (validatorSnapshot) GetDataNode(string) (*pb.DataNode, bool) {
	return &pb.DataNode{NodeId: "node-a", Status: "active", ServiceTarget: "ip://127.0.0.1:20107"}, true
}

func (validatorSnapshot) ListDatasetColumns(string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return []*pb.DatasetColumn{{ColumnName: "value", OriginId: "value", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"}}, &pb.PageResult{Page: 1, Size: 1000}, nil
}

func TestMetadataValidatorUsesRequestSnapshotWithoutStoreAccess(t *testing.T) {
	var writes, reads int
	node := &recordingNode{
		write: func(_ context.Context, req *pb.UpsertFieldsReq) (*pb.UpsertFieldsRsp, error) {
			writes++
			return &pb.UpsertFieldsRsp{RetInfo: successRetInfo(), Keys: []*pb.RowKey{req.GetRows()[0].GetKey()}}, nil
		},
		read: func(_ context.Context, req *pb.ReadFieldsReq) (*pb.ReadFieldsRsp, error) {
			reads++
			return &pb.ReadFieldsRsp{RetInfo: successRetInfo(), Rows: []*pb.RowFieldValues{{Key: req.GetKeys()[0]}}}, nil
		},
	}
	validator := NewMetadataValidator(&snapshotOnlyMetadataReader{snapshot: validatorSnapshot{}})
	svc, err := New(Options{Node: node, Validator: validator})
	if err != nil {
		t.Fatal(err)
	}
	key := &pb.RowKey{SpaceId: "space", DatasetId: "dataset", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "record", Version: "1"}}}
	write, err := svc.UpsertFields(context.Background(), &pb.PrimaryUpsertFieldsReq{
		AuthInfo: &pb.AuthInfo{AppId: "caller"},
		Rows:     []*pb.RowFieldUpsert{{Key: key, Fields: []*pb.FieldValue{{FieldId: "value", Value: &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}}}}}},
	})
	if err != nil || write.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || writes != 1 {
		t.Fatalf("write=%v err=%v writes=%d", write, err, writes)
	}
	read, err := svc.ReadFields(context.Background(), &pb.PrimaryReadFieldsReq{AuthInfo: &pb.AuthInfo{AppId: "caller"}, Keys: []*pb.RowKey{key}})
	if err != nil || read.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS || reads != 1 {
		t.Fatalf("read=%v err=%v reads=%d", read, err, reads)
	}
}
