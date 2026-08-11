package registry

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/proto"
)

// MetadataClient is the Storage Metadata subset required by factor registry sync.
type MetadataClient interface {
	CreateFactor(ctx context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error)
	UpdateFactor(ctx context.Context, req *storagepb.UpdateFactorReq) (*storagepb.UpdateFactorRsp, error)
	GetFactor(ctx context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error)
	CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error)
	GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
	UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
}

type viewMetadataClient interface {
	CreateView(context.Context, *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error)
	GetView(context.Context, *storagepb.GetViewReq) (*storagepb.GetViewRsp, error)
	UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error)
}

type viewUpdater interface {
	UpdateView(context.Context, *storagepb.UpdateViewReq) (*storagepb.UpdateViewRsp, error)
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

// MetadataSync mirrors local factor definitions into Storage Metadata.
type MetadataSync struct {
	client MetadataClient
	auth   *commonpb.AuthInfo
}

// SupportsViews reports whether the configured Storage client exposes the
// explicit View metadata surface introduced by the view-driven trigger.
func (s *MetadataSync) SupportsViews() bool {
	if s == nil || s.client == nil {
		return false
	}
	_, ok := s.client.(viewMetadataClient)
	return ok
}

// NewMetadataSync creates a Storage Metadata syncer.
func NewMetadataSync(client MetadataClient, auth *commonpb.AuthInfo) *MetadataSync {
	return &MetadataSync{client: client, auth: auth}
}

func (s *MetadataSync) viewClient() (viewMetadataClient, error) {
	client, ok := s.client.(viewMetadataClient)
	if !ok {
		return nil, fmt.Errorf("storage metadata client does not support View metadata")
	}
	return client, nil
}

func (s *MetadataSync) SourceViewDatasetIDs(ctx context.Context, spaceID, viewID string) ([]string, error) {
	view, err := s.getView(ctx, spaceID, viewID)
	if err != nil {
		return nil, err
	}
	ids := append([]string(nil), view.GetDatasetIds()...)
	if view.GetPrimaryDatasetId() != "" {
		ids = append(ids, view.GetPrimaryDatasetId())
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	return ids, nil
}

// ResolveManagedResultIDs resolves the source View's primary Dataset before
// deriving managed result object IDs. This keeps result names readable and
// stable even when the source View ID has a suffix such as "_view".
func (s *MetadataSync) ResolveManagedResultIDs(ctx context.Context, spaceID, viewID string) (string, string, error) {
	view, err := s.getView(ctx, spaceID, viewID)
	if err != nil {
		return "", "", err
	}
	sourceDatasetID := strings.TrimSpace(view.GetPrimaryDatasetId())
	if sourceDatasetID == "" {
		return "", "", fmt.Errorf("source View %s/%s has no primary Dataset", spaceID, viewID)
	}
	return ResultDatasetForView(sourceDatasetID, viewID), ResultViewForView(sourceDatasetID, viewID), nil
}

// SyncBindingViews ensures the managed result Dataset/View and desired output
// columns exist. It reports whether the desired Result View schema is active.
func (s *MetadataSync) SyncBindingViews(ctx context.Context, binding domain.FactorBinding, factors []domain.FactorDef) (bool, error) {
	if s == nil || s.client == nil {
		return true, nil
	}
	if len(factors) == 0 {
		return false, fmt.Errorf("at least one factor is required")
	}
	if _, ok := s.client.(viewMetadataClient); !ok {
		source := binding.SourceViewID
		if binding.SourceDataset != "" {
			source = binding.SourceDataset
		}
		target := binding.ResultDatasetID
		if binding.TargetDataset != "" {
			target = binding.TargetDataset
		}
		if err := s.SyncTargetDataset(ctx, binding.SpaceID, source, target, binding.Freq, factors); err != nil {
			return false, err
		}
		return true, nil
	}
	for _, factor := range factors {
		if err := s.SyncFactorMetadata(ctx, binding.SpaceID, factor); err != nil {
			return false, err
		}
		if err := s.ValidateEnabledBinding(ctx, binding, factor); err != nil {
			return false, err
		}
	}
	sourceView, err := s.getView(ctx, binding.SpaceID, binding.SourceViewID)
	if err != nil {
		return false, err
	}
	source, err := s.getDataset(ctx, binding.SpaceID, sourceView.GetPrimaryDatasetId())
	if err != nil {
		return false, err
	}
	if err := s.ensureManagedResultDataset(ctx, binding, sourceView, source); err != nil {
		return false, err
	}
	if err := s.copyDatasetSubjects(ctx, binding.SpaceID, sourceView.GetPrimaryDatasetId(), binding.ResultDatasetID); err != nil {
		return false, err
	}
	for _, factor := range factors {
		for _, output := range factor.Outputs {
			if err := s.upsertResultColumn(ctx, binding.SpaceID, binding.ResultDatasetID, factor, output); err != nil {
				return false, err
			}
		}
	}
	if err := s.activateResultDataset(ctx, binding.SpaceID, binding.ResultDatasetID); err != nil {
		return false, err
	}
	if err := s.ensureManagedResultView(ctx, binding, sourceView, source, factors); err != nil {
		return false, err
	}
	return s.resultViewReady(ctx, binding, factors)
}

// RemoveBindingResultColumns reconciles retired factor columns out of the
// desired Result View schema. Storage treats a non-nil View.Columns on
// UpdateView as a complete replacement and builds the next revision.
func (s *MetadataSync) RemoveBindingResultColumns(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	if s == nil || s.client == nil {
		return nil
	}
	updater, ok := s.client.(viewUpdater)
	if !ok {
		return nil
	}
	view, found, err := s.findView(ctx, binding.SpaceID, binding.ResultViewID)
	if err != nil || !found || view == nil {
		return err
	}
	remove := make(map[string]struct{}, len(factor.Outputs))
	for _, output := range factor.Outputs {
		remove[binding.ResultDatasetID+"."+OutputField(factor.FactorID, output)] = struct{}{}
	}
	columns := make([]*storagepb.ViewColumn, 0, len(view.GetColumns()))
	removed := false
	for _, column := range view.GetColumns() {
		if _, ok := remove[column.GetColumnName()]; ok {
			removed = true
			continue
		}
		columns = append(columns, column)
	}
	if !removed {
		// A previous attempt may have committed the desired schema update but
		// timed out while the asynchronous View build was still running. Keep
		// checking the revision on retries instead of treating the already
		// removed desired column as completion.
		return s.waitViewRevision(ctx, binding.SpaceID, binding.ResultViewID, view)
	}
	view.Columns = columns
	if view.Attributes == nil {
		view.Attributes = make(map[string]string)
	}
	view.Attributes["moox.columns_explicit"] = "true"
	rsp, err := updater.UpdateView(ctx, &storagepb.UpdateViewReq{AuthInfo: s.auth, View: view, ReplaceColumns: true})
	if err != nil {
		return err
	}
	if !retOK(rsp.GetRetInfo()) {
		return retInfoError("UpdateView", rsp.GetRetInfo())
	}
	return s.waitViewRevision(ctx, binding.SpaceID, binding.ResultViewID, rsp.GetView())
}

func (s *MetadataSync) waitViewRevision(ctx context.Context, spaceID, viewID string, updated *storagepb.View) error {
	if updated == nil || updated.GetDesiredViewRevision() == 0 || updated.GetActiveViewRevision() >= updated.GetDesiredViewRevision() {
		return nil
	}
	client, ok := s.client.(viewMetadataClient)
	if !ok {
		return fmt.Errorf("storage metadata client cannot verify View revision activation")
	}
	// The View reconciler runs on a 30-second cadence in the default personal
	// deployment. Allow one full tick plus a small scheduling margin before
	// leaving cleanup_pending for an explicit retry.
	waitCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := client.GetView(waitCtx, &storagepb.GetViewReq{AuthInfo: s.auth, SpaceId: spaceID, ViewId: viewID})
		if err != nil {
			return err
		}
		if !retOK(current.GetRetInfo()) {
			return retInfoError("GetView", current.GetRetInfo())
		}
		view := current.GetView()
		if view != nil && view.GetActiveViewRevision() >= updated.GetDesiredViewRevision() {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("View %s/%s revision %d not active: %w", spaceID, viewID, updated.GetDesiredViewRevision(), waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// SyncResultDataset ensures factor, result dataset, and result columns exist.
func (s *MetadataSync) SyncResultDataset(ctx context.Context, spaceID string, sourceDataset string, freq string, factors []domain.FactorDef) error {
	return s.SyncTargetDataset(ctx, spaceID, sourceDataset, ResultDataset(sourceDataset), freq, factors)
}

// SyncFactorMetadata reconciles one local factor definition into a Storage space.
func (s *MetadataSync) SyncFactorMetadata(ctx context.Context, spaceID string, factor domain.FactorDef) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.createFactor(ctx, spaceID, factor)
}

// SyncTargetDataset ensures factor, result dataset, and result columns exist.
func (s *MetadataSync) SyncTargetDataset(ctx context.Context, spaceID string, sourceDataset string, targetDataset string, freq string, factors []domain.FactorDef) error {
	if s == nil || s.client == nil {
		return nil
	}
	for _, factor := range factors {
		if err := s.SyncFactorMetadata(ctx, spaceID, factor); err != nil {
			return err
		}
	}
	return s.syncTargetDatasetAfterFactorMetadata(ctx, spaceID, sourceDataset, targetDataset, freq, factors)
}

// SyncTargetDatasetAfterFactorMetadata reconciles a target after its factors have
// already been synchronized into the same Storage space.
func (s *MetadataSync) SyncTargetDatasetAfterFactorMetadata(ctx context.Context, spaceID string, sourceDataset string, targetDataset string, freq string, factors []domain.FactorDef) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.syncTargetDatasetAfterFactorMetadata(ctx, spaceID, sourceDataset, targetDataset, freq, factors)
}

func (s *MetadataSync) syncTargetDatasetAfterFactorMetadata(ctx context.Context, spaceID string, sourceDataset string, targetDataset string, freq string, factors []domain.FactorDef) error {
	if strings.TrimSpace(targetDataset) == "" {
		targetDataset = ResultDataset(sourceDataset)
	}
	source, err := s.getDataset(ctx, spaceID, sourceDataset)
	if err != nil {
		return fmt.Errorf("load source dataset %s/%s: %w", spaceID, sourceDataset, err)
	}
	if source.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("source dataset %s/%s must be time-series", spaceID, sourceDataset)
	}
	dataSourceID := source.GetDataSourceId()
	if strings.TrimSpace(dataSourceID) == "" {
		return fmt.Errorf("source dataset %s/%s must define data_source_id", spaceID, sourceDataset)
	}
	if strings.TrimSpace(source.GetDataNodeId()) == "" || strings.TrimSpace(source.GetKeepDuration()) == "" {
		return fmt.Errorf("source dataset %s/%s must define data_node_id and keep_duration", spaceID, sourceDataset)
	}
	if err := s.createDataset(ctx, spaceID, sourceDataset, targetDataset, dataSourceID, source, freq); err != nil {
		return err
	}
	if err := s.copyDatasetSubjects(ctx, spaceID, sourceDataset, targetDataset); err != nil {
		return err
	}
	for _, factor := range factors {
		for _, output := range factor.Outputs {
			if err := s.upsertColumn(ctx, spaceID, targetDataset, factor, output); err != nil {
				return err
			}
		}
	}
	target, err := s.getDataset(ctx, spaceID, targetDataset)
	if err != nil {
		return fmt.Errorf("load target dataset %s/%s after sync: %w", spaceID, targetDataset, err)
	}
	if target.GetStatus() == "active" && target.GetBindingLocked() {
		return nil
	}
	checkRsp, err := s.client.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
		AuthInfo:  s.auth,
		SpaceId:   spaceID,
		DatasetId: targetDataset,
	})
	if err != nil {
		return fmt.Errorf("check dataset activation %s/%s: %w", spaceID, targetDataset, err)
	}
	if !retOK(checkRsp.GetRetInfo()) {
		return retInfoError("CheckDatasetActivation", checkRsp.GetRetInfo())
	}
	if !checkRsp.GetReady() {
		return activationNotReadyError(spaceID, targetDataset, checkRsp.GetChecks())
	}
	activateRsp, err := s.client.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
		AuthInfo:         s.auth,
		SpaceId:          spaceID,
		DatasetId:        targetDataset,
		ExpectedRevision: checkRsp.GetDatasetRevision(),
	})
	if err != nil {
		return fmt.Errorf("activate dataset %s/%s: %w", spaceID, targetDataset, err)
	}
	if retOK(activateRsp.GetRetInfo()) {
		return nil
	}
	if isConflictRet(activateRsp.GetRetInfo()) {
		latest, latestErr := s.getDataset(ctx, spaceID, targetDataset)
		if latestErr == nil && latest.GetStatus() == "active" && latest.GetBindingLocked() {
			return nil
		}
	}
	return retInfoError("ActivateDataset", activateRsp.GetRetInfo())
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

