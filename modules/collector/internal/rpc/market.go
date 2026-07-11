package rpc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	commonpb "github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/marketmanifest"
)

func (s *Service) ListMarketModules(_ context.Context, _ *pb.ListMarketModulesReq) (*pb.ListMarketModulesRsp, error) {
	ids := make([]string, 0, len(s.manifests))
	for id := range s.manifests {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]*pb.MarketModule, 0, len(ids))
	for _, id := range ids {
		manifest := s.manifests[id]
		result = append(result, marketModule(manifest.MarketID, manifest.SpaceID, manifest.Readiness.Status, manifest.RuntimeEnabled, manifest.InstrumentTypes))
	}
	return &pb.ListMarketModulesRsp{RetInfo: retOK(), Modules: result}, nil
}

func (s *Service) GetMarketStatus(_ context.Context, req *pb.GetMarketStatusReq) (*pb.GetMarketStatusRsp, error) {
	manifest, ok := s.manifests[strings.TrimSpace(req.GetMarketId())]
	if !ok {
		return &pb.GetMarketStatusRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "market not found")}, nil
	}
	count := int32(0)
	if manifest.RuntimeEnabled && manifest.Readiness.CapabilityEnabled {
		count = int32(len(manifest.Providers))
	}
	return &pb.GetMarketStatusRsp{RetInfo: retOK(), Module: marketModule(manifest.MarketID, manifest.SpaceID, manifest.Readiness.Status, manifest.RuntimeEnabled, manifest.InstrumentTypes), RuntimeProviderCount: count}, nil
}

func marketModule(marketID, spaceID, status string, enabled bool, instrumentTypes []string) *pb.MarketModule {
	return &pb.MarketModule{MarketId: marketID, SpaceId: spaceID, ReadinessStatus: status, RuntimeEnabled: enabled, InstrumentTypes: append([]string(nil), instrumentTypes...)}
}

type marketQueryScope struct {
	MarketID       string   `json:"market_id"`
	InstrumentType []string `json:"instrument_types"`
	Subjects       []string `json:"subjects"`
	Frequency      string   `json:"frequency"`
	Start          string   `json:"start"`
	End            string   `json:"end"`
	Order          string   `json:"order"`
}

type queriedKline struct {
	row        *pb.MarketKline
	datasetID  string
	dimensions map[string]string
}

func (s *Service) QueryMarketKlines(ctx context.Context, req *pb.QueryMarketKlinesReq) (*pb.QueryMarketKlinesRsp, error) {
	manifest, ok := s.manifests[strings.TrimSpace(req.GetMarketId())]
	if !ok {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "market not found")}, nil
	}
	if len(req.GetSubjectIds()) == 0 || req.GetFrequency() == "" || req.GetStartTime() == "" || req.GetEndTime() == "" {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "subjects, frequency and time range are required")}, nil
	}
	start, err := time.Parse(time.RFC3339, req.GetStartTime())
	if err != nil {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid start_time")}, nil
	}
	end, err := time.Parse(time.RFC3339, req.GetEndTime())
	if err != nil || end.Before(start) {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid end_time")}, nil
	}
	instrumentTypes := normalizedStrings(req.GetInstrumentTypes())
	if len(instrumentTypes) == 0 {
		instrumentTypes = normalizedStrings(manifest.InstrumentTypes)
	}
	subjects := normalizedSubjectIDs(req.GetSubjectIds())
	order := strings.ToLower(strings.TrimSpace(req.GetOrder()))
	if order == "" {
		order = "asc"
	}
	if order != "asc" && order != "desc" {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "order must be asc or desc")}, nil
	}
	scope := marketQueryScope{MarketID: manifest.MarketID, InstrumentType: instrumentTypes, Subjects: subjects, Frequency: req.GetFrequency(), Start: start.UTC().Format(time.RFC3339Nano), End: end.UTC().Format(time.RFC3339Nano), Order: order}
	hash := marketQueryHash(scope)
	now := s.now().UTC()
	cursor := marketCursor{QueryHash: hash, QueryAsOf: now.Format(time.RFC3339Nano)}
	if req.GetCursor() != "" {
		cursor, err = decodeMarketCursor(req.GetCursor())
		if err != nil || cursor.QueryHash != hash {
			return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "cursor does not match query")}, nil
		}
	}
	rows, err := s.readLogicalMarketKlines(ctx, manifest.SpaceID, manifest.Feeds, instrumentTypes, subjects, req.GetFrequency(), start.UTC(), end.UTC())
	if err != nil {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	sort.Slice(rows, func(i, j int) bool {
		left, right := marketRowTuple(rows[i]), marketRowTuple(rows[j])
		if order == "desc" {
			return left > right
		}
		return left < right
	})
	if cursor.BoundaryKey != "" {
		found := false
		for _, row := range rows {
			if marketRowTuple(row) == cursor.BoundaryKey {
				found = marketRowDigest(row) == cursor.BoundaryDigest && row.row.GetResolvedAt() <= cursor.QueryAsOf
				break
			}
		}
		if !found {
			return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "data_changed_restart_query")}, nil
		}
	}
	if cursor.Offset > len(rows) {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "data_changed_restart_query")}, nil
	}
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	finish := cursor.Offset + pageSize
	if finish > len(rows) {
		finish = len(rows)
	}
	pageRows := rows[cursor.Offset:finish]
	result := make([]*pb.MarketKline, 0, len(pageRows))
	for _, row := range pageRows {
		result = append(result, row.row)
	}
	next := ""
	if finish < len(rows) && len(pageRows) > 0 {
		last := pageRows[len(pageRows)-1]
		cursor.Offset, cursor.BoundaryKey, cursor.BoundaryDigest = finish, marketRowTuple(last), marketRowDigest(last)
		next, _ = encodeMarketCursor(cursor)
	}
	return &pb.QueryMarketKlinesRsp{RetInfo: retOK(), Rows: result, NextCursor: next, QueryAsOf: cursor.QueryAsOf, Freshness: "stored", CoverageStatus: "unknown"}, nil
}

