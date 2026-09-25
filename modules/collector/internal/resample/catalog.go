package resample

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/client"
)

var ErrTargetViewNotReady = errors.New("target View route is not ready")

// ViewSyncWaiter is implemented by PrimaryStore after the route-ready marker
// has been appended. Keeping it optional makes Catalog unit-testable without a
// live View service while production always supplies the authenticated client.
type ViewSyncWaiter interface {
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error)
}

type metadataAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq, ...client.Option) (*storagepb.CreateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error)
	CreateView(context.Context, *storagepb.CreateViewReq, ...client.Option) (*storagepb.CreateViewRsp, error)
	UpdateView(context.Context, *storagepb.UpdateViewReq, ...client.Option) (*storagepb.UpdateViewRsp, error)
	DeleteView(context.Context, *storagepb.DeleteViewReq, ...client.Option) (*storagepb.DeleteViewRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
	UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq, ...client.Option) (*storagepb.UpsertViewColumnRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq, ...client.Option) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq, ...client.Option) (*storagepb.ActivateDatasetRsp, error)
}

// Catalog is the narrow Metadata API needed to provision a task-owned target
// dataset and its query View.
type Catalog struct {
	Metadata metadataAPI
	Auth     *storagepb.AuthInfo
	ViewSync ViewSyncWaiter
}