func (s *MetadataSync) createFactor(ctx context.Context, spaceID string, factor domain.FactorDef) error {
	inputColumnsJSON, err := json.Marshal(factor.InputColumns)
	if err != nil {
		return fmt.Errorf("marshal input columns for factor %s: %w", factor.FactorID, err)
	}
	outputsJSON, err := json.Marshal(factor.Outputs)
	if err != nil {
		return fmt.Errorf("marshal outputs for factor %s: %w", factor.FactorID, err)
	}
	storageFactor := &storagepb.Factor{
		SpaceId:    spaceID,
		FactorId:   factor.FactorID,
		Name:       factor.Name,
		Algorithm:  factor.Name,
		ParamsJson: factor.ParamsJSON,
		ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     storageFactorStatus(factor.Status),
		Attributes: map[string]string{
			"input_columns_json": string(inputColumnsJSON),
			"outputs_json":       string(outputsJSON),
			"lookback_periods":   strconv.Itoa(factor.LookbackPeriods),
		},
	}

	getRsp, err := s.client.GetFactor(ctx, &storagepb.GetFactorReq{
		AuthInfo: s.auth, SpaceId: spaceID, FactorId: factor.FactorID,
	})
	if err != nil {
		return fmt.Errorf("get factor %s/%s: %w", spaceID, factor.FactorID, err)
	}
	if retOK(getRsp.GetRetInfo()) {
		if getRsp.GetFactor() == nil {
			return fmt.Errorf("GetFactor succeeded without factor %s/%s", spaceID, factor.FactorID)
		}
		rsp, updateErr := s.client.UpdateFactor(ctx, &storagepb.UpdateFactorReq{
			AuthInfo: s.auth, Factor: storageFactor,
		})
		if updateErr != nil {
			return fmt.Errorf("update factor %s/%s: %w", spaceID, factor.FactorID, updateErr)
		}
		if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
			return nil
		}
		return retInfoError("UpdateFactor", rsp.GetRetInfo())
	}
	if !isFactorNotFoundRet(getRsp.GetRetInfo()) {
		return retInfoError("GetFactor", getRsp.GetRetInfo())
	}
	rsp, err := s.client.CreateFactor(ctx, &storagepb.CreateFactorReq{
		AuthInfo: s.auth, Factor: storageFactor,
	})
	if err != nil {
		return fmt.Errorf("create factor %s/%s: %w", spaceID, factor.FactorID, err)
	}
	if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("CreateFactor", rsp.GetRetInfo())
}

