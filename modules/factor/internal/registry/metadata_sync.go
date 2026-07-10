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
	"google.golang.org/protobuf/proto"
)

// MetadataClient is the Storage Metadata subset required by factor registry sync.
type MetadataClient interface {
	CreateFactor(ctx context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error)
	CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error)
	GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
}

type factorGetter interface {
	GetFactor(ctx context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error)
}

type columnLister interface {
	ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error)
}

type datasetSubjectLister interface {
	ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error)
}

type datasetSubjectBinder interface {
	BindDatasetSubject(ctx context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error)
}

type primaryStoreRouteLister interface {
	ListPrimaryStoreRoutes(ctx context.Context, req *storagepb.ListPrimaryStoreRoutesReq) (*storagepb.ListPrimaryStoreRoutesRsp, error)
}

type primaryStoreRouteCreator interface {
	CreatePrimaryStoreRoute(ctx context.Context, req *storagepb.CreatePrimaryStoreRouteReq) (*storagepb.CreatePrimaryStoreRouteRsp, error)
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
	if err := s.copyPrimaryStoreRoutes(ctx, spaceID, sourceDataset, targetDataset); err != nil {
		return err
	}
	if err := s.copyDatasetSubjects(ctx, spaceID, sourceDataset, targetDataset); err != nil {
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

func (s *MetadataSync) copyPrimaryStoreRoutes(ctx context.Context, spaceID string, sourceDataset string, targetDataset string) error {
	lister, ok := s.client.(primaryStoreRouteLister)
	if !ok {
		return nil
	}
	creator, ok := s.client.(primaryStoreRouteCreator)
	if !ok {
		return nil
	}
	const pageSize uint32 = 500
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := lister.ListPrimaryStoreRoutes(ctx, &storagepb.ListPrimaryStoreRoutesReq{
			AuthInfo:  s.auth,
			SpaceId:   spaceID,
			DatasetId: sourceDataset,
			Page:      &commonpb.Page{Page: pageNo, Size: pageSize},
		})
		if err != nil {
			return err
		}
		if !retOK(rsp.GetRetInfo()) {
			return retInfoError("ListPrimaryStoreRoutes", rsp.GetRetInfo())
		}
		for _, route := range rsp.GetPrimaryStoreRoutes() {
			if strings.TrimSpace(route.GetNodeId()) == "" {
				continue
			}
			if err := s.createTargetRoute(ctx, creator, spaceID, targetDataset, route); err != nil {
				return err
			}
		}
		page := rsp.GetPageResult()
		if page == nil || !page.GetHasMore() || len(rsp.GetPrimaryStoreRoutes()) == 0 {
			return nil
		}
	}
}

func (s *MetadataSync) createTargetRoute(ctx context.Context, creator primaryStoreRouteCreator, spaceID string, targetDataset string, source *storagepb.PrimaryStoreRoute) error {
	status := strings.TrimSpace(source.GetStatus())
	if status == "" {
		status = "active"
	}
	req := &storagepb.CreatePrimaryStoreRouteReq{
		AuthInfo: s.auth,
		PrimaryStoreRoute: &storagepb.PrimaryStoreRoute{
			SpaceId:        spaceID,
			RouteId:        targetRouteID(targetDataset, source),
			DatasetId:      targetDataset,
			SubjectId:      source.GetSubjectId(),
			SubjectPattern: source.GetSubjectPattern(),
			HashRule:       source.GetHashRule(),
			NodeId:         source.GetNodeId(),
			Priority:       source.GetPriority(),
			Status:         status,
			Attributes:     cloneStringMap(source.GetAttributes()),
		},
	}
	rsp, err := creator.CreatePrimaryStoreRoute(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) || isDuplicateRet(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("CreatePrimaryStoreRoute", rsp.GetRetInfo())
}

func (s *MetadataSync) copyDatasetSubjects(ctx context.Context, spaceID string, sourceDataset string, targetDataset string) error {
	lister, ok := s.client.(datasetSubjectLister)
	if !ok {
		return nil
	}
	binder, ok := s.client.(datasetSubjectBinder)
	if !ok {
		return nil
	}
	const pageSize uint32 = 500
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := lister.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			AuthInfo:  s.auth,
			SpaceId:   spaceID,
			DatasetId: sourceDataset,
			Page:      &commonpb.Page{Page: pageNo, Size: pageSize},
		})
		if err != nil {
			return err
		}
		if !retOK(rsp.GetRetInfo()) {
			return retInfoError("ListDatasetSubjects", rsp.GetRetInfo())
		}
		for _, subject := range rsp.GetDatasetSubjects() {
			if strings.TrimSpace(subject.GetSubjectId()) == "" {
				continue
			}
			if err := s.bindDatasetSubject(ctx, binder, spaceID, targetDataset, subject); err != nil {
				return err
			}
		}
		page := rsp.GetPageResult()
		if page == nil || !page.GetHasMore() || len(rsp.GetDatasetSubjects()) == 0 {
			return nil
		}
	}
}

