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
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/report"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

var ErrTargetViewNotReady = errors.New("target View route is not ready")

// ViewSyncWaiter is implemented by PrimaryStore after the route-ready marker
// has been appended. Keeping it optional makes Catalog unit-testable without a
// live View service while production always supplies the authenticated client.
type ViewSyncWaiter interface {
	WaitViewSyncPoint(context.Context, *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error)
}

// Catalog is the narrow Metadata API needed to provision a target dataset and
// its query View. The concrete proxy uses trpc client.Option; any is avoided in
// the public constructor by accepting the generated proxy below.
type Catalog struct {
	Metadata storagepb.MetadataClientProxy
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
func (c *Catalog) PrepareTarget(ctx context.Context, rule domain.TaskRule, params *domain.CollectParams, source storagesource.DatasetInfo, subjects []domain.DatasetSubject, keepDuration string) error {
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
	attrs := map[string]string{
		"owner_module": "collector", "managed_by": "collector", "market_type": strings.ToLower(rule.MarketType),
		"storage_model": "wide_common_metrics", "dataset_role": "kline_resample_result", "resample_rule_id": rule.RuleID,
		"source_dataset_id": params.SourceDatasetID, "source_data_source_id": source.DataSourceID,
		"source_freq": params.SourceFrequency, "source_series_tag": params.SourceSeriesTag,
		"target_freq": targetFreq.Storage, "alignment": params.Alignment,
	}
	target, getErr := c.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: c.Auth, SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID})
	if getErr != nil {
		return fmt.Errorf("get target Dataset: %w", getErr)
	}
	if target.GetRetInfo() == nil {
		return errors.New("get target Dataset: empty ret_info")
	}
	if target.GetRetInfo().GetCode() == storagepb.ErrorCode_DATASET_NOT_FOUND || target.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := c.Metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: c.Auth, Dataset: &storagepb.Dataset{
			SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID, DataSourceId: "crypto", DataNodeId: source.DataNodeID,
			// Dataset names are unique within a space and must contain Chinese
			// display text. Derive a short stable suffix from the target ID so
			// independent resample targets do not collide on metadata creation.
			Name: uniqueResampleDisplayName(params.TargetDatasetID), Description: "Collector生成的K线重采样结果", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES,
			Freqs: []string{targetFreq.Storage}, Status: "draft", Attributes: attrs, KeepDuration: keepDuration,
		}})
		if createErr != nil {
			return fmt.Errorf("create target Dataset: %w", createErr)
		}
		if err := ensureMetadataSuccess("create target Dataset", created.GetRetInfo()); err != nil {
			return err
		}
		target.Dataset = created.GetDataset()
	} else if err := ensureMetadataSuccess("get target Dataset", target.GetRetInfo()); err != nil {
		return err
	}
	if target.GetDataset() == nil {
		return errors.New("target Dataset is empty")
	}
	if err := validateTargetDataset(target.GetDataset(), attrs, targetFreq.Storage, "crypto", source.DataNodeID); err != nil {
		return err
	}
	// Mirror the source subject snapshot before activation so the target has the
	// same universe. Read existing memberships first so an unchanged target does
	// not issue hundreds of redundant metadata writes on every timer tick.
	existingSubjects := make(map[string]*storagepb.DatasetSubject)
	for page := uint32(1); ; page++ {
		bindings, listErr := c.Metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{AuthInfo: c.Auth, SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID, Page: &storagepb.Page{Page: page, Size: 500}})
		if listErr != nil {
			return fmt.Errorf("list target Dataset subjects: %w", listErr)
		}
		if err := ensureMetadataSuccess("list target Dataset subjects", bindings.GetRetInfo()); err != nil {
			return err
		}
		for _, binding := range bindings.GetDatasetSubjects() {
			if binding != nil && strings.TrimSpace(binding.GetSubjectId()) != "" {
				existingSubjects[binding.GetSubjectId()] = binding
			}
		}
		if bindings.GetPageResult() == nil || !bindings.GetPageResult().GetHasMore() || len(bindings.GetDatasetSubjects()) == 0 {
			break
		}
	}
	desiredSubjects := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		if strings.TrimSpace(subject.SubjectID) == "" || (strings.TrimSpace(subject.Status) != "" && !strings.EqualFold(subject.Status, "active")) {
			continue
		}
		desiredSubjects[subject.SubjectID] = struct{}{}
		if current := existingSubjects[subject.SubjectID]; current != nil && strings.EqualFold(strings.TrimSpace(current.GetStatus()), "active") {
			continue
		}
		binding := &storagepb.DatasetSubject{SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID, SubjectId: subject.SubjectID, SubjectRole: "normal", Status: "active"}
		resp, bindErr := c.Metadata.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{AuthInfo: c.Auth, DatasetSubject: binding})
		if bindErr != nil {
			return fmt.Errorf("bind target Dataset subject: %w", bindErr)
		}
		if err := ensureMetadataSuccess("bind target Dataset subject", resp.GetRetInfo()); err != nil {
			return err
		}
	}
	// Disable memberships that disappeared from the source snapshot. Keeping
	// the rows (rather than deleting them) preserves metadata history while
	// preventing Planner and View from treating stale symbols as active.
	for _, binding := range existingSubjects {
		if binding == nil || !strings.EqualFold(strings.TrimSpace(binding.GetStatus()), "active") {
			continue
		}
		if _, keep := desiredSubjects[binding.GetSubjectId()]; keep {
			continue
		}
		copy, ok := proto.Clone(binding).(*storagepb.DatasetSubject)
		if !ok {
			return fmt.Errorf("clone stale target Dataset subject failed")
		}
		copy.Status = "disabled"
		if resp, bindErr := c.Metadata.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{AuthInfo: c.Auth, DatasetSubject: copy}); bindErr != nil {
			return fmt.Errorf("disable stale target Dataset subject: %w", bindErr)
		} else if err := ensureMetadataSuccess("disable stale target Dataset subject", resp.GetRetInfo()); err != nil {
			return err
		}
	}
	for _, field := range klineFields {
		resp, callErr := c.Metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: c.Auth, Column: &storagepb.DatasetColumn{
			SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID, ColumnName: field.name,
			OriginType: storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD, OriginId: field.name,
			ValueType: field.type_, Required: true, Status: "active", Attributes: map[string]string{"display_name": field.label},
		}})
		if callErr != nil {
			return fmt.Errorf("upsert target column %s: %w", field.name, callErr)
		}
		if err := ensureMetadataSuccess("upsert target column", resp.GetRetInfo()); err != nil {
			return err
		}
	}
	check, err := c.Metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: c.Auth, SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID})
	if err != nil {
		return err
	}
	if err := ensureMetadataSuccess("check target Dataset activation", check.GetRetInfo()); err != nil {
		return err
	}
	if !check.GetReady() {
		return fmt.Errorf("target Dataset activation is not ready")
	}
	if strings.ToLower(strings.TrimSpace(target.GetDataset().GetStatus())) != "active" {
		activated, activateErr := c.Metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: c.Auth, SpaceId: rule.SpaceID, DatasetId: params.TargetDatasetID, ExpectedRevision: target.GetDataset().GetRevision()})
		if activateErr != nil {
			return fmt.Errorf("activate target Dataset: %w", activateErr)
		}
		if err := ensureMetadataSuccess("activate target Dataset", activated.GetRetInfo()); err != nil {
			return err
		}
	}
	viewID := DefaultTargetViewID(params.TargetDatasetID)
	viewResp, viewErr := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: rule.SpaceID, ViewId: viewID})
	if viewErr != nil {
		return viewErr
	}
	if viewResp.GetRetInfo() == nil {
		return errors.New("get target View: empty ret_info")
	}
	if viewResp.GetRetInfo().GetCode() == storagepb.ErrorCode_VIEW_NOT_FOUND || viewResp.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		created, createErr := c.Metadata.CreateView(ctx, &storagepb.CreateViewReq{AuthInfo: c.Auth, View: &storagepb.View{
			SpaceId: rule.SpaceID, ViewId: viewID, Name: uniqueResampleDisplayName(params.TargetDatasetID), Description: "Collector生成的K线重采样查询视图", PrimaryDatasetId: params.TargetDatasetID,
			DatasetIds: []string{params.TargetDatasetID}, GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, FilterJson: fmt.Sprintf(`{"freq":%q}`, targetFreq.Storage),
			Engine: "duckdb", KeepDuration: keepDuration, Status: "active", Attributes: map[string]string{"route_ready_request_id": "kline-resample-route:" + rule.RuleID + ":" + fmt.Sprint(target.GetDataset().GetRevision())},
		}})
		if createErr != nil {
			return createErr
		}
		if err := ensureMetadataSuccess("create target View", created.GetRetInfo()); err != nil {
			return err
		}
		viewResp.View = created.GetView()
	} else if err := ensureMetadataSuccess("get target View", viewResp.GetRetInfo()); err != nil {
		return err
	} else if err := validateTargetView(viewResp.GetView(), rule, params, targetFreq.Storage); err != nil {
		return err
	}
	requestID := "kline-resample-route:" + rule.RuleID + ":" + fmt.Sprint(target.GetDataset().GetRevision())
	if viewResp.GetView() != nil && viewResp.GetView().GetAttributes()["route_ready_request_id"] != requestID {
		updated := *viewResp.GetView()
		updated.Attributes = cloneStringMap(updated.GetAttributes())
		updated.Attributes["route_ready_request_id"] = requestID
		updatedResp, updateErr := c.Metadata.UpdateView(ctx, &storagepb.UpdateViewReq{AuthInfo: c.Auth, View: &updated})
		if updateErr != nil {
			return fmt.Errorf("update target View route-ready marker: %w", updateErr)
		}
		if err := ensureMetadataSuccess("update target View route-ready marker", updatedResp.GetRetInfo()); err != nil {
			return err
		}
	}
	for index, field := range klineFields {
		originID := params.TargetDatasetID + "." + field.name
		resp, callErr := c.Metadata.UpsertViewColumn(ctx, &storagepb.UpsertViewColumnReq{AuthInfo: c.Auth, Column: &storagepb.ViewColumn{
			SpaceId: rule.SpaceID, ViewId: viewID, ColumnName: originID, OriginType: storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
			OriginId: originID, ValueType: field.type_, SortOrder: uint32(index + 1), Attributes: map[string]string{"display_name": field.label},
		}})
		if callErr != nil {
			return callErr
		}
		if err := ensureMetadataSuccess("upsert target View column", resp.GetRetInfo()); err != nil {
			return err
		}
	}
	// Upserting a View column advances desired_view_revision. Re-read the
	// authoritative revision after the complete desired schema is written; the
	// earlier GetView response may describe a stale definition.
	finalViewResp, finalViewErr := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.Auth, SpaceId: rule.SpaceID, ViewId: viewID})
	if finalViewErr != nil {
		return fmt.Errorf("get target View after columns: %w", finalViewErr)
	}
	if err := ensureMetadataSuccess("get target View after columns", finalViewResp.GetRetInfo()); err != nil {
		return err
	}
	finalView := finalViewResp.GetView()
	if finalView == nil {
		return errors.New("target View after columns is empty")
	}
	if c.ViewSync == nil {
		return errors.New("resample catalog View sync waiter is required")
	}
	syncResp, syncErr := c.ViewSync.WaitViewSyncPoint(ctx, &storagepb.WaitViewSyncPointReq{
		AuthInfo: c.Auth, SpaceId: rule.SpaceID, ViewId: viewID, RequestId: requestID,
		DatasetIds: []string{params.TargetDatasetID}, WaitTimeoutMs: 5000,
	})
	if syncErr != nil {
		return fmt.Errorf("wait target View sync point: %w", syncErr)
	}
	if syncResp == nil {
		return ErrTargetViewNotReady
	}
	if err := ensureMetadataSuccess("wait target View sync point", syncResp.GetRetInfo()); err != nil {
		return err
	}
	if !syncResp.GetReady() {
		return ErrTargetViewNotReady
	}
	if err := waitTargetViewRevision(ctx, c.Metadata, c.Auth, rule.SpaceID, viewID, finalView.GetDesiredViewRevision(), 5*time.Second); err != nil {
		return err
	}
	return nil
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

