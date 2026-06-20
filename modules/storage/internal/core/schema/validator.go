package schema

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type MetadataReader interface {
	GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error)
	ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error)
	ListDataSetSubjectsPage(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DataSetSubject, *pb.PageResult, error)
}

type Validator struct {
	metadata MetadataReader
}

func NewValidator(store MetadataReader) *Validator {
	return &Validator{metadata: store}
}

func (v *Validator) ValidateWriteRows(ctx context.Context, rows []*pb.DataRow) error {
	datasets := make(map[string]*pb.DataSet)
	columnsByDataset := make(map[string]map[string]*pb.DataSetColumn)
	subjectBindings := make(map[string]bool)
	for _, row := range rows {
		if row.GetKey() == nil || row.GetKey().GetScope() == nil {
			return fmt.Errorf("data key and scope are required")
		}
		scope := row.GetKey().GetScope()
		if scope.GetSpaceId() == "" || scope.GetDatasetId() == "" {
			return fmt.Errorf("space_id and dataset_id are required")
		}
		datasetKey := scope.GetSpaceId() + "|" + scope.GetDatasetId()
		dataset, ok := datasets[datasetKey]
		if !ok {
			var err error
			dataset, err = v.metadata.GetDataSet(ctx, scope.GetSpaceId(), scope.GetDatasetId())
			if err != nil {
				return err
			}
			datasets[datasetKey] = dataset
		}
		if dataset.GetStatus() != "" && dataset.GetStatus() != "active" {
			return fmt.Errorf("dataset %s is not active", scope.GetDatasetId())
		}
		if dataset.GetDataKind() != pb.DataKind_DATA_KIND_OBJECT {
			if scope.GetSubjectId() == "" {
				return fmt.Errorf("subject_id is required")
			}
		}
		if requiresSubjectBinding(dataset) {
			bindingKey := datasetKey + "|" + scope.GetSubjectId()
			bound, ok := subjectBindings[bindingKey]
			if !ok {
				bindings, _, err := v.metadata.ListDataSetSubjectsPage(ctx, scope.GetSpaceId(), scope.GetDatasetId(), scope.GetSubjectId(), &pb.Page{Page: 1, Size: 1})
				if err != nil {
					return err
				}
				for _, binding := range bindings {
					if binding.GetStatus() == "" || binding.GetStatus() == "active" {
						bound = true
						break
					}
				}
				subjectBindings[bindingKey] = bound
			}
			if !bound {
				return fmt.Errorf("subject %s is not bound to dataset %s", scope.GetSubjectId(), scope.GetDatasetId())
			}
		}
		allowed, ok := columnsByDataset[datasetKey]
		if !ok {
			columns, _, err := v.metadata.ListDataSetColumns(ctx, scope.GetSpaceId(), scope.GetDatasetId(), false, nil)
			if err != nil {
				return err
			}
			allowed = make(map[string]*pb.DataSetColumn, len(columns))
			for _, column := range columns {
				if column.GetStatus() == "" || column.GetStatus() == "active" {
					allowed[column.GetColumnName()] = column
				}
			}
			columnsByDataset[datasetKey] = allowed
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

func requiresSubjectBinding(dataset *pb.DataSet) bool {
	switch dataset.GetDataKind() {
	case pb.DataKind_DATA_KIND_OBJECT, pb.DataKind_DATA_KIND_TIME_SERIES:
		return false
	default:
		return true
	}
}
