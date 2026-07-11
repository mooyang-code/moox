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
	cursorKey  string
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
		if err := s.validateMarketCursorBoundary(ctx, manifest.SpaceID, cursor); err != nil {
			return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "data_changed_restart_query")}, nil
		}
		if err := s.validateMarketCursorPrefixes(ctx, manifest, req.GetFrequency(), start.UTC(), end.UTC(), order, cursor); err != nil {
			return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "data_changed_restart_query")}, nil
		}
	}
	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = 100
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	rows, nextOffsets, hasMore, err := s.readLogicalMarketKlinePage(ctx, manifest.SpaceID, manifest.Feeds, instrumentTypes, subjects, req.GetFrequency(), start.UTC(), end.UTC(), order, pageSize, cursor.DatasetOffsets)
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
	rows = dedupeQueriedKlines(rows)
	queryAsOf, _ := time.Parse(time.RFC3339Nano, cursor.QueryAsOf)
	if req.GetCursor() != "" {
		for _, row := range rows {
			resolvedAt, err := time.Parse(time.RFC3339Nano, row.row.GetResolvedAt())
			if err == nil && resolvedAt.After(queryAsOf) {
				return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "data_changed_restart_query")}, nil
			}
		}
	}
	finish := pageSize
	if finish > len(rows) {
		finish = len(rows)
	}
	pageRows := rows[:finish]
	result := make([]*pb.MarketKline, 0, len(pageRows))
	for _, row := range pageRows {
		result = append(result, row.row)
	}
	next := ""
	if hasMore && len(pageRows) > 0 {
		last := pageRows[len(pageRows)-1]
		cursor.Offset += finish
		cursor.BoundaryKey, cursor.BoundaryDigest = marketRowTuple(last), marketRowDigest(last)
		cursor.DatasetOffsets = nextOffsets
		if cursor.StreamPrefixDigests == nil {
			cursor.StreamPrefixDigests = map[string]string{}
		}
		for _, row := range pageRows {
			cursor.StreamPrefixDigests[row.cursorKey] = chainStreamDigest(cursor.StreamPrefixDigests[row.cursorKey], marketRowDigest(row))
		}
		cursor.BoundaryDataset, cursor.BoundaryInstrument = last.datasetID, last.row.GetInstrumentType()
		cursor.BoundarySubject, cursor.BoundaryFrequency, cursor.BoundaryDataTime = last.row.GetSubjectId(), last.row.GetFrequency(), last.row.GetDataTime()
		cursor.BoundaryDimensions = last.dimensions
		next, _ = encodeMarketCursor(cursor)
	}
	coverageStatus, missingRanges, err := s.readMarketCoverage(ctx, manifest, instrumentTypes, subjects, req.GetFrequency(), start.UTC(), end.UTC())
	if err != nil {
		return &pb.QueryMarketKlinesRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, err.Error())}, nil
	}
	return &pb.QueryMarketKlinesRsp{RetInfo: retOK(), Rows: result, NextCursor: next, QueryAsOf: cursor.QueryAsOf, Freshness: marketFreshness(rows, cursor.QueryAsOf), CoverageStatus: coverageStatus, MissingRanges: missingRanges}, nil
}