func (s *MetadataSync) createDataset(ctx context.Context, spaceID string, sourceDataset string, datasetID string, dataSourceID string, source *storagepb.Dataset, freq string) error {
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
			Name:         factorResultDatasetDisplayName(source),
			Description:  fmt.Sprintf("Factor result dataset for %s", sourceDataset),
			DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs:        []string{freq},
			Status:       "disabled",
			DataNodeId:   source.GetDataNodeId(),
			KeepDuration: source.GetKeepDuration(),
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

func (s *MetadataSync) upsertColumn(ctx context.Context, spaceID string, datasetID string, factor domain.FactorDef, columnName string) error {
	req := &storagepb.UpsertDatasetColumnReq{
		AuthInfo: s.auth,
		Column: &storagepb.DatasetColumn{
			SpaceId:    spaceID,
			DatasetId:  datasetID,
			ColumnName: columnName,
			OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR,
			OriginId:   factorColumnOriginID(factor.FactorID, columnName),
			ValueType:  storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Required:   false,
			Status:     "active",
			Attributes: factorOutputAttributes(factor.FactorID, columnName),
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

func (s *MetadataSync) getView(ctx context.Context, spaceID, viewID string) (*storagepb.View, error) {
	client, err := s.viewClient()
	if err != nil {
		return nil, err
	}
	rsp, err := client.GetView(ctx, &storagepb.GetViewReq{AuthInfo: s.auth, SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return nil, err
	}
	if retOK(rsp.GetRetInfo()) && rsp.GetView() != nil {
		return rsp.GetView(), nil
	}
	return nil, retInfoError("GetView", rsp.GetRetInfo())
}

func (s *MetadataSync) ensureManagedResultDataset(ctx context.Context, binding domain.FactorBinding, sourceView *storagepb.View, source *storagepb.Dataset) error {
	attrs := factorResultViewAttributes(binding.SourceViewID, binding.ResultViewID, binding.Freq)
	existing, found, err := s.findDataset(ctx, binding.SpaceID, binding.ResultDatasetID)
	if err != nil {
		return err
	}
	if found {
		if err := validateTargetDataset(existing, binding.SpaceID, binding.ResultDatasetID); err != nil {
			return err
		}
		for key, value := range attrs {
			if current := strings.TrimSpace(existing.GetAttributes()[key]); current != "" && current != value {
				return fmt.Errorf("dataset %s attribute conflict: %s=%q cannot be overwritten with %q", binding.ResultDatasetID, key, current, value)
			}
		}
		next := proto.Clone(existing).(*storagepb.Dataset)
		next.Attributes = cloneStringMap(existing.GetAttributes())
		if next.Attributes == nil {
			next.Attributes = map[string]string{}
		}
		for key, value := range attrs {
			next.Attributes[key] = value
		}
		next.Freqs = mergeDatasetFreq(next.GetFreqs(), binding.Freq)
		if sourceView != nil || source != nil {
			desiredKeep := factorResultKeepDuration(sourceView, source)
			// Storage requires every Dataset keep to cover its current Views.
			// When shrinking, defer the Dataset update until the managed View has
			// been shortened; expansion can happen in this first step.
			if strings.TrimSpace(next.GetKeepDuration()) != desiredKeep && !resultKeepShorterThanView(ctx, s, binding, desiredKeep) {
				next.KeepDuration = desiredKeep
			}
		}
		if stringMapsEqual(existing.GetAttributes(), next.GetAttributes()) && stringSlicesEqual(existing.GetFreqs(), next.GetFreqs()) && existing.GetKeepDuration() == next.GetKeepDuration() {
			return nil
		}
		rsp, updateErr := s.client.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{AuthInfo: s.auth, Dataset: next})
		if updateErr != nil {
			return updateErr
		}
		if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
			return nil
		}
		return retInfoError("UpdateDataset", rsp.GetRetInfo())
	}
	if source == nil || sourceView == nil {
		return fmt.Errorf("source view and primary dataset are required")
	}
	keepDuration := factorResultKeepDuration(sourceView, source)
	rsp, err := s.client.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: s.auth, Dataset: &storagepb.Dataset{
		SpaceId: binding.SpaceID, DatasetId: binding.ResultDatasetID,
		DataSourceId: source.GetDataSourceId(), Name: factorResultDatasetDisplayName(source),
		Description: "Factor result Dataset for " + binding.SourceViewID,
		DataKind:    storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{binding.Freq},
		Status: "disabled", DataNodeId: source.GetDataNodeId(), KeepDuration: keepDuration, Attributes: attrs,
	}})
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("CreateDataset", rsp.GetRetInfo())
}

func resultKeepShorterThanView(ctx context.Context, sync *MetadataSync, binding domain.FactorBinding, desired string) bool {
	if sync == nil || sync.client == nil || strings.TrimSpace(desired) == "" {
		return false
	}
	view, found, err := sync.findView(ctx, binding.SpaceID, binding.ResultViewID)
	if err != nil || !found || view == nil {
		return false
	}
	current := strings.TrimSpace(view.GetKeepDuration())
	if current == "" || current == desired {
		return false
	}
	if desired == "0" {
		return false
	}
	if current == "0" {
		return true
	}
	desiredDuration, desiredErr := time.ParseDuration(desired)
	currentDuration, currentErr := time.ParseDuration(current)
	return desiredErr == nil && currentErr == nil && desiredDuration < currentDuration
}

// factorResultKeepDuration keeps the managed result View within the retention
// window of the source View. A zero source window is intentionally propagated
// to the result Dataset so Storage's dataset/view retention invariant remains
// valid; ordinary finite source views keep result facts bounded as well.
func factorResultKeepDuration(sourceView *storagepb.View, source *storagepb.Dataset) string {
	if sourceView != nil {
		if keep := strings.TrimSpace(sourceView.GetKeepDuration()); keep != "" {
			return keep
		}
	}
	if source != nil {
		if keep := strings.TrimSpace(source.GetKeepDuration()); keep != "" {
			return keep
		}
	}
	return "0"
}

func (s *MetadataSync) upsertResultColumn(ctx context.Context, spaceID, datasetID string, factor domain.FactorDef, output string) error {
	fieldID := OutputField(factor.FactorID, output)
	rsp, err := s.client.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: s.auth, Column: &storagepb.DatasetColumn{
		SpaceId: spaceID, DatasetId: datasetID, ColumnName: fieldID,
		OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR,
		OriginId:   factorColumnOriginID(factor.FactorID, output), ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     "active",
		Attributes: factorOutputAttributes(factor.FactorID, output),
	}})
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("UpsertDatasetColumn", rsp.GetRetInfo())
}

