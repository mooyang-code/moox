package registry

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// MetadataClient is the Storage Metadata subset required by factor registry sync.
type MetadataClient interface {
	CreateFactor(ctx context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error)
	CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
}

type factorGetter interface {
	GetFactor(ctx context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error)
}

type datasetGetter interface {
	GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
}

type columnLister interface {
	ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error)
}

// MetadataSync mirrors local factor definitions into Storage Metadata.
type MetadataSync struct {
	client MetadataClient
	auth   *commonpb.AuthInfo
}

// NewMetadataSync creates a Storage Metadata syncer.
func NewMetadataSync(client MetadataClient, auth *commonpb.AuthInfo) *MetadataSync {
	return &MetadataSync{client: client, auth: auth}
}

// SyncResultDataset ensures factor, result dataset, and result columns exist.
func (s *MetadataSync) SyncResultDataset(ctx context.Context, spaceID string, sourceDataset string, freq string, factors []domain.FactorDef) error {
	return s.SyncTargetDataset(ctx, spaceID, sourceDataset, ResultDataset(sourceDataset), freq, factors)
}

// SyncTargetDataset ensures factor, result dataset, and result columns exist.
func (s *MetadataSync) SyncTargetDataset(ctx context.Context, spaceID string, sourceDataset string, targetDataset string, freq string, factors []domain.FactorDef) error {
	if s == nil || s.client == nil {
		return nil
	}
	if strings.TrimSpace(targetDataset) == "" {
		targetDataset = ResultDataset(sourceDataset)
	}
	for _, factor := range factors {
		if err := s.createFactor(ctx, spaceID, factor); err != nil {
			return err
		}
	}
	dataSourceID := s.sourceDataSourceID(ctx, spaceID, sourceDataset)
	if err := s.createDataset(ctx, spaceID, sourceDataset, targetDataset, dataSourceID, freq); err != nil {
		return err
	}
	for _, factor := range factors {
		params, err := factorParams(factor.ParamsJSON)
		if err != nil {
			return fmt.Errorf("parse params for factor %s: %w", factor.FactorID, err)
		}
		for _, param := range params {
			columnName := fmt.Sprintf("%s_%d", factor.Name, param)
			if err := s.upsertColumn(ctx, spaceID, targetDataset, factor, param, columnName); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MetadataSync) createFactor(ctx context.Context, spaceID string, factor domain.FactorDef) error {
	req := &storagepb.CreateFactorReq{
		AuthInfo: s.auth,
		Factor: &storagepb.Factor{
			SpaceId:    spaceID,
			FactorId:   factor.FactorID,
			Name:       factor.Name,
			Algorithm:  factor.Name,
			ParamsJson: factor.ParamsJSON,
			ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Status:     "enabled",
		},
	}
	rsp, err := s.client.CreateFactor(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) {
		return nil
	}
	if isDuplicateRet(rsp.GetRetInfo()) {
		return s.confirmFactorExists(ctx, spaceID, factor.FactorID)
	}
	return retInfoError("CreateFactor", rsp.GetRetInfo())
}

func (s *MetadataSync) createDataset(ctx context.Context, spaceID string, sourceDataset string, datasetID string, dataSourceID string, freq string) error {
	req := &storagepb.CreateDatasetReq{
		AuthInfo: s.auth,
		Dataset: &storagepb.Dataset{
			SpaceId:      spaceID,
			DatasetId:    datasetID,
			DataSourceId: dataSourceID,
			Name:         datasetDisplayName(datasetID),
			Description:  fmt.Sprintf("Factor result dataset for %s", sourceDataset),
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs:        []string{freq},
			Status:       "active",
		},
	}
	rsp, err := s.client.CreateDataset(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) {
		return nil
	}
	if isDuplicateRet(rsp.GetRetInfo()) {
		return s.confirmDatasetExists(ctx, spaceID, datasetID)
	}
	return retInfoError("CreateDataset", rsp.GetRetInfo())
}

func (s *MetadataSync) upsertColumn(ctx context.Context, spaceID string, datasetID string, factor domain.FactorDef, param int, columnName string) error {
	req := &storagepb.UpsertDatasetColumnReq{
		AuthInfo: s.auth,
		Column: &storagepb.DatasetColumn{
			SpaceId:    spaceID,
			DatasetId:  datasetID,
			ColumnName: columnName,
			OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR,
			OriginId:   factorColumnOriginID(factor.FactorID, param),
			ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Required:   false,
			Status:     "active",
			Attributes: map[string]string{
				"display_name":     columnDisplayName(columnName),
				"origin_factor_id": factor.FactorID,
				"factor_param":     fmt.Sprintf("%d", param),
			},
		},
	}
	rsp, err := s.client.UpsertDatasetColumn(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) {
		return nil
	}
	if isDuplicateRet(rsp.GetRetInfo()) {
		return s.confirmColumnExists(ctx, spaceID, datasetID, columnName)
	}
	return retInfoError("UpsertDatasetColumn", rsp.GetRetInfo())
}

func (s *MetadataSync) confirmFactorExists(ctx context.Context, spaceID string, factorID string) error {
	getter, ok := s.client.(factorGetter)
	if !ok {
		return fmt.Errorf("CreateFactor duplicate for %s but MetadataClient cannot confirm existence", factorID)
	}
	rsp, err := getter.GetFactor(ctx, &storagepb.GetFactorReq{AuthInfo: s.auth, SpaceId: spaceID, FactorId: factorID})
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) && rsp.GetFactor() != nil {
		return nil
	}
	return retInfoError("GetFactor", rsp.GetRetInfo())
}

func (s *MetadataSync) confirmDatasetExists(ctx context.Context, spaceID string, datasetID string) error {
	getter, ok := s.client.(datasetGetter)
	if !ok {
		return fmt.Errorf("CreateDataset duplicate for %s but MetadataClient cannot confirm existence", datasetID)
	}
	rsp, err := getter.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) && rsp.GetDataset() != nil {
		return nil
	}
	return retInfoError("GetDataset", rsp.GetRetInfo())
}