func (s *Service) readLogicalMarketKlines(ctx context.Context, spaceID string, feeds []marketmanifest.Feed, instrumentTypes, subjects []string, frequency string, start, end time.Time) ([]queriedKline, error) {
	allowedTypes := map[string]bool{}
	for _, value := range instrumentTypes {
		allowedTypes[value] = true
	}
	result := []queriedKline{}
	for _, feed := range feeds {
		if feed.DatasetID == "" || !allowedTypes[strings.ToLower(feed.InstrumentType)] || !containsString(feed.Frequencies, frequency) {
			continue
		}
		keys := make([]*storagepb.TimeSeriesKey, 0, len(subjects))
		for _, subject := range subjects {
			keys = append(keys, &storagepb.TimeSeriesKey{SpaceId: spaceID, DatasetId: feed.DatasetID, SubjectId: subject, Freq: frequency})
		}
		rsp, err := s.storageAccess.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{Keys: keys, TimeRange: &storagepb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)}, Page: &commonpb.Page{Page: 1, Size: 1000}})
		if err != nil {
			return nil, err
		}
		if !storageRetOK(rsp.GetRetInfo()) {
			return nil, fmt.Errorf("read market dataset %s: %s", feed.DatasetID, rsp.GetRetInfo().GetMsg())
		}
		for _, source := range rsp.GetRows() {
			columns := columnMap(source.GetColumns())
			result = append(result, queriedKline{datasetID: feed.DatasetID, dimensions: source.GetKey().GetDimensions(), row: &pb.MarketKline{
				MarketId: spaceID, InstrumentType: feed.InstrumentType, SubjectId: source.GetKey().GetSubjectId(), Frequency: source.GetKey().GetFreq(), DataTime: source.GetKey().GetDataTime(),
				Open: stringValueColumn(columns, "open_exact"), High: stringValueColumn(columns, "high_exact"), Low: stringValueColumn(columns, "low_exact"), Close: stringValueColumn(columns, "close_exact"), Volume: stringValueColumn(columns, "volume_exact"), Amount: stringValueColumn(columns, "amount_exact"),
				SourceProvider: stringValueColumn(columns, "source_provider"), QualityStatus: stringValueColumn(columns, "quality_status"), Revision: intValueColumn(columns, "revision"), ResolvedAt: timeValueColumn(columns, "resolved_at"),
			}})
		}
	}
	return result, nil
}

