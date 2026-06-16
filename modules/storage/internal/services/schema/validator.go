package schema

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type MetadataReader interface {
	GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error)
	ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error)
}

type Validator struct {
	metadata MetadataReader
}

func NewValidator(store MetadataReader) *Validator {
	return &Validator{metadata: store}
}

func (v *Validator) ValidateWriteRows(ctx context.Context, rows []*pb.DataRow) error {
	for _, row := range rows {
		if row.GetKey() == nil || row.GetKey().GetScope() == nil {
			return fmt.Errorf("data key and scope are required")
		}
		scope := row.GetKey().GetScope()
		if scope.GetSpaceId() == "" || scope.GetDatasetId() == "" {
			return fmt.Errorf("space_id and dataset_id are required")
		}
		dataset, err := v.metadata.GetDataSet(ctx, scope.GetSpaceId(), scope.GetDatasetId())
		if err != nil {
			return err
		}
		if dataset.GetStatus() != "" && dataset.GetStatus() != "active" {
			return fmt.Errorf("dataset %s is not active", scope.GetDatasetId())
		}
		columns, _, err := v.metadata.ListDataSetColumns(ctx, scope.GetSpaceId(), scope.GetDatasetId(), false, nil)
		if err != nil {
			return err
		}
		allowed := make(map[string]*pb.DataSetColumn, len(columns))
		for _, column := range columns {
			if column.GetStatus() == "" || column.GetStatus() == "active" {
				allowed[column.GetColumnName()] = column
			}
		}
		for _, value := range row.GetColumns() {
			column := allowed[value.GetColumnName()]
			if column == nil {
				return fmt.Errorf("column %s is not registered in dataset %s", value.GetColumnName(), scope.GetDatasetId())
			}
			if value.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && column.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && value.GetValueType() != column.GetValueType() {
				return fmt.Errorf("column %s type mismatch: got %s want %s", value.GetColumnName(), value.GetValueType().String(), column.GetValueType().String())
			}
		}
	}
	return nil
}