func (s *MetadataSync) activateResultDataset(ctx context.Context, spaceID, datasetID string) error {
	dataset, err := s.getDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	if dataset.GetStatus() == "active" && dataset.GetBindingLocked() {
		return nil
	}
	check, err := s.client.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return err
	}
	if !retOK(check.GetRetInfo()) {
		return retInfoError("CheckDatasetActivation", check.GetRetInfo())
	}
	if !check.GetReady() {
		return activationNotReadyError(spaceID, datasetID, check.GetChecks())
	}
	rsp, err := s.client.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: s.auth, SpaceId: spaceID, DatasetId: datasetID, ExpectedRevision: check.GetDatasetRevision()})
	if err != nil {
		return err
	}
	if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("ActivateDataset", rsp.GetRetInfo())
}

func (s *MetadataSync) ensureManagedResultView(ctx context.Context, binding domain.FactorBinding, sourceView *storagepb.View, source *storagepb.Dataset, factors []domain.FactorDef) error {
	keepDuration := factorResultKeepDuration(sourceView, source)
	view, found, err := s.findView(ctx, binding.SpaceID, binding.ResultViewID)
	if err != nil {
		return err
	}
	if !found {
		client, clientErr := s.viewClient()
		if clientErr != nil {
			return clientErr
		}
		rsp, createErr := client.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: s.auth, View: &storagepb.View{
			SpaceId: binding.SpaceID, ViewId: binding.ResultViewID, Name: factorResultViewDisplayName(),
			Description:      "Factor result View for " + binding.SourceViewID,
			PrimaryDatasetId: binding.ResultDatasetID, DatasetIds: []string{binding.ResultDatasetID},
			FilterJson: fmt.Sprintf(`{"freq":%q}`, binding.Freq), KeepDuration: keepDuration, Status: "active",
			Attributes: factorResultViewAttributes(binding.SourceViewID, binding.ResultViewID, binding.Freq),
		}})
		if createErr != nil {
			return createErr
		}
		if !retOK(rsp.GetRetInfo()) {
			return retInfoError("CreateView", rsp.GetRetInfo())
		}
		view = rsp.GetView()
	}
	if view == nil || view.GetPrimaryDatasetId() != binding.ResultDatasetID || len(view.GetDatasetIds()) != 1 || view.GetDatasetIds()[0] != binding.ResultDatasetID {
		return fmt.Errorf("result view %s/%s has incompatible dataset scope", binding.SpaceID, binding.ResultViewID)
	}
	if strings.TrimSpace(view.GetKeepDuration()) != keepDuration {
		updater, ok := s.client.(viewUpdater)
		if !ok {
			return fmt.Errorf("storage metadata client does not support View updates")
		}
		next := proto.Clone(view).(*storagepb.View)
		next.KeepDuration = keepDuration
		rsp, updateErr := updater.UpdateView(ctx, &storagepb.UpdateViewReq{AuthInfo: s.auth, View: next})
		if updateErr != nil {
			return updateErr
		}
		if !retOK(rsp.GetRetInfo()) && !isRefreshInProgressRet(rsp.GetRetInfo()) {
			return retInfoError("UpdateView", rsp.GetRetInfo())
		}
		if rsp.GetView() != nil {
			view = rsp.GetView()
		}
	}
	if err := validateViewFrequency(view.GetFilterJson(), binding.Freq); err != nil {
		return fmt.Errorf("result view %s/%s: %w", binding.SpaceID, binding.ResultViewID, err)
	}
	for _, factor := range factors {
		for _, output := range factor.Outputs {
			fieldID := OutputField(factor.FactorID, output)
			qualified := binding.ResultDatasetID + "." + fieldID
			client, clientErr := s.viewClient()
			if clientErr != nil {
				return clientErr
			}
			rsp, upsertErr := client.UpsertViewColumn(ctx, &storagepb.UpsertViewColumnReq{AuthInfo: s.auth, Column: &storagepb.ViewColumn{
				SpaceId: binding.SpaceID, ViewId: binding.ResultViewID, ColumnName: qualified,
				OriginType: storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   qualified, ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Attributes: factorOutputAttributes(factor.FactorID, output),
			}})
			if upsertErr != nil {
				return upsertErr
			}
			if !retOK(rsp.GetRetInfo()) && !isRefreshInProgressRet(rsp.GetRetInfo()) {
				return retInfoError("UpsertViewColumn", rsp.GetRetInfo())
			}
		}
	}
	if err := s.reconcileManagedResultDatasetKeep(ctx, binding, keepDuration); err != nil {
		return err
	}
	return nil
}