func validateTargetView(view *storagepb.View, rule domain.TaskRule, params *domain.CollectParams, frequency string) error {
	if view == nil {
		return errors.New("target View is empty")
	}
	if view.GetPrimaryDatasetId() != params.TargetDatasetID || len(view.GetDatasetIds()) != 1 || view.GetDatasetIds()[0] != params.TargetDatasetID {
		return errors.New("target View immutable Dataset contract does not match rule")
	}
	if view.GetFilterJson() != fmt.Sprintf(`{"freq":%q}`, frequency) || view.GetEngine() != "duckdb" {
		return errors.New("target View immutable frequency contract does not match rule")
	}
	wantGrain := []string{"subject_id", "freq", "data_time", "series_tag"}
	if len(view.GetGrainKeys()) != len(wantGrain) {
		return errors.New("target View grain contract does not match rule")
	}
	for i := range wantGrain {
		if view.GetGrainKeys()[i] != wantGrain[i] {
			return errors.New("target View grain contract does not match rule")
		}
	}
	_ = rule
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
		return fmt.Errorf("target Dataset data source does not match rule: got %q want %q", dataset.GetDataSourceId(), dataSourceID)
	}
	if strings.TrimSpace(dataNodeID) != "" && strings.TrimSpace(dataset.GetDataNodeId()) != strings.TrimSpace(dataNodeID) {
		return fmt.Errorf("target Dataset data node does not match source: got %q want %q", dataset.GetDataNodeId(), dataNodeID)
	}
	for key, expected := range want {
		if dataset.GetAttributes()[key] != expected {
			return fmt.Errorf("target Dataset immutable lineage attribute %s does not match rule", key)
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