var klineFields = []struct {
	name  string
	type_ storagepb.FieldValueType
	label string
}{
	{"open", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "开盘价"},
	{"high", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "最高价"},
	{"low", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "最低价"},
	{"close", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "收盘价"},
	{"volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "成交量"},
	{"quote_volume", storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, "报价成交量"},
	{"trade_num", storagepb.FieldValueType_FIELD_VALUE_TYPE_INT, "成交笔数"},
}

// PrepareTarget creates or validates the target Dataset, columns, subject
// bindings and View. Existing resources with a mismatched immutable contract
// are rejected instead of being silently overwritten.
func (c *Catalog) PrepareTarget(ctx context.Context, task domain.CollectionTask, params *domain.CollectParams, source storagesource.DatasetInfo, subjects []domain.Subject, keepDuration string) error {
	if c == nil || c.Metadata == nil || c.Auth == nil {
		return errors.New("resample catalog dependencies are required")
	}
	if params == nil {
		return errors.New("resample params are required")
	}
	targetFreq, err := ParseFixedFrequency(params.TargetFrequency)
	if err != nil {
		return err
	}
	ids := taskresult.ResultIDs(task.SpaceID, task.TaskID)
	params.TargetDatasetID = ids.DatasetID
	targetDatasetID, targetViewID := ids.DatasetID, ids.ViewID
	attrs := map[string]string{
		"owner_module": "collector", "managed_by": "collector", "collector_task_id": task.TaskID, "market_type": strings.ToLower(task.MarketType),
		"storage_model": "wide_common_metrics", "dataset_role": "kline_resample_result",
		"source_dataset_id": params.SourceDatasetID, "source_data_source_id": source.DataSourceID,
		"source_freq": params.SourceFrequency, "source_series_tag": params.SourceSeriesTag,
		"target_freq": targetFreq.Storage, "alignment": params.Alignment,
	}
	createdDataset, createdView := false, false
	compensate := func(original error) error {
		if original == nil {
			return nil
		}
		if cleanupErr := c.cleanupCreatedTarget(ctx, task.SpaceID, targetDatasetID, targetViewID, createdView, createdDataset); cleanupErr != nil {
			return errors.Join(original, cleanupErr)
		}
		return original
	}
	target, getErr := c.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, DatasetId: targetDatasetID})
	if getErr != nil {
		return fmt.Errorf("get target Dataset: %w", getErr)
	}
	if target.GetRetInfo() == nil {
		return errors.New("get target Dataset: empty ret_info")
	}
	if target.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || target.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := c.Metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: c.Auth, Dataset: &storagepb.Dataset{
			SpaceId: task.SpaceID, DatasetId: targetDatasetID, DataSourceId: "crypto", DataNodeId: source.DataNodeID,
			// Dataset names are unique within a space and must contain Chinese
			// display text. Derive a short stable suffix from the target ID so
			// independent resample targets do not collide on metadata creation.
			Name: uniqueResampleDisplayName(targetDatasetID), Description: "Collector生成的K线重采样结果", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs: []string{targetFreq.Storage}, Status: "draft", Attributes: attrs, SubjectTags: append([]string(nil), source.SubjectTags...), KeepDuration: keepDuration,
		}})
		if createErr != nil {
			return fmt.Errorf("create target Dataset: %w", createErr)
		}
		if err := ensureMetadataSuccess("create target Dataset", created.GetRetInfo()); err != nil {
			return err
		}
		createdDataset = true
		target.Dataset = created.GetDataset()
	} else if err := ensureMetadataSuccess("get target Dataset", target.GetRetInfo()); err != nil {
		return err
	}
	if target.GetDataset() == nil {
		return compensate(errors.New("target Dataset is empty"))
	}
	if err := validateTargetDataset(target.GetDataset(), attrs, targetFreq.Storage, "crypto", source.DataNodeID); err != nil {
		return compensate(err)
	}
	_ = subjects
	for _, field := range klineFields {
		resp, callErr := c.Metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: c.Auth, Column: &storagepb.DatasetColumn{
			SpaceId: task.SpaceID, DatasetId: targetDatasetID, ColumnName: field.name,
			OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: field.name,
			ValueType: field.type_, Required: true, Status: "active", Attributes: map[string]string{"display_name": field.label},
		}})
		if callErr != nil {
			return compensate(fmt.Errorf("upsert target column %s: %w", field.name, callErr))
		}
		if err := ensureMetadataSuccess("upsert target column", resp.GetRetInfo()); err != nil {
			return compensate(err)
		}
	}
	check, err := c.Metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, DatasetId: targetDatasetID})
	if err != nil {
		return compensate(fmt.Errorf("check target Dataset activation: %w", err))
	}
	if err := ensureMetadataSuccess("check target Dataset activation", check.GetRetInfo()); err != nil {
		return compensate(err)
	}
	if !check.GetReady() {
		return compensate(fmt.Errorf("target Dataset activation is not ready"))
	}
	if strings.ToLower(strings.TrimSpace(target.GetDataset().GetStatus())) != "active" {
		activated, activateErr := c.Metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, DatasetId: targetDatasetID, ExpectedRevision: target.GetDataset().GetRevision()})
		if activateErr != nil {
			return compensate(fmt.Errorf("activate target Dataset: %w", activateErr))
		}
		if err := ensureMetadataSuccess("activate target Dataset", activated.GetRetInfo()); err != nil {
			return compensate(err)
		}
	}
	viewResp, viewErr := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, ViewId: targetViewID})
	if viewErr != nil {
		return compensate(fmt.Errorf("get target View: %w", viewErr))
	}
	if viewResp.GetRetInfo() == nil {
		return compensate(errors.New("get target View: empty ret_info"))
	}
	if viewResp.GetRetInfo().GetCode() == storagepb.ErrorCode_VIEW_NOT_FOUND || viewResp.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := c.Metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: c.Auth, View: &storagepb.View{
			SpaceId: task.SpaceID, ViewId: targetViewID, Name: uniqueResampleDisplayName(targetDatasetID), Description: "Collector生成的K线重采样查询视图", DatasetId: targetDatasetID,
			GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Engine: "duckdb", FilterJson: fmt.Sprintf(`{"freq":%q}`, targetFreq.Storage), KeepDuration: keepDuration, Status: "active",
			Attributes: map[string]string{"owner_module": "collector", "managed_by": "collector", "collector_task_id": task.TaskID, "view_role": "collection_browse", "route_ready_request_id": "kline-resample-route:" + task.TaskID + ":" + fmt.Sprint(target.GetDataset().GetRevision())},
		}})
		if createErr != nil {
			return compensate(fmt.Errorf("create target View: %w", createErr))
		}
		if err := ensureMetadataSuccess("create target View", created.GetRetInfo()); err != nil {
			return compensate(err)
		}
		createdView = true
		viewResp.View = created.GetView()
	} else if err := ensureMetadataSuccess("get target View", viewResp.GetRetInfo()); err != nil {
		return compensate(err)
	} else if err := validateTargetView(viewResp.GetView(), task, params, targetFreq.Storage); err != nil {
		return compensate(err)
	}
	requestID := "kline-resample-route:" + task.TaskID + ":" + fmt.Sprint(target.GetDataset().GetRevision())
	if viewResp.GetView() != nil && viewResp.GetView().GetAttributes()["route_ready_request_id"] != requestID {
		updated := *viewResp.GetView()
		updated.Attributes = cloneStringMap(updated.GetAttributes())
		updated.Attributes["route_ready_request_id"] = requestID
		updatedResp, updateErr := c.Metadata.UpdateView(ctx, &storagepb.UpdateViewReq{AuthInfo: c.Auth, View: &updated})
		if updateErr != nil {
			return compensate(fmt.Errorf("update target View route-ready marker: %w", updateErr))
		}
		if err := ensureMetadataSuccess("update target View route-ready marker", updatedResp.GetRetInfo()); err != nil {
			return compensate(err)
		}
		viewResp.View = updatedResp.GetView()
	}
	for index, field := range klineFields {
		originID := targetDatasetID + "." + field.name
		resp, callErr := c.Metadata.UpsertViewColumn(ctx, &storagepb.UpsertViewColumnReq{AuthInfo: c.Auth, Column: &storagepb.ViewColumn{
			SpaceId: task.SpaceID, ViewId: targetViewID, ColumnName: originID, OriginType: storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId: originID, ValueType: field.type_, SortOrder: uint32(index + 1), Attributes: map[string]string{"display_name": field.label},
		}})
		if callErr != nil {
			return compensate(fmt.Errorf("upsert target View column %s: %w", field.name, callErr))
		}
		if err := ensureMetadataSuccess("upsert target View column", resp.GetRetInfo()); err != nil {
			return compensate(err)
		}
	}
	// Upserting a View column advances desired_view_revision. Re-read the
	// authoritative revision after the complete desired schema is written; the
	// earlier GetView response may describe a stale definition.
	finalViewResp, finalViewErr := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: task.SpaceID, ViewId: targetViewID})
	if finalViewErr != nil {
		return compensate(fmt.Errorf("get target View after columns: %w", finalViewErr))
	}
	if err := ensureMetadataSuccess("get target View after columns", finalViewResp.GetRetInfo()); err != nil {
		return compensate(err)
	}
	finalView := finalViewResp.GetView()
	if finalView == nil {
		return compensate(errors.New("target View after columns is empty"))
	}
	if c.ViewSync == nil {
		return compensate(errors.New("resample catalog View sync waiter is required"))
	}
	syncResp, syncErr := c.ViewSync.WaitViewSyncPoint(ctx, &storagepb.WaitViewSyncPointReq{
		AuthInfo: c.Auth, SpaceId: task.SpaceID, ViewId: targetViewID, RequestId: requestID,
		DatasetIds: []string{targetDatasetID}, WaitTimeoutMs: 5000,
	})
	if syncErr != nil {
		return compensate(fmt.Errorf("wait target View sync point: %w", syncErr))
	}
	if syncResp == nil {
		return compensate(ErrTargetViewNotReady)
	}
	if err := ensureMetadataSuccess("wait target View sync point", syncResp.GetRetInfo()); err != nil {
		return compensate(err)
	}
	if !syncResp.GetReady() {
		return compensate(ErrTargetViewNotReady)
	}
	if err := waitTargetViewRevision(ctx, c.Metadata, c.Auth, task.SpaceID, targetViewID, finalView.GetDesiredViewRevision(), 5*time.Second); err != nil {
		return compensate(err)
	}
	return nil
}