func (s *MetadataSync) reconcileManagedResultDatasetKeep(ctx context.Context, binding domain.FactorBinding, desired string) error {
	dataset, err := s.getDataset(ctx, binding.SpaceID, binding.ResultDatasetID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(dataset.GetKeepDuration()) == desired {
		return nil
	}
	next := proto.Clone(dataset).(*storagepb.Dataset)
	next.KeepDuration = desired
	rsp, updateErr := s.client.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{AuthInfo: s.auth, Dataset: next})
	if updateErr != nil {
		return updateErr
	}
	if retOK(rsp.GetRetInfo()) || isRefreshInProgressRet(rsp.GetRetInfo()) {
		return nil
	}
	return retInfoError("UpdateDataset", rsp.GetRetInfo())
}

func (s *MetadataSync) resultViewReady(ctx context.Context, binding domain.FactorBinding, factors []domain.FactorDef) (bool, error) {
	view, err := s.getView(ctx, binding.SpaceID, binding.ResultViewID)
	if err != nil {
		return false, err
	}
	if view.GetStatus() != "active" || strings.TrimSpace(view.GetActiveIndexId()) == "" {
		return false, nil
	}
	if view.GetActiveViewRevision() < view.GetDesiredViewRevision() {
		return false, nil
	}
	active := make(map[string]struct{}, len(view.GetActiveColumns()))
	for _, column := range view.GetActiveColumns() {
		active[column.GetColumnName()] = struct{}{}
	}
	for _, factor := range factors {
		for _, output := range factor.Outputs {
			qualified := binding.ResultDatasetID + "." + OutputField(factor.FactorID, output)
			if _, ok := active[qualified]; !ok {
				return false, nil
			}
		}
	}
	return true, nil
}