func (s *MetadataSync) bindDatasetSubject(ctx context.Context, binder datasetSubjectBinder, spaceID string, targetDataset string, source *storagepb.DatasetSubject) error {
	role := strings.TrimSpace(source.GetSubjectRole())
	if role == "" {
		role = "normal"
	}
	status := strings.TrimSpace(source.GetStatus())
	if status == "" {
		status = "active"
	}
	req := &storagepb.BindDatasetSubjectReq{
		AuthInfo: s.auth,
		DatasetSubject: &storagepb.DatasetSubject{
			SpaceId:            spaceID,
			DatasetId:          targetDataset,
			SubjectId:          source.GetSubjectId(),
			SubjectRole:        role,
			EffectiveStartTime: source.GetEffectiveStartTime(),
			EffectiveEndTime:   source.GetEffectiveEndTime(),
			Status:             status,
			Attributes:         cloneStringMap(source.GetAttributes()),
		},
	}
	rsp, err := binder.BindDatasetSubject(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) || isDuplicateRet(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("BindDatasetSubject", rsp.GetRetInfo())
}

func targetRouteID(targetDataset string, source *storagepb.PrimaryStoreRoute) string {
	parts := []string{"route", targetDataset, source.GetSubjectId(), source.GetSubjectPattern(), source.GetNodeId()}
	base := safeID(strings.Join(parts, "-"))
	if base == "" || base == "route" {
		base = safeID("route-" + targetDataset)
	}
	if len(base) <= 80 {
		return base
	}
	sum := sha1.Sum([]byte(base))
	suffix := fmt.Sprintf("-%x", sum[:3])
	return strings.TrimRight(base[:80-len(suffix)], "-") + suffix
}

func safeID(raw string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
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
			Status:     storageFactorStatus(factor.Status),
		},
	}
	rsp, err := s.client.CreateFactor(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) {
		return nil
	}
	if isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	if isDuplicateRet(rsp.GetRetInfo()) {
		return s.confirmFactorExists(ctx, spaceID, factor.FactorID)
	}
	return retInfoError("CreateFactor", rsp.GetRetInfo())
}