func (c *Catalog) cleanupCreatedTarget(ctx context.Context, spaceID, datasetID, viewID string, deleteView, deleteDataset bool) error {
	if c == nil || c.Metadata == nil {
		return errors.New("resample catalog metadata is not configured")
	}
	var cleanupErrors []error
	if deleteView {
		view, err := c.Metadata.DeleteView(ctx, &storagepb.DeleteViewReq{AuthInfo: c.Auth, SpaceId: spaceID, ViewId: viewID})
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete newly-created target View: %w", err))
		} else if view == nil || (view.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && view.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_FOUND && view.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
			cleanupErrors = append(cleanupErrors, catalogMetadataError("delete newly-created target View", nil, retInfoViewDelete(view)))
		}
	}
	if deleteDataset {
		dataset, err := c.Metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: c.Auth, SpaceId: spaceID, DatasetId: datasetID})
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete newly-created target Dataset: %w", err))
		} else if dataset == nil || (dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_DATASET_NOT_FOUND && dataset.GetRetInfo().GetCode() != storagepb.ErrorCode_NOT_FOUND) {
			cleanupErrors = append(cleanupErrors, catalogMetadataError("delete newly-created target Dataset", nil, retInfoDatasetDelete(dataset)))
		}
	}
	return errors.Join(cleanupErrors...)
}

func retInfoViewDelete(resp *storagepb.DeleteViewRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func retInfoDatasetDelete(resp *storagepb.DeleteDatasetRsp) *storagepb.RetInfo {
	if resp == nil {
		return nil
	}
	return resp.GetRetInfo()
}

func catalogMetadataError(action string, callErr error, ret *storagepb.RetInfo) error {
	if callErr != nil {
		return fmt.Errorf("%s: %w", action, callErr)
	}
	if ret == nil {
		return fmt.Errorf("%s: empty response", action)
	}
	return fmt.Errorf("%s: %s", action, ret.GetMsg())
}

func uniqueResampleDisplayName(targetDatasetID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(targetDatasetID)))
	// Storage display names must contain Chinese and be at most ten runes.
	// Keep one Chinese marker plus nine base32 characters (45 bits) so the
	// deterministic names remain readable while making collisions extremely
	// unlikely across arbitrary target dataset IDs.
	suffix := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
	return "重" + strings.ToLower(suffix[:9])
}