func (s *Service) validateMarketCursorPrefixes(ctx context.Context, manifest marketmanifest.Manifest, frequency string, start, end time.Time, order string, cursor marketCursor) error {
	feeds := map[string]marketmanifest.Feed{}
	for _, feed := range manifest.Feeds {
		feeds[feed.DatasetID] = feed
	}
	for cursorKey, offset := range cursor.DatasetOffsets {
		if offset == 0 {
			continue
		}
		parts := strings.SplitN(cursorKey, "\x00", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid stream cursor")
		}
		feed, ok := feeds[parts[0]]
		if !ok {
			return fmt.Errorf("unknown stream dataset")
		}
		digest, seen := "", 0
		for page := uint32(1); seen < offset; page++ {
			rsp, err := s.storageAccess.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{Keys: []*storagepb.TimeSeriesKey{{SpaceId: manifest.SpaceID, DatasetId: parts[0], SubjectId: parts[1], Freq: frequency}}, TimeRange: &storagepb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)}, Order: mapMarketStorageOrder(order), Page: &commonpb.Page{Page: page, Size: 1000}})
			if err != nil || !storageRetOK(rsp.GetRetInfo()) || len(rsp.GetRows()) == 0 {
				return fmt.Errorf("stream prefix unavailable")
			}
			for _, source := range rsp.GetRows() {
				if seen >= offset {
					break
				}
				columns := columnMap(source.GetColumns())
				value := queriedKline{datasetID: parts[0], dimensions: source.GetKey().GetDimensions(), row: &pb.MarketKline{MarketId: manifest.SpaceID, InstrumentType: feed.InstrumentType, SubjectId: source.GetKey().GetSubjectId(), Frequency: source.GetKey().GetFreq(), DataTime: source.GetKey().GetDataTime(), Open: stringValueColumn(columns, "open_exact"), High: stringValueColumn(columns, "high_exact"), Low: stringValueColumn(columns, "low_exact"), Close: stringValueColumn(columns, "close_exact"), Volume: stringValueColumn(columns, "volume_exact"), Amount: stringValueColumn(columns, "amount_exact"), SourceProvider: stringValueColumn(columns, "source_provider"), QualityStatus: stringValueColumn(columns, "quality_status"), Revision: intValueColumn(columns, "revision"), ResolvedAt: timeValueColumn(columns, "resolved_at")}}
				digest = chainStreamDigest(digest, marketRowDigest(value))
				seen++
			}
		}
		if seen != offset || digest != cursor.StreamPrefixDigests[cursorKey] {
			return fmt.Errorf("stream prefix changed")
		}
	}
	return nil
}

func chainStreamDigest(previous, rowDigest string) string {
	sum := sha256.Sum256([]byte(previous + "|" + rowDigest))
	return hex.EncodeToString(sum[:])
}

func (s *Service) validateMarketCursorBoundary(ctx context.Context, spaceID string, cursor marketCursor) error {
	if cursor.BoundaryDataset == "" {
		return nil
	}
	rsp, err := s.storageAccess.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{Keys: []*storagepb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: cursor.BoundaryDataset, SubjectId: cursor.BoundarySubject, Freq: cursor.BoundaryFrequency, Dimensions: cursor.BoundaryDimensions, DataTime: cursor.BoundaryDataTime}}, Page: &commonpb.Page{Page: 1, Size: 1}})
	if err != nil || !storageRetOK(rsp.GetRetInfo()) || len(rsp.GetRows()) != 1 {
		return fmt.Errorf("boundary not found")
	}
	source := rsp.GetRows()[0]
	columns := columnMap(source.GetColumns())
	value := queriedKline{datasetID: cursor.BoundaryDataset, dimensions: source.GetKey().GetDimensions(), row: &pb.MarketKline{MarketId: spaceID, InstrumentType: cursor.BoundaryInstrument, SubjectId: source.GetKey().GetSubjectId(), Frequency: source.GetKey().GetFreq(), DataTime: source.GetKey().GetDataTime(), Open: stringValueColumn(columns, "open_exact"), High: stringValueColumn(columns, "high_exact"), Low: stringValueColumn(columns, "low_exact"), Close: stringValueColumn(columns, "close_exact"), Volume: stringValueColumn(columns, "volume_exact"), Amount: stringValueColumn(columns, "amount_exact"), SourceProvider: stringValueColumn(columns, "source_provider"), QualityStatus: stringValueColumn(columns, "quality_status"), Revision: intValueColumn(columns, "revision"), ResolvedAt: timeValueColumn(columns, "resolved_at")}}
	if marketRowDigest(value) != cursor.BoundaryDigest {
		return fmt.Errorf("boundary changed")
	}
	return nil
}

