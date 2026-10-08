package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func (s *Service) ensureFactorResultDefaultView(ctx context.Context, dataset *pb.Dataset) error {
	if dataset == nil || strings.TrimSpace(dataset.GetAttributes()["dataset_role"]) != "factor_result" {
		return nil
	}
	if err := validateFactorResultDatasetContract(dataset); err != nil {
		return err
	}
	viewID, err := defaultFactorResultViewID(dataset.GetDatasetId())
	if err != nil {
		return err
	}
	columns, err := s.factorResultViewColumns(ctx, dataset, viewID)
	if err != nil {
		return err
	}
	expected, err := s.factorResultDefaultView(ctx, dataset, viewID, columns)
	if err != nil {
		return err
	}
	existing, err := s.metadata.GetView(ctx, dataset.GetSpaceId(), viewID)
	if err == nil {
		return validateFactorResultDefaultView(existing, expected)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read factor result default View: %w", err)
	}
	if _, err := s.metadata.CreateView(ctx, expected); err != nil {
		if errors.Is(err, metadatastore.ErrViewExists) {
			existing, readErr := s.metadata.GetView(ctx, dataset.GetSpaceId(), viewID)
			if readErr == nil {
				return validateFactorResultDefaultView(existing, expected)
			}
			if readErr != nil {
				return errors.Join(err, readErr)
			}
			return fmt.Errorf("default View %s/%s belongs to another Dataset", dataset.GetSpaceId(), viewID)
		}
		return fmt.Errorf("create factor result default View: %w", err)
	}
	return nil
}

func validateFactorResultDatasetContract(dataset *pb.Dataset) error {
	if dataset == nil || strings.TrimSpace(dataset.GetAttributes()["dataset_role"]) != "factor_result" {
		return nil
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES || len(dataset.GetFreqs()) != 1 || strings.TrimSpace(dataset.GetFreqs()[0]) == "" {
		return errors.New("factor_result Dataset must be a time series with exactly one non-empty frequency")
	}
	return nil
}

func (s *Service) factorResultDefaultView(ctx context.Context, dataset *pb.Dataset, viewID string, columns []*pb.ViewColumn) (*pb.View, error) {
	frequency := strings.TrimSpace(dataset.GetFreqs()[0])
	view := &pb.View{
		SpaceId: dataset.GetSpaceId(), ViewId: viewID, Name: dataset.GetName(), DatasetId: dataset.GetDatasetId(),
		GrainKeys: defaultViewGrainKeys(dataset.GetDataKind()), FilterJson: fmt.Sprintf(`{"freq":%q}`, frequency),
		Engine: defaultViewEngine(dataset.GetDataKind()), KeepDuration: dataset.GetKeepDuration(), Status: "active", Columns: columns,
		Attributes: map[string]string{
			"owner_module": "factor", "view_role": "factor_result", "managed_by": "storage",
			"primary_dataset_role": "factor_result",
		},
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return nil, fmt.Errorf("validate factor result default View: %w", err)
	}
	return view, nil
}

func validateFactorResultDefaultView(existing, expected *pb.View) error {
	if existing == nil || expected == nil {
		return errors.New("factor result default View metadata is required")
	}
	if existing.GetSpaceId() != expected.GetSpaceId() || existing.GetViewId() != expected.GetViewId() ||
		existing.GetDatasetId() != expected.GetDatasetId() ||
		existing.GetEngine() != expected.GetEngine() || existing.GetStatus() != "active" ||
		existing.GetKeepDuration() != expected.GetKeepDuration() ||
		!slicesEqual(existing.GetGrainKeys(), expected.GetGrainKeys()) ||
		!factorResultFrequencyMatches(existing.GetFilterJson(), expected.GetFilterJson()) ||
		!proto.Equal(&pb.View{Columns: existing.GetColumns()}, &pb.View{Columns: expected.GetColumns()}) {
		return fmt.Errorf("default factor result View %s/%s conflicts with the managed contract", expected.GetSpaceId(), expected.GetViewId())
	}
	for key, value := range expected.GetAttributes() {
		if existing.GetAttributes()[key] != value {
			return fmt.Errorf("default factor result View %s/%s conflicts with managed attribute %q", expected.GetSpaceId(), expected.GetViewId(), key)
		}
	}
	return nil
}

func factorResultFrequencyMatches(actualFilter, expectedFilter string) bool {
	var actual, expected map[string]json.RawMessage
	if json.Unmarshal([]byte(actualFilter), &actual) != nil || json.Unmarshal([]byte(expectedFilter), &expected) != nil || len(actual) != 1 || len(expected) != 1 {
		return false
	}
	var actualFrequency, expectedFrequency string
	if json.Unmarshal(actual["freq"], &actualFrequency) != nil || json.Unmarshal(expected["freq"], &expectedFrequency) != nil {
		return false
	}
	return strings.TrimSpace(actualFrequency) == strings.TrimSpace(expectedFrequency)
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Service) factorResultViewColumns(ctx context.Context, dataset *pb.Dataset, viewID string) ([]*pb.ViewColumn, error) {
	columns := make([]*pb.ViewColumn, 0)
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := s.metadata.ListDatasetColumns(ctx, dataset.GetSpaceId(), dataset.GetDatasetId(), &pb.Page{Page: pageNo, Size: 1000})
		if err != nil {
			return nil, fmt.Errorf("list factor result Dataset columns: %w", err)
		}
		for _, item := range items {
			if item == nil || (item.GetStatus() != "" && item.GetStatus() != "active") {
				continue
			}
			columns = append(columns, &pb.ViewColumn{
				SpaceId: dataset.GetSpaceId(), ViewId: viewID, ColumnName: item.GetColumnName(),
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   item.GetColumnName(), ValueType: item.GetValueType(),
				SortOrder: uint32(len(columns)), Attributes: cloneStringMap(item.GetAttributes()),
			})
		}
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			return columns, nil
		}
	}
}

func defaultFactorResultViewID(datasetID string) (string, error) {
	datasetID = strings.TrimSpace(datasetID)
	if !strings.HasPrefix(datasetID, "dataset_") || len(datasetID) == len("dataset_") {
		return "", errors.New("factor_result Dataset ID must start with dataset_ and include a suffix")
	}
	viewID := "view_" + strings.TrimPrefix(datasetID, "dataset_")
	if err := validateViewID(viewID); err != nil {
		return "", fmt.Errorf("derive factor result default View ID: %w", err)
	}
	return viewID, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