type viewGetter interface {
	GetView(context.Context, *storagepb.GetViewReq, ...client.Option) (*storagepb.GetViewRsp, error)
}

// waitTargetViewRevision fences Collector activation on the physical View
// index, not merely on the route-ready marker. A marker can be visible before
// the asynchronous View Maintainer has activated the desired schema revision.
func waitTargetViewRevision(ctx context.Context, getter viewGetter, auth *storagepb.AuthInfo, spaceID, viewID string, desired uint64, timeout time.Duration) error {
	if desired == 0 {
		return nil
	}
	if getter == nil {
		return errors.New("metadata View getter is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		response, err := getter.GetView(waitCtx, &storagepb.GetViewReq{AuthInfo: auth, SpaceId: spaceID, ViewId: viewID})
		if err != nil {
			if waitCtx.Err() != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrTargetViewNotReady
			}
			return fmt.Errorf("get target View revision: %w", err)
		}
		if response == nil {
			return errors.New("get target View revision: empty response")
		}
		if err := ensureMetadataSuccess("get target View revision", response.GetRetInfo()); err != nil {
			return err
		}
		view := response.GetView()
		if view == nil {
			return errors.New("get target View revision: empty View")
		}
		if targetViewRevisionReady(view, desired) {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-waitCtx.Done():
			timer.Stop()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return ErrTargetViewNotReady
		case <-timer.C:
		}
	}
}

func targetViewRevisionReady(view *storagepb.View, desired uint64) bool {
	if view == nil || view.GetDesiredViewRevision() > desired || view.GetActiveViewRevision() < desired || strings.TrimSpace(view.GetActiveIndexId()) == "" {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(view.GetStatus()))
	return status == "" || status == "active"
}

func validateTargetView(view *storagepb.View, task domain.CollectionTask, params *domain.CollectParams, frequency string) error {
	if view == nil {
		return errors.New("target View is empty")
	}
	if view.GetDatasetId() != params.TargetDatasetID {
		return errors.New("target View immutable Dataset contract does not match task")
	}
	if view.GetFilterJson() != fmt.Sprintf(`{"freq":%q}`, frequency) || view.GetEngine() != "duckdb" {
		return errors.New("target View immutable frequency contract does not match task")
	}
	wantGrain := []string{"subject_id", "freq", "data_time", "series_tag"}
	if len(view.GetGrainKeys()) != len(wantGrain) {
		return errors.New("target View grain contract does not match task")
	}
	for i := range wantGrain {
		if view.GetGrainKeys()[i] != wantGrain[i] {
			return errors.New("target View grain contract does not match task")
		}
	}
	if strings.TrimSpace(task.TaskID) != "" {
		attributes := view.GetAttributes()
		if attributes["owner_module"] != "collector" || attributes["collector_task_id"] != task.TaskID {
			return errors.New("target View owner contract does not match task")
		}
	}
	return nil
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input)+1)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func validateTargetDataset(dataset *storagepb.Dataset, want map[string]string, frequency, dataSourceID, dataNodeID string) error {
	if dataset.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return errors.New("target Dataset must be time_series")
	}
	if strings.TrimSpace(dataset.GetDataSourceId()) != strings.TrimSpace(dataSourceID) {
		return fmt.Errorf("target Dataset data source does not match task: got %q want %q", dataset.GetDataSourceId(), dataSourceID)
	}
	if strings.TrimSpace(dataNodeID) != "" && strings.TrimSpace(dataset.GetDataNodeId()) != strings.TrimSpace(dataNodeID) {
		return fmt.Errorf("target Dataset data node does not match source: got %q want %q", dataset.GetDataNodeId(), dataNodeID)
	}
	for key, expected := range want {
		if dataset.GetAttributes()[key] != expected {
			return fmt.Errorf("target Dataset immutable lineage attribute %s does not match task", key)
		}
	}
	wantedFrequency, err := report.NormalizeDatasetFrequency(strings.TrimSpace(frequency))
	if err != nil {
		return fmt.Errorf("target Dataset frequency %q is invalid: %w", frequency, err)
	}
	found := false
	for _, freq := range dataset.GetFreqs() {
		actualFrequency, normalizeErr := report.NormalizeDatasetFrequency(strings.TrimSpace(freq))
		if normalizeErr == nil && actualFrequency == wantedFrequency {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("target Dataset does not enable frequency %s", frequency)
	}
	return nil
}

func ensureMetadataSuccess(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}