func (s *Service) readMarketCoverage(ctx context.Context, manifest marketmanifest.Manifest, instrumentTypes, subjects []string, frequency string, start, end time.Time) (string, []string, error) {
	coverageDataset := ""
	for _, dataset := range manifest.Datasets {
		if dataset.Role == "coverage_state" && dataset.Feed == "coverage" {
			coverageDataset = dataset.ID
			break
		}
	}
	if coverageDataset == "" {
		return "unknown", nil, nil
	}
	keys := []*storagepb.RecordKey{}
	for _, feed := range manifest.Feeds {
		if !containsString(instrumentTypes, strings.ToLower(feed.InstrumentType)) || !containsString(feed.Frequencies, frequency) {
			continue
		}
		for _, subject := range subjects {
			for day := start.UTC().Truncate(24 * time.Hour); !day.After(end.UTC()); day = day.Add(24 * time.Hour) {
				partitionID := day.Format("2006-01-02")
				sum := sha256.Sum256([]byte(feed.DatasetID + "|" + subject + "|" + frequency + "|" + partitionID))
				keys = append(keys, &storagepb.RecordKey{SpaceId: manifest.SpaceID, DatasetId: coverageDataset, RecordId: hex.EncodeToString(sum[:])})
			}
		}
	}
	if len(keys) == 0 {
		return "unknown", nil, nil
	}
	rsp, err := s.storageAccess.ReadRecordRows(ctx, &storagepb.ReadRecordRowsReq{Keys: keys, Order: storagepb.SortOrder_SORT_ORDER_DESC, Page: &commonpb.Page{Page: 1, Size: uint32(len(keys))}})
	if err != nil {
		return "", nil, err
	}
	if !storageRetOK(rsp.GetRetInfo()) {
		return "", nil, fmt.Errorf("read market coverage: %s", rsp.GetRetInfo().GetMsg())
	}
	status, missing := "complete", []string{}
	seen := map[string]bool{}
	for _, row := range rsp.GetRows() {
		if seen[row.GetKey().GetRecordId()] {
			continue
		}
		seen[row.GetKey().GetRecordId()] = true
		columns := columnMap(row.GetColumns())
		value := stringValueColumn(columns, "coverage_status")
		if value != "complete" {
			status = firstString(value, "unknown")
		}
		if raw := stringValueColumn(columns, "missing_ranges"); raw != "" && raw != "[]" {
			missing = append(missing, raw)
		}
	}
	if len(seen) < len(keys) && status == "complete" {
		status = "unknown"
	}
	return status, missing, nil
}

func marketFreshness(rows []queriedKline, queryAsOf string) string {
	if len(rows) == 0 {
		return "empty"
	}
	latest := time.Time{}
	for _, row := range rows {
		if value, err := time.Parse(time.RFC3339Nano, row.row.GetResolvedAt()); err == nil && value.After(latest) {
			latest = value
		}
	}
	asOf, _ := time.Parse(time.RFC3339Nano, queryAsOf)
	if !latest.IsZero() && asOf.Sub(latest) <= 24*time.Hour {
		return "fresh"
	}
	return "stale"
}