func (s *MetadataSync) findView(ctx context.Context, spaceID, viewID string) (*storagepb.View, bool, error) {
	client, clientErr := s.viewClient()
	if clientErr != nil {
		return nil, false, clientErr
	}
	rsp, err := client.GetView(ctx, &storagepb.GetViewReq{AuthInfo: s.auth, SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return nil, false, err
	}
	if retOK(rsp.GetRetInfo()) && rsp.GetView() != nil {
		return rsp.GetView(), true, nil
	}
	if isViewNotFoundRet(rsp.GetRetInfo()) {
		return nil, false, nil
	}
	return nil, false, retInfoError("GetView", rsp.GetRetInfo())
}

func (s *MetadataSync) ensureFactorDatasetAttribution(ctx context.Context, spaceID string, datasetID string, sourceDataset string, freq string) error {
	dataset, err := s.getDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	if err := validateTargetDataset(dataset, spaceID, datasetID); err != nil {
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
	if err := validateTargetDataset(dataset, spaceID, datasetID); err != nil {
		return false, err
	}
	return true, s.updateFactorDatasetAttribution(ctx, dataset, datasetID, sourceDataset, freq)
}

func validateTargetDataset(dataset *storagepb.Dataset, spaceID, datasetID string) error {
	if dataset.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("target dataset %s/%s must be time-series", spaceID, datasetID)
	}
	return nil
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

func activationNotReadyError(spaceID, datasetID string, checks []*storagepb.DatasetActivationCheck) error {
	failed := make([]string, 0, len(checks))
	for _, check := range checks {
		if check == nil || check.GetReady() {
			continue
		}
		summary := strings.TrimSpace(check.GetSummary())
		if summary == "" {
			summary = "not ready"
		}
		failed = append(failed, fmt.Sprintf("%s: %s", check.GetCheckId(), summary))
	}
	if len(failed) == 0 {
		failed = append(failed, "metadata activation checks are not ready")
	}
	return fmt.Errorf("dataset %s/%s activation readiness failed: %s", spaceID, datasetID, strings.Join(failed, "; "))
}

func factorOutputAttributes(factorID, output string) map[string]string {
	output = strings.TrimSpace(output)
	return map[string]string{
		"display_name":     output,
		"origin_factor_id": strings.TrimSpace(factorID),
		"factor_output":    output,
	}
}

func datasetDisplayName(datasetID string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(datasetID)))
	return fmt.Sprintf("因子%x", sum[:3])
}