func (s *MetadataSync) createDataset(ctx context.Context, spaceID string, sourceDataset string, datasetID string, dataSourceID string, freq string) error {
	exists, err := s.ensureExistingFactorDataset(ctx, spaceID, datasetID, sourceDataset, freq)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
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
			Attributes:   factorResultDatasetAttributes(sourceDataset, freq),
		},
	}
	rsp, err := s.client.CreateDataset(ctx, req)
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) {
		return nil
	}
	if isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	if isDuplicateRet(rsp.GetRetInfo()) {
		return s.ensureFactorDatasetAttribution(ctx, spaceID, datasetID, sourceDataset, freq)
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
	if isRefreshInProgressRet(rsp.GetRetInfo()) {
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

func (s *MetadataSync) getDataset(ctx context.Context, spaceID string, datasetID string) (*storagepb.Dataset, error) {
	rsp, err := s.client.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return nil, err
	}
	if retOK(rsp.GetRetInfo()) && rsp.GetDataset() != nil {
		return rsp.GetDataset(), nil
	}
	return nil, retInfoError("GetDataset", rsp.GetRetInfo())
}

func (s *MetadataSync) ensureFactorDatasetAttribution(ctx context.Context, spaceID string, datasetID string, sourceDataset string, freq string) error {
	dataset, err := s.getDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	return s.updateFactorDatasetAttribution(ctx, dataset, datasetID, sourceDataset, freq)
}

func (s *MetadataSync) ensureExistingFactorDataset(ctx context.Context, spaceID string, datasetID string, sourceDataset string, freq string) (bool, error) {
	dataset, found, err := s.findDataset(ctx, spaceID, datasetID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return true, s.updateFactorDatasetAttribution(ctx, dataset, datasetID, sourceDataset, freq)
}

func (s *MetadataSync) findDataset(ctx context.Context, spaceID string, datasetID string) (*storagepb.Dataset, bool, error) {
	rsp, err := s.client.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return nil, false, err
	}
	if retOK(rsp.GetRetInfo()) && rsp.GetDataset() != nil {
		return rsp.GetDataset(), true, nil
	}
	if isDatasetNotFoundRet(rsp.GetRetInfo()) {
		return nil, false, nil
	}
	return nil, false, retInfoError("GetDataset", rsp.GetRetInfo())
}

func (s *MetadataSync) updateFactorDatasetAttribution(ctx context.Context, dataset *storagepb.Dataset, datasetID string, sourceDataset string, freq string) error {
	nextAttrs, err := mergeFactorResultDatasetAttributes(datasetID, dataset.GetAttributes(), sourceDataset, freq)
	if err != nil {
		return err
	}
	if stringMapsEqual(dataset.GetAttributes(), nextAttrs) {
		nextFreqs := mergeDatasetFreq(dataset.GetFreqs(), freq)
		if stringSlicesEqual(dataset.GetFreqs(), nextFreqs) {
			return nil
		}
	}
	next := proto.Clone(dataset).(*storagepb.Dataset)
	next.Attributes = nextAttrs
	next.Freqs = mergeDatasetFreq(dataset.GetFreqs(), freq)
	rsp, err := s.client.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{
		AuthInfo: s.auth,
		Dataset:  next,
	})
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) || isDuplicateRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("UpdateDataset", rsp.GetRetInfo())
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
	rsp, err := s.client.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: sourceDataset})
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

func factorResultDatasetAttributes(sourceDataset string, freq string) map[string]string {
	return map[string]string{
		"owner_module":      "factor",
		"dataset_role":      "factor_result",
		"managed_by":        "factor",
		"source_dataset_id": strings.TrimSpace(sourceDataset),
		"source_freq":       strings.TrimSpace(freq),
	}
}

func mergeFactorResultDatasetAttributes(datasetID string, existing map[string]string, sourceDataset string, freq string) (map[string]string, error) {
	next := cloneStringMap(existing)
	if next == nil {
		next = map[string]string{}
	}
	for key, value := range factorResultDatasetAttributes(sourceDataset, freq) {
		current := strings.TrimSpace(next[key])
		if current != "" && current != value {
			if key == "source_freq" {
				continue
			}
			return nil, fmt.Errorf("dataset %s attribute conflict: %s=%q cannot be overwritten with %q", datasetID, key, current, value)
		}
		next[key] = value
	}
	return next, nil
}

func mergeDatasetFreq(freqs []string, freq string) []string {
	normalized := strings.TrimSpace(freq)
	out := append([]string(nil), freqs...)
	if normalized == "" {
		return out
	}
	for _, item := range out {
		if strings.TrimSpace(item) == normalized {
			return out
		}
	}
	return append(out, normalized)
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
		strings.Contains(msg, "unique constraint")
}

func isDatasetNotFoundRet(ret *commonpb.RetInfo) bool {
	if ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return false
	}
	return ret.GetCode() == commonpb.ErrorCode_DATASET_NOT_FOUND ||
		strings.Contains(strings.ToLower(ret.GetMsg()), "dataset not found")
}

func isRefreshInProgressRet(ret *commonpb.RetInfo) bool {
	if ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return false
	}
	return strings.Contains(strings.ToLower(ret.GetMsg()), "refresh already in progress")
}

func retInfoError(op string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return nil
	}
	return fmt.Errorf("%s failed: code=%d msg=%s", op, ret.GetCode(), ret.GetMsg())
}

func storageFactorStatus(status string) string {
	if strings.TrimSpace(status) == domain.FactorStatusDisabled {
		return "disabled"
	}
	return "active"
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringMapsEqual(left map[string]string, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if right[key] != leftValue {
			return false
		}
	}
	return true
}

func stringSlicesEqual(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