func (s *MetadataSync) confirmColumnExists(ctx context.Context, spaceID string, datasetID string, columnName string) error {
	lister, ok := s.client.(columnLister)
	if !ok {
		return fmt.Errorf("UpsertDatasetColumn duplicate for %s but MetadataClient cannot confirm existence", columnName)
	}
	rsp, err := lister.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return err
	}
	if !retOK(rsp.GetRetInfo()) {
		return retInfoError("ListDatasetColumns", rsp.GetRetInfo())
	}
	for _, col := range rsp.GetColumns() {
		if col.GetColumnName() == columnName {
			return nil
		}
	}
	return fmt.Errorf("dataset column %s/%s/%s not found after duplicate response", spaceID, datasetID, columnName)
}

func (s *MetadataSync) sourceDataSourceID(ctx context.Context, spaceID string, sourceDataset string) string {
	getter, ok := s.client.(datasetGetter)
	if !ok {
		return DataSourceIDFromDataset(sourceDataset)
	}
	rsp, err := getter.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: sourceDataset})
	if err == nil && rsp != nil && retOK(rsp.GetRetInfo()) && rsp.GetDataset().GetDataSourceId() != "" {
		return rsp.GetDataset().GetDataSourceId()
	}
	return DataSourceIDFromDataset(sourceDataset)
}

func factorParams(raw string) ([]int, error) {
	var params []int
	if strings.TrimSpace(raw) == "" {
		raw = "[]"
	}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	return params, nil
}

// DataSourceIDFromDataset infers the Storage data_source_id from a dataset id.
// Seeded market datasets follow "<source>_..." (for example binance_spot_kline).
func DataSourceIDFromDataset(datasetID string) string {
	parts := strings.Split(strings.TrimSpace(datasetID), "_")
	if len(parts) == 0 || parts[0] == "" {
		return strings.TrimSpace(datasetID)
	}
	return parts[0]
}

func columnDisplayName(columnName string) string {
	suffix := ""
	if idx := strings.LastIndex(columnName, "_"); idx >= 0 && idx+1 < len(columnName) {
		suffix = columnName[idx+1:]
	}
	name := "因子" + suffix
	if len([]rune(name)) <= 10 {
		return name
	}
	return "因子列"
}

func datasetDisplayName(datasetID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(datasetID)))
	return fmt.Sprintf("因子%x", sum[:3])
}

func factorColumnOriginID(factorID string, param int) string {
	return fmt.Sprintf("%s_%d", strings.TrimSpace(factorID), param)
}

func retOK(ret *commonpb.RetInfo) bool {
	return ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS
}

func isDuplicateRet(ret *commonpb.RetInfo) bool {
	if ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return false
	}
	msg := strings.ToLower(ret.GetMsg())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "constraint failed")
}

func retInfoError(op string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return nil
	}
	return fmt.Errorf("%s failed: code=%d msg=%s", op, ret.GetCode(), ret.GetMsg())
}
