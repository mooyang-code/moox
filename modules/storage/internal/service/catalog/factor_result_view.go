package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	metadatastore "github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

func (s *Service) ensureFactorResultDefaultView(ctx context.Context, dataset *pb.Dataset) error {
	if dataset == nil || strings.TrimSpace(dataset.GetAttributes()["dataset_role"]) != "factor_result" {
		return nil
	}
	viewID, err := defaultFactorResultViewID(dataset.GetDatasetId())
	if err != nil {
		return err
	}
	existing, err := s.metadata.GetView(ctx, dataset.GetSpaceId(), viewID)
	if err == nil {
		if existing.GetDatasetId() != dataset.GetDatasetId() {
			return fmt.Errorf("default View %s/%s belongs to another Dataset", dataset.GetSpaceId(), viewID)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read factor result default View: %w", err)
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_TIME_SERIES || len(dataset.GetFreqs()) != 1 {
		return errors.New("factor_result Dataset must be a time series with exactly one frequency")
	}

	columns, err := s.factorResultViewColumns(ctx, dataset, viewID)
	if err != nil {
		return err
	}
	view := &pb.View{
		SpaceId: dataset.GetSpaceId(), ViewId: viewID, Name: dataset.GetName(), DatasetId: dataset.GetDatasetId(),
		GrainKeys: defaultViewGrainKeys(dataset.GetDataKind()), FilterJson: fmt.Sprintf(`{"freq":%q}`, dataset.GetFreqs()[0]),
		Engine: defaultViewEngine(dataset.GetDataKind()), KeepDuration: dataset.GetKeepDuration(), Status: "active", Columns: columns,
		Attributes: map[string]string{
			"owner_module": "factor", "view_role": "factor_result", "managed_by": "storage",
			"primary_dataset_role": "factor_result",
		},
	}
	if err := s.normalizeAndValidateViewDatasets(ctx, view); err != nil {
		return fmt.Errorf("validate factor result default View: %w", err)
	}
	if _, err := s.metadata.CreateView(ctx, view); err != nil {
		if errors.Is(err, metadatastore.ErrViewExists) {
			existing, readErr := s.metadata.GetView(ctx, dataset.GetSpaceId(), viewID)
			if readErr == nil && existing.GetDatasetId() == dataset.GetDatasetId() {
				return nil
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
				SpaceId: dataset.GetSpaceId(), ViewId: viewID, ColumnName: dataset.GetDatasetId() + "." + item.GetColumnName(),
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   dataset.GetDatasetId() + "." + item.GetColumnName(), ValueType: item.GetValueType(),
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