func (s *Service) RefreshMarketKlines(ctx context.Context, req *pb.RefreshMarketKlinesReq) (*pb.RefreshMarketKlinesRsp, error) {
	if err := s.requireLeader(ctx); err != nil {
		return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	manifest, ok := s.manifests[strings.TrimSpace(req.GetMarketId())]
	if !ok {
		return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "market not found")}, nil
	}
	if !manifest.RuntimeEnabled || !manifest.Readiness.CapabilityEnabled {
		return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "market is not runtime ready")}, nil
	}
	start, startErr := time.Parse(time.RFC3339, req.GetStartTime())
	end, endErr := time.Parse(time.RFC3339, req.GetEndTime())
	if startErr != nil || endErr != nil || !end.After(start) || req.GetFrequency() == "" || len(req.GetSubjectIds()) == 0 {
		return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "subjects, frequency and valid range are required")}, nil
	}
	wantedDatasets := map[string]bool{}
	wantedTypes := normalizedStrings(req.GetInstrumentTypes())
	if len(wantedTypes) == 0 {
		wantedTypes = normalizedStrings(manifest.InstrumentTypes)
	}
	for _, feed := range manifest.Feeds {
		if containsString(wantedTypes, strings.ToLower(feed.InstrumentType)) && containsString(feed.Frequencies, req.GetFrequency()) {
			wantedDatasets[feed.DatasetID] = true
		}
	}
	instances := make([]domain.TaskInstance, 0, len(req.GetSubjectIds()))
	now := s.now().UTC()
	preparer := taskpublisher.OutboxLeasePreparer{Leases: s.marketControl}
	for _, subject := range normalizedSubjectIDs(req.GetSubjectIds()) {
		values, _, err := s.instanceRepo.List(ctx, repository.TaskInstanceFilter{SpaceID: manifest.SpaceID, SubjectID: subject, Interval: req.GetFrequency(), DataType: "kline", PageSize: 1000})
		if err != nil {
			return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
		}
		matched := false
		for _, instance := range values {
			if !wantedDatasets[instance.DatasetID] {
				continue
			}
			params := map[string]any{}
			if err := json.Unmarshal([]byte(instance.TaskParams), &params); err != nil {
				return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
			}
			params["start_time"], params["end_time"] = start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
			params["schedule_window"] = "refresh:" + now.Format(time.RFC3339Nano)
			raw, _ := json.Marshal(params)
			outboxID := refreshExecutionID(instance.TaskID, string(raw))
			prepared, err := preparer.Prepare(ctx, domain.AttemptOutbox{OutboxID: outboxID, Payload: string(raw)}, now)
			if err != nil {
				return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
			}
			instance.TaskParams = prepared.Payload
			instances = append(instances, instance)
			matched = true
		}
		if !matched {
			return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "no planned logical task for subject "+subject)}, nil
		}
	}
	jobIDs, err := s.cloudJobs.SubmitCollectorJobItems(ctx, instances)
	if err != nil {
		return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	if err := s.instanceRepo.UpdateCloudJobItemIDs(ctx, manifest.SpaceID, jobIDs); err != nil {
		return &pb.RefreshMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	_, _ = s.cloudJobs.WakeCollectorNodes(ctx, taskpublisher.WakeOptions{SpaceID: manifest.SpaceID, JobTypes: []string{"kline"}})
	ids := make([]string, 0, len(instances))
	for _, instance := range instances {
		ids = append(ids, instance.TaskID)
	}
	return &pb.RefreshMarketKlinesRsp{RetInfo: retOK(), TaskIds: ids}, nil
}

func refreshExecutionID(taskID, payload string) string {
	sum := sha256.Sum256([]byte(taskID + "|" + payload))
	return hex.EncodeToString(sum[:])
}

func (s *Service) ListTaskAttempts(ctx context.Context, req *pb.ListTaskAttemptsReq) (*pb.ListTaskAttemptsRsp, error) {
	limit := int(req.GetPageSize())
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	offset, _ := strconv.Atoi(req.GetCursor())
	var values []domain.MarketAttempt
	query := s.db.WithContext(ctx).Order("c_finalized_at DESC").Limit(limit + 1).Offset(offset)
	if req.GetTaskId() != "" {
		query = query.Where("c_job_item_id LIKE ?", req.GetTaskId()+"%")
	}
	if err := query.Find(&values).Error; err != nil {
		return &pb.ListTaskAttemptsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	next := ""
	if len(values) > limit {
		values = values[:limit]
		next = strconv.Itoa(offset + limit)
	}
	result := make([]*pb.MarketAttemptReceipt, 0, len(values))
	for _, value := range values {
		result = append(result, attemptReceiptPB(repository.AttemptReceipt{Attempt: value}))
	}
	return &pb.ListTaskAttemptsRsp{RetInfo: retOK(), Attempts: result, NextCursor: next}, nil
}

func normalizedStrings(values []string) []string {
	seen, result := map[string]bool{}, []string{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func normalizedSubjectIDs(values []string) []string {
	seen, result := map[string]bool{}, []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func marketRowTuple(value queriedKline) string {
	row := value.row
	return strings.Join([]string{row.GetDataTime(), row.GetInstrumentType(), row.GetSubjectId(), row.GetFrequency(), value.datasetID}, "|")
}

func marketRowDigest(value queriedKline) string {
	row := value.row
	sum := sha256.Sum256([]byte(strings.Join([]string{marketRowTuple(value), strconv.FormatInt(row.GetRevision(), 10), row.GetResolvedAt(), row.GetSourceProvider(), row.GetOpen(), row.GetHigh(), row.GetLow(), row.GetClose(), row.GetVolume(), row.GetAmount()}, "|")))
	return hex.EncodeToString(sum[:])
}

func columnMap(columns []*storagepb.ColumnValue) map[string]*storagepb.TypedValue {
	result := make(map[string]*storagepb.TypedValue, len(columns))
	for _, column := range columns {
		result[column.GetColumnName()] = column.GetValue()
	}
	return result
}

func storageRetOK(ret *commonpb.RetInfo) bool {
	return ret != nil && ret.GetCode() == commonpb.ErrorCode_SUCCESS
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func stringValueColumn(values map[string]*storagepb.TypedValue, key string) string {
	if values[key] == nil {
		return ""
	}
	return values[key].GetStringValue()
}
func timeValueColumn(values map[string]*storagepb.TypedValue, key string) string {
	if values[key] == nil {
		return ""
	}
	return values[key].GetTimeValue()
}
func intValueColumn(values map[string]*storagepb.TypedValue, key string) int64 {
	if values[key] == nil {
		return 0
	}
	return values[key].GetIntValue()
}