func (s *Service) readLogicalMarketKlinePage(ctx context.Context, spaceID string, feeds []marketmanifest.Feed, instrumentTypes, subjects []string, frequency string, start, end time.Time, order string, pageSize int, offsets map[string]int) ([]queriedKline, map[string]int, bool, error) {
	allowedTypes := map[string]bool{}
	for _, value := range instrumentTypes {
		allowedTypes[value] = true
	}
	result := []queriedKline{}
	storageHasMore := false
	for _, feed := range feeds {
		if feed.DatasetID == "" || !allowedTypes[strings.ToLower(feed.InstrumentType)] || !containsString(feed.Frequencies, frequency) {
			continue
		}
		for _, subject := range subjects {
			cursorKey := feed.DatasetID + "\x00" + subject
			offset := offsets[cursorKey]
			storagePageSize := pageSize
			page := offset/storagePageSize + 1
			within := offset % storagePageSize
			var sources []*storagepb.TimeSeriesRow
			for len(sources) < pageSize+1 {
				rsp, err := s.storageAccess.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{Keys: []*storagepb.TimeSeriesKey{{SpaceId: spaceID, DatasetId: feed.DatasetID, SubjectId: subject, Freq: frequency}}, TimeRange: &storagepb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)}, Order: mapMarketStorageOrder(order), Page: &commonpb.Page{Page: uint32(page), Size: uint32(storagePageSize)}})
				if err != nil {
					return nil, nil, false, err
				}
				if !storageRetOK(rsp.GetRetInfo()) {
					return nil, nil, false, fmt.Errorf("read market dataset %s: %s", feed.DatasetID, rsp.GetRetInfo().GetMsg())
				}
				pageRows := rsp.GetRows()
				if within > len(pageRows) {
					return nil, nil, false, fmt.Errorf("market dataset %s changed during pagination", feed.DatasetID)
				}
				pageRows = pageRows[within:]
				sources = append(sources, pageRows...)
				within = 0
				if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
					break
				}
				storageHasMore = true
				page++
			}
			if len(sources) > pageSize+1 {
				sources = sources[:pageSize+1]
			}
			for _, source := range sources {
				columns := columnMap(source.GetColumns())
				result = append(result, queriedKline{datasetID: feed.DatasetID, cursorKey: cursorKey, dimensions: source.GetKey().GetDimensions(), row: &pb.MarketKline{
					MarketId: spaceID, InstrumentType: feed.InstrumentType, SubjectId: source.GetKey().GetSubjectId(), Frequency: source.GetKey().GetFreq(), DataTime: source.GetKey().GetDataTime(), CanonicalDimensions: source.GetKey().GetDimensions(),
					Open: stringValueColumn(columns, "open_exact"), High: stringValueColumn(columns, "high_exact"), Low: stringValueColumn(columns, "low_exact"), Close: stringValueColumn(columns, "close_exact"), Volume: stringValueColumn(columns, "volume_exact"), Amount: stringValueColumn(columns, "amount_exact"),
					SourceProvider: stringValueColumn(columns, "source_provider"), QualityStatus: stringValueColumn(columns, "quality_status"), Revision: intValueColumn(columns, "revision"), ResolvedAt: timeValueColumn(columns, "resolved_at"),
				}})
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if order == "desc" {
			return marketRowTuple(result[i]) > marketRowTuple(result[j])
		}
		return marketRowTuple(result[i]) < marketRowTuple(result[j])
	})
	result = dedupeQueriedKlines(result)
	nextOffsets := make(map[string]int, len(offsets)+len(feeds))
	for key, value := range offsets {
		nextOffsets[key] = value
	}
	consume := pageSize
	if consume > len(result) {
		consume = len(result)
	}
	for _, row := range result[:consume] {
		nextOffsets[row.cursorKey]++
	}
	return result, nextOffsets, storageHasMore || len(result) > pageSize, nil
}

func mapMarketStorageOrder(order string) storagepb.SortOrder {
	if order == "desc" {
		return storagepb.SortOrder_SORT_ORDER_DESC
	}
	return storagepb.SortOrder_SORT_ORDER_ASC
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
	return strings.Join([]string{row.GetDataTime(), row.GetInstrumentType(), row.GetSubjectId(), row.GetFrequency(), canonicalDimensions(value.dimensions)}, "|")
}

func canonicalDimensions(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, "&")
}

func marketRowDigest(value queriedKline) string {
	row := value.row
	sum := sha256.Sum256([]byte(strings.Join([]string{marketRowTuple(value), strconv.FormatInt(row.GetRevision(), 10), row.GetResolvedAt(), row.GetSourceProvider(), row.GetOpen(), row.GetHigh(), row.GetLow(), row.GetClose(), row.GetVolume(), row.GetAmount()}, "|")))
	return hex.EncodeToString(sum[:])
}

func prefixDigest(values []queriedKline) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = hash.Write([]byte(marketRowTuple(value)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(marketRowDigest(value)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func dedupeQueriedKlines(values []queriedKline) []queriedKline {
	if len(values) < 2 {
		return values
	}
	out := values[:0]
	last := ""
	for _, value := range values {
		key := marketRowTuple(value)
		if key == last {
			continue
		}
		last = key
		out = append(out, value)
	}
	return out
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