func factorResultDatasetDisplayName(source *storagepb.Dataset) string {
	if source != nil {
		name := strings.TrimSpace(source.GetName())
		candidate := name + "因子"
		if name != "" && len([]rune(candidate)) <= 10 {
			return candidate
		}
	}
	return "因子结果"
}

func factorResultViewDisplayName() string {
	return "因子结果视图"
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

func factorResultViewAttributes(sourceViewID, resultViewID, freq string) map[string]string {
	return map[string]string{
		"owner_module":   "factor",
		"managed_by":     "factor",
		"dataset_role":   "factor_result",
		"write_owner":    "factor",
		"source_view_id": strings.TrimSpace(sourceViewID),
		"result_view_id": strings.TrimSpace(resultViewID),
		"source_freq":    strings.TrimSpace(freq),
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

func factorColumnOriginID(factorID string, output string) string {
	return strings.TrimSpace(factorID) + "." + strings.TrimSpace(output)
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

func isFactorNotFoundRet(ret *commonpb.RetInfo) bool {
	if ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return false
	}
	return ret.GetCode() == commonpb.ErrorCode_FACTOR_NOT_FOUND
}

func isViewNotFoundRet(ret *commonpb.RetInfo) bool {
	if ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return false
	}
	return ret.GetCode() == commonpb.ErrorCode_VIEW_NOT_FOUND ||
		strings.Contains(strings.ToLower(ret.GetMsg()), "view not found")
}

func isRefreshInProgressRet(ret *commonpb.RetInfo) bool {
	if ret == nil || ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return false
	}
	return strings.Contains(strings.ToLower(ret.GetMsg()), "refresh already in progress")
}

func isConflictRet(ret *commonpb.RetInfo) bool {
	return ret != nil && ret.GetCode() == commonpb.ErrorCode_CONFLICT
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
