package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

// subjectCatalogClient is optional so lightweight embedders and existing
// tests do not have to implement the broader MetadataClient surface. The
// production metadata proxy implements it and lets period backfills constrain
// one time-first Primary scan to the dataset's declared subject universe.
type subjectCatalogClient interface {
	ListSubjects(context.Context, *pb.ListSubjectsReq, ...client.Option) (*pb.ListSubjectsRsp, error)
}

// errActiveContractUnavailable is a startup-fatal metadata condition. A
// legacy view already mid-rebuild must not fall back to its desired contract:
// doing so would acknowledge rows/markers against the wrong View revision.
var errActiveContractUnavailable = errors.New("active view contract unavailable")

func (s *Service) BackfillView(ctx context.Context, spaceID, viewID string, batchSize int) error {
	return s.BackfillViewWithReader(ctx, spaceID, viewID, batchSize, nil)
}

func (s *Service) BackfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader) error {
	_, err := s.backfillViewWithReader(ctx, spaceID, viewID, batchSize, reader, nil, 0, 0, defaultMaxHistoryScanRows)
	return err
}

func (s *Service) backfillViewWithReader(ctx context.Context, spaceID, viewID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, minimumLookback time.Duration, lookbackPeriods, maxHistoryScanRows uint64) (uint64, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return 0, errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	activeID, nextID := runtime.active, runtime.next
	runtime.mu.Unlock()
	if nextID == "" {
		return 0, errors.New("view has no pending build")
	}

	// Every time-series rebuild must use Primary for the configured lookback;
	// copying the active index can reproduce a short or stale View. Record
	// Bleve Views retain the active-copy path because they have no time-series
	// Primary history reader.
	if activeID != "" && (minimumLookback > 0 || lookbackPeriods > 0) {
		nextEngine, err := s.engineFor(nextID)
		if err != nil {
			return 0, err
		}
		if !strings.EqualFold(strings.TrimSpace(nextEngine.Engine()), "bleve") {
			if reader == nil {
				return 0, errors.New("Primary field reader is required for a time-series View rebuild")
			}
			if rangeReader == nil {
				return 0, errors.New("Primary history range reader is required for a time-series View rebuild")
			}
			activeID = ""
		}
	}
	if activeID != "" && rangeReader != nil {
		if _, err := s.engineFor(activeID); err != nil {
			activeID = ""
		}
	}

	var written uint64
	if activeID != "" {
		active, err := s.engineFor(activeID)
		if err != nil {
			return 0, err
		}
		next, err := s.engineFor(nextID)
		if err != nil {
			return 0, err
		}
		s.mu.RLock()
		nextSchema := s.schemas[nextID]
		activeSchema := s.schemas[activeID]
		catalogView := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
		s.mu.RUnlock()
		for _, timeRange := range backfillTimeRanges(catalogView, minimumLookback) {
			var after *pb.RowKey
			for {
				rows, _, err := active.Query(ctx, activeID, viewindex.QuerySpec{
					AfterKey: after, TimeRange: timeRange, Sorts: backfillSorts(active.Engine()), Limit: batchSize, TotalMode: pb.TotalMode_NONE,
				})
				if err != nil {
					return written, fmt.Errorf("query active view %q for backfill: %w", activeID, err)
				}
				if len(rows) == 0 {
					break
				}
				writes := make([]viewindex.RowWrite, 0, len(rows))
				for _, row := range rows {
					if row == nil || row.GetKey() == nil {
						continue
					}
					writes = append(writes, viewindex.RowWrite{
						Key:        viewindex.RowKey{Key: proto.Clone(row.GetKey()).(*pb.RowKey)},
						Fields:     projectBackfillFields(row.GetFields(), activeSchema, nextSchema),
						Attributes: row.GetAttributes(),
					})
				}
				if reader != nil && len(writes) > 0 {
					if err := s.enrichBackfillRows(ctx, reader, activeID, nextID, writes); err != nil {
						return written, err
					}
				}
				for offset := 0; offset < len(writes); offset += 256 {
					end := offset + 256
					if end > len(writes) {
						end = len(writes)
					}
					if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
						return written, err
					}
					if err := s.writeIndex(ctx, nextID, next, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
						return written, fmt.Errorf("write view backfill %q: %w", nextID, err)
					}
					written += uint64(end - offset)
				}
				if len(rows) < batchSize {
					break
				}
				after = proto.Clone(rows[len(rows)-1].GetKey()).(*pb.RowKey)
			}
		}
	} else {
		next, err := s.engineFor(nextID)
		if err != nil {
			return 0, err
		}
		if strings.EqualFold(strings.TrimSpace(next.Engine()), "bleve") {
			log.Printf("storage record View %s/%s starts empty: no Primary history reader", spaceID, viewID)
		} else {
			if reader == nil {
				return 0, errors.New("Primary field reader is required for a time-series View rebuild")
			}
			written, err = s.backfillPrimaryHistory(ctx, spaceID, viewID, nextID, batchSize, reader, rangeReader, minimumLookback, lookbackPeriods, maxHistoryScanRows)
			if err != nil {
				return written, err
			}
		}
	}
	runtime.mu.Lock()
	if runtime.next != nextID || runtime.buildFailed {
		runtime.mu.Unlock()
		return written, errViewBuildFailed
	}
	runtime.status = "ready"
	runtime.mu.Unlock()
	return written, nil
}

func (s *Service) backfillPrimaryHistory(ctx context.Context, spaceID, viewID, nextID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, minimumLookback time.Duration, lookbackPeriods, maxHistoryScanRows uint64) (uint64, error) {
	if reader == nil && (minimumLookback > 0 || lookbackPeriods > 0) {
		return 0, errors.New("Primary field reader is required for a time-series View history backfill")
	}
	if rangeReader == nil {
		if minimumLookback <= 0 && lookbackPeriods == 0 {
			return 0, nil
		}
		return 0, errors.New("Primary history range reader is required for a View without an active index")
	}
	s.mu.RLock()
	view := s.catalogViews[viewRef{spaceID: spaceID, viewID: viewID}]
	nextSchema := s.schemas[nextID]
	auth := s.primaryAuth
	if auth != nil {
		auth = proto.Clone(auth).(*pb.AuthInfo)
	}
	s.mu.RUnlock()
	if view == nil || view.GetPrimaryDatasetId() == "" {
		return 0, errors.New("primary dataset is required for View history backfill")
	}
	if lookbackPeriods > 0 {
		frequency := viewFrequencyValue(view)
		if frequency != "" {
			return s.backfillPrimaryHistoryByPeriods(ctx, spaceID, viewID, nextID, batchSize, reader, rangeReader, view, nextSchema, auth, frequency, lookbackPeriods, maxHistoryScanRows)
		}
		return 0, errors.New("time-series View frequency is required for period-based Primary history backfill")
	}
	ranges := backfillTimeRanges(view, minimumLookback)
	// The history runtime narrows scans by physical bucket. Use one logical
	// range here instead of issuing one full Primary scan per five-minute build
	// chunk; writes are still committed in small batches below.
	if len(ranges) > 1 {
		ranges = []*pb.TimeRange{{StartTime: ranges[0].GetStartTime(), EndTime: ranges[len(ranges)-1].GetEndTime()}}
	}
	var written uint64
	for _, timeRange := range ranges {
		var afterKey []byte
		for {
			if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
				return written, err
			}
			rsp, err := rangeReader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
				AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(), TimeRange: timeRange,
				Order: pb.SortOrder_SORT_ORDER_ASC, Page: &pb.Page{Page: 1, Size: uint32(batchSize)}, AfterKey: afterKey,
			})
			if err != nil {
				return written, fmt.Errorf("scan Primary history for %s/%s: %w", spaceID, view.GetPrimaryDatasetId(), err)
			}
			if err := requireSuccess(rsp.GetRetInfo()); err != nil {
				return written, fmt.Errorf("scan Primary history for %s/%s: %w", spaceID, view.GetPrimaryDatasetId(), err)
			}
			writes := make([]viewindex.RowWrite, 0, len(rsp.GetRows()))
			for _, row := range rsp.GetRows() {
				if row == nil || row.GetKey() == nil {
					continue
				}
				key := row.GetKey()
				writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{
					SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
					Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()}},
				}}, Fields: nil})
			}
			if len(writes) > 0 && reader != nil {
				if err := s.enrichBackfillRows(ctx, reader, "", nextID, writes); err != nil {
					return written, err
				}
			}
			for offset := 0; offset < len(writes); offset += 256 {
				end := offset + 256
				if end > len(writes) {
					end = len(writes)
				}
				if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
					return written, err
				}
				engine, err := s.engineFor(nextID)
				if err != nil {
					return written, err
				}
				if err := s.writeIndex(ctx, nextID, engine, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
					return written, fmt.Errorf("write Primary history backfill %q: %w", nextID, err)
				}
				written += uint64(end - offset)
			}
			if len(rsp.GetRows()) == 0 || rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
				break
			}
			last := rsp.GetRows()[len(rsp.GetRows())-1].GetKey()
			if last == nil {
				break
			}
			afterKey, err = proto.Marshal(&pb.RowKey{SpaceId: last.GetSpaceId(), DatasetId: last.GetDatasetId(), Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: last.GetSubjectId(), Freq: last.GetFreq(), DataTime: last.GetDataTime(), SeriesTag: last.GetSeriesTag()}}})
			if err != nil {
				return written, fmt.Errorf("encode Primary history cursor: %w", err)
			}
		}
	}
	return written, nil
}

// backfillPrimaryHistoryByPeriods rebuilds the most recent completed bars for
// every subject bound to the View's primary dataset. The Primary scan is
// descending so weekends, holidays, and other market gaps do not consume the
// configured bar budget. The subject catalog constrains one physical scan;
// Primary rows remain authoritative for series and tag coverage.
func (s *Service) backfillPrimaryHistoryByPeriods(ctx context.Context, spaceID, viewID, nextID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, view *pb.View, nextSchema viewindex.ViewIndexSchema, auth *pb.AuthInfo, frequency string, periods, maxHistoryScanRows uint64) (uint64, error) {
	// Use the metadata subject binding table to avoid repeating the time-first
	// Primary walk for every subject. If it is unavailable or empty, fall back
	// to an unconstrained Primary scan rather than publishing an incomplete B.
	expected := make(map[string]struct{})
	counts := make(map[string]uint64, len(expected))
	var selectors []*pb.TimeSeriesSelector
	if metadata := s.metadataClientSnapshot(); metadata != nil {
		if catalog, ok := metadata.(subjectCatalogClient); ok {
			subjects, err := s.listBackfillSubjectCatalog(ctx, catalog, auth, view.GetSpaceId(), view.GetPrimaryDatasetId())
			if err != nil {
				return 0, fmt.Errorf("load Primary subject catalog for %s/%s: %w", view.GetSpaceId(), view.GetPrimaryDatasetId(), err)
			}
			if len(subjects) == 0 {
				// An empty catalog is valid for a dataset that has not produced any
				// rows yet (for example a newly-created system-metrics dataset).
				// Probe Primary before failing: empty Primary means there is simply
				// nothing to backfill, while non-empty Primary falls back to the
				// authoritative scan rather than activating a partial index.
				probe, probeErr := rangeReader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
					AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
					Order: pb.SortOrder_SORT_ORDER_DESC, Page: &pb.Page{Page: 1, Size: 1},
				})
				if probeErr != nil {
					return 0, fmt.Errorf("probe Primary history for %s/%s: %w", view.GetSpaceId(), view.GetPrimaryDatasetId(), probeErr)
				}
				if err := requireSuccess(probe.GetRetInfo()); err != nil {
					return 0, fmt.Errorf("probe Primary history for %s/%s: %w", view.GetSpaceId(), view.GetPrimaryDatasetId(), err)
				}
				if len(probe.GetRows()) == 0 {
					log.Printf("storage view history backfill has no Primary rows space=%s dataset=%s", view.GetSpaceId(), view.GetPrimaryDatasetId())
					return 0, nil
				}
				log.Printf("storage view history backfill subject catalog is empty; using authoritative Primary scan space=%s dataset=%s", view.GetSpaceId(), view.GetPrimaryDatasetId())
			}
			if len(subjects) > 0 {
				// The subject-first Primary history index lets us read each bound
				// subject without repeating a full time-first dataset scan. The helper
				// discovers all tags for that subject and writes only the configured
				// latest periods per series.
				return s.backfillPrimaryHistoryBySubjectCatalog(ctx, spaceID, viewID, nextID, batchSize, reader, rangeReader, view, nextSchema, auth, frequency, subjects, periods)
			}
		}
	}
	useCatalog := len(selectors) > 0
	knownSubjects := make(map[string]struct{}, len(selectors))
	for _, selector := range selectors {
		knownSubjects[selector.GetSubjectId()] = struct{}{}
	}
	var afterKey []byte
	var written uint64
	var scanned uint64
	if maxHistoryScanRows == 0 {
		maxHistoryScanRows = defaultMaxHistoryScanRows
	}
	if useCatalog {
		// A complete dataset subject catalog gives us a bounded selector universe,
		// while the time-first Primary history index may still contain many older
		// rows before EOF. Do not apply the generic safety fuse here: doing so would
		// reject the normal 5M-row Binance history before discovering quiet/older
		// series. The per-series write quota remains the hard output bound.
		maxHistoryScanRows = ^uint64(0)
	}
	for {
		if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
			return written, err
		}
		rsp, err := rangeReader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
			AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(), Selectors: selectors,
			Order: pb.SortOrder_SORT_ORDER_DESC, Page: &pb.Page{Page: 1, Size: uint32(batchSize)}, AfterKey: afterKey,
		})
		if err != nil {
			return written, fmt.Errorf("scan Primary history for %s/%s by periods (scanned=%d written=%d): %w", spaceID, view.GetPrimaryDatasetId(), scanned, written, err)
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return written, fmt.Errorf("scan Primary history for %s/%s by periods (scanned=%d written=%d): %w", spaceID, view.GetPrimaryDatasetId(), scanned, written, err)
		}
		rows := rsp.GetRows()
		if scanned+uint64(len(rows)) > maxHistoryScanRows {
			return written, fmt.Errorf("Primary history scan exceeded %d rows before completing %d-bar coverage; subject catalog is unavailable or incomplete", maxHistoryScanRows, periods)
		}
		scanned += uint64(len(rows))
		writes := make([]viewindex.RowWrite, 0, len(rows))
		for _, row := range rows {
			if row == nil || row.GetKey() == nil {
				continue
			}
			key := row.GetKey()
			if useCatalog {
				if _, ok := knownSubjects[key.GetSubjectId()]; !ok {
					continue
				}
			}
			seriesKey := periodSeriesIdentity(key.GetSubjectId(), key.GetFreq(), key.GetSeriesTag())
			if !strings.EqualFold(strings.TrimSpace(key.GetFreq()), strings.TrimSpace(frequency)) {
				continue
			}
			expected[seriesKey] = struct{}{}
			if counts[seriesKey] >= periods {
				continue
			}
			counts[seriesKey]++
			writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{
				SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
				Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()}},
			}}})
		}
		if len(writes) > 0 && reader != nil {
			if err := s.enrichBackfillRows(ctx, reader, "", nextID, writes); err != nil {
				return written, fmt.Errorf("enrich Primary history for %s/%s (rows=%d written=%d): %w", spaceID, view.GetPrimaryDatasetId(), len(writes), written, err)
			}
		}
		for offset := 0; offset < len(writes); offset += 256 {
			end := offset + 256
			if end > len(writes) {
				end = len(writes)
			}
			if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
				return written, err
			}
			engine, err := s.engineFor(nextID)
			if err != nil {
				return written, err
			}
			if err := s.writeIndex(ctx, nextID, engine, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
				return written, fmt.Errorf("write Primary period backfill %q: %w", nextID, err)
			}
			written += uint64(end - offset)
		}
		if len(rows) == 0 || rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			partial, empty := periodCoverageGaps(expected, counts, periods)
			if len(partial) > 0 {
				return written, fmt.Errorf("Primary history has fewer than %d bars for %d series (first missing: %s; %d series have no matching bars: %s)", periods, len(partial), formatPeriodCoverageCounts(partial, counts), len(empty), formatPeriodSeriesKeys(empty))
			}
			if len(expected) == 0 {
				return written, fmt.Errorf("Primary history has no matching %s bars in %s/%s", frequency, view.GetSpaceId(), view.GetPrimaryDatasetId())
			}
			// A dataset subject catalog can contain instruments that have not
			// produced a bar at this frequency yet. They must not block a
			// rebuild for series that do have data.
			if len(empty) > 0 {
				log.Printf("storage view history backfill skipped %d series without matching %s bars space=%s view=%s", len(empty), frequency, spaceID, viewID)
			}
			return written, nil
		}
		// Do not finish early based on the series discovered so far. The history
		// index is ordered by time, so a quiet subject or an older series_tag may
		// appear only after the currently active series have reached their quota.
		// Scanning to EOF is what makes the per-series lookback guarantee true;
		// the row cap still bounds how much data can be returned and written.
		last := rows[len(rows)-1].GetKey()
		if last == nil {
			return written, errors.New("Primary history page ended without a cursor key")
		}
		afterKey, err = marshalTimeSeriesHistoryCursor(last)
		if err != nil {
			return written, fmt.Errorf("encode Primary history period cursor: %w", err)
		}
	}
}

// backfillPrimaryHistoryBySubjectCatalog uses the subject-first Primary
// history index. It avoids rescanning a multi-million-row dataset for every
// page while still walking each bound subject to EOF, so quiet subjects and
// older series tags cannot be mistaken for complete coverage.
func (s *Service) backfillPrimaryHistoryBySubjectCatalog(ctx context.Context, spaceID, viewID, nextID string, batchSize int, reader FieldReader, rangeReader TimeSeriesRangeReader, view *pb.View, nextSchema viewindex.ViewIndexSchema, auth *pb.AuthInfo, frequency string, subjects []string, periods uint64) (uint64, error) {
	if reader == nil || rangeReader == nil {
		return 0, errors.New("Primary readers are required for subject-catalog history backfill")
	}
	var written uint64
	for _, subject := range subjects {
		counts := make(map[string]uint64)
		var afterKey []byte
		for {
			if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
				return written, err
			}
			rsp, err := rangeReader.ReadTimeSeriesRows(ctx, &pb.ReadTimeSeriesRowsReq{
				AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
				Selectors: []*pb.TimeSeriesSelector{{SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(), SubjectId: subject, Freq: frequency}},
				Order:     pb.SortOrder_SORT_ORDER_DESC, Page: &pb.Page{Page: 1, Size: 10000}, AfterKey: afterKey,
			})
			if err != nil {
				return written, fmt.Errorf("scan Primary history for %s/%s subject %s: %w", spaceID, view.GetPrimaryDatasetId(), subject, err)
			}
			if err := requireSuccess(rsp.GetRetInfo()); err != nil {
				return written, fmt.Errorf("scan Primary history for %s/%s subject %s: %w", spaceID, view.GetPrimaryDatasetId(), subject, err)
			}
			rows := rsp.GetRows()
			writes := make([]viewindex.RowWrite, 0, len(rows))
			for _, row := range rows {
				if row == nil || row.GetKey() == nil || !strings.EqualFold(strings.TrimSpace(row.GetKey().GetFreq()), strings.TrimSpace(frequency)) {
					continue
				}
				key := row.GetKey()
				seriesKey := periodSeriesIdentity(key.GetSubjectId(), key.GetFreq(), key.GetSeriesTag())
				if counts[seriesKey] >= periods {
					continue
				}
				counts[seriesKey]++
				writes = append(writes, viewindex.RowWrite{Key: viewindex.RowKey{Key: &pb.RowKey{
					SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
					Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag()}},
				}}})
			}
			if len(writes) > 0 {
				if err := s.enrichBackfillRows(ctx, reader, "", nextID, writes); err != nil {
					return written, fmt.Errorf("enrich Primary history for %s/%s subject %s: %w", spaceID, view.GetPrimaryDatasetId(), subject, err)
				}
			}
			for offset := 0; offset < len(writes); offset += 256 {
				end := offset + 256
				if end > len(writes) {
					end = len(writes)
				}
				if err := s.backfillStillActive(spaceID, viewID, nextID); err != nil {
					return written, err
				}
				engine, err := s.engineFor(nextID)
				if err != nil {
					return written, err
				}
				if err := s.writeIndex(ctx, nextID, engine, viewindex.ViewIndexWriteBatch{RowWrites: writes[offset:end], ViewRevision: nextSchema.ViewVersion, ViewSchemaHash: nextSchema.SchemaHash, WriteMode: viewindex.Backfill}); err != nil {
					return written, fmt.Errorf("write Primary period backfill %q: %w", nextID, err)
				}
				written += uint64(end - offset)
			}
			if len(rows) == 0 || rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
				break
			}
			last := rows[len(rows)-1].GetKey()
			if last == nil {
				return written, errors.New("Primary subject history page ended without a cursor key")
			}
			afterKey, err = marshalTimeSeriesHistoryCursor(last)
			if err != nil {
				return written, fmt.Errorf("encode Primary subject history cursor: %w", err)
			}
		}
		for seriesKey, count := range counts {
			if count < periods {
				return written, fmt.Errorf("Primary history has fewer than %d bars for series %s", periods, seriesKey)
			}
		}
	}
	return written, nil
}

// marshalTimeSeriesHistoryCursor converts the public range-read key into the
// RowKey envelope consumed by the Primary history cursor. Range responses use
// TimeSeriesKey, while the opaque after_key contract is deliberately shared
// with DataNode's RowKey-based history index.
func marshalTimeSeriesHistoryCursor(key *pb.TimeSeriesKey) ([]byte, error) {
	if key == nil {
		return nil, errors.New("time-series history cursor key is nil")
	}
	return proto.Marshal(&pb.RowKey{
		SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(),
		Kind: &pb.RowKey_TimeSeries{TimeSeries: &pb.TimeSeriesRowKey{
			SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), DataTime: key.GetDataTime(), SeriesTag: key.GetSeriesTag(),
		}},
	})
}

func (s *Service) listBackfillSubjects(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, spaceID, datasetID string) ([]string, error) {
	const pageSize uint32 = 1000
	seen := make(map[string]struct{})
	for page := uint32(1); ; page++ {
		if page > 10000 {
			return nil, errors.New("too many dataset subject pages during View history backfill")
		}
		rsp, err := metadata.ListDatasetSubjects(ctx, &pb.ListDatasetSubjectsReq{AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID, Page: &pb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, fmt.Errorf("list subjects for %s/%s: %w", spaceID, datasetID, err)
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, fmt.Errorf("list subjects for %s/%s: %w", spaceID, datasetID, err)
		}
		for _, subject := range rsp.GetDatasetSubjects() {
			if subject == nil || strings.TrimSpace(subject.GetSubjectId()) == "" || strings.EqualFold(strings.TrimSpace(subject.GetStatus()), "deleted") {
				continue
			}
			seen[strings.TrimSpace(subject.GetSubjectId())] = struct{}{}
		}
		pageResult := rsp.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() || len(rsp.GetDatasetSubjects()) == 0 {
			break
		}
	}
	result := make([]string, 0, len(seen))
	for subjectID := range seen {
		result = append(result, subjectID)
	}
	sort.Strings(result)
	return result, nil
}

func (s *Service) listBackfillSubjectCatalog(ctx context.Context, metadata subjectCatalogClient, auth *pb.AuthInfo, spaceID, datasetID string) ([]string, error) {
	if datasetMetadata, ok := metadata.(MetadataClient); ok {
		// Prefer the dataset binding table when it is populated. The global
		// subject table is only a fallback because a subject can be registered in
		// both spot and swap markets while retaining one metadata row.
		if subjects, err := s.listBackfillSubjects(ctx, datasetMetadata, auth, spaceID, datasetID); err == nil && len(subjects) > 0 {
			return subjects, nil
		}
	}
	const pageSize uint32 = 1000
	market := ""
	lowerDatasetID := strings.ToLower(strings.TrimSpace(datasetID))
	if strings.Contains(lowerDatasetID, "_spot_") || strings.HasSuffix(lowerDatasetID, "_spot") {
		market = "spot"
	} else if strings.Contains(lowerDatasetID, "_swap_") || strings.HasSuffix(lowerDatasetID, "_swap") {
		market = "swap"
	}
	seen := make(map[string]struct{})
	for page := uint32(1); ; page++ {
		if page > 10000 {
			return nil, errors.New("too many subject pages during View history backfill")
		}
		rsp, err := metadata.ListSubjects(ctx, &pb.ListSubjectsReq{AuthInfo: auth, SpaceId: spaceID, Market: market, Page: &pb.Page{Page: page, Size: pageSize}})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, subject := range rsp.GetSubjects() {
			if subject == nil || strings.TrimSpace(subject.GetSubjectId()) == "" || strings.EqualFold(strings.TrimSpace(subject.GetStatus()), "deleted") {
				continue
			}
			seen[strings.TrimSpace(subject.GetSubjectId())] = struct{}{}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetSubjects()) == 0 {
			break
		}
	}
	result := make([]string, 0, len(seen))
	for subjectID := range seen {
		result = append(result, subjectID)
	}
	sort.Strings(result)
	return result, nil
}

func buildPeriodHistorySelectors(spaceID, datasetID, frequency string, subjects []string) ([]*pb.TimeSeriesSelector, map[string]struct{}) {
	selectors := make([]*pb.TimeSeriesSelector, 0, len(subjects))
	expected := make(map[string]struct{}, len(subjects))
	for _, subjectID := range subjects {
		selectors = append(selectors, &pb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subjectID, Freq: frequency})
		expected[periodSeriesKey(subjectID, frequency)] = struct{}{}
	}
	return selectors, expected
}

func periodSeriesKey(subjectID, frequency string) string {
	return strings.TrimSpace(subjectID) + "\x00" + strings.ToLower(strings.TrimSpace(frequency))
}

func periodSeriesIdentity(subjectID, frequency, seriesTag string) string {
	return strings.TrimSpace(subjectID) + "\x00" + strings.ToLower(strings.TrimSpace(frequency)) + "\x00" + strings.TrimSpace(seriesTag)
}

// periodCoverageGaps separates started series that are short from catalog
// series with no matching Primary bars. Empty series are not a coverage gap:
// there is no historical data to backfill for them, and treating them as
// incomplete would make one newly cataloged symbol block every View rebuild.
func periodCoverageGaps(expected map[string]struct{}, counts map[string]uint64, periods uint64) (partial, empty []string) {
	for key := range expected {
		switch {
		case counts[key] == 0:
			empty = append(empty, key)
		case counts[key] < periods:
			partial = append(partial, key)
		}
	}
	sort.Strings(partial)
	sort.Strings(empty)
	return partial, empty
}

func formatPeriodSeriesKey(key string) string {
	parts := strings.Split(key, "\x00")
	if len(parts) >= 2 {
		formatted := parts[0] + "/" + parts[1]
		if len(parts) >= 3 && parts[2] != "" {
			formatted += "/" + parts[2]
		}
		return formatted
	}
	return key
}

func formatPeriodSeriesKeys(keys []string) string {
	limit := minInt(len(keys), 3)
	formatted := make([]string, 0, limit)
	for _, key := range keys[:limit] {
		formatted = append(formatted, formatPeriodSeriesKey(key))
	}
	return strings.Join(formatted, ",")
}

func formatPeriodCoverageCounts(keys []string, counts map[string]uint64) string {
	limit := minInt(len(keys), 3)
	formatted := make([]string, 0, limit)
	for _, key := range keys[:limit] {
		formatted = append(formatted, fmt.Sprintf("%s=%d", formatPeriodSeriesKey(key), counts[key]))
	}
	return strings.Join(formatted, ",")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var errViewBuildFailed = errors.New("view build has been marked failed")

func (s *Service) backfillStillActive(spaceID, viewID, nextID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errViewBuildFailed
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next != nextID || runtime.buildFailed {
		return errViewBuildFailed
	}
	return nil
}

func backfillTimeRanges(view *pb.View, minimumLookback time.Duration) []*pb.TimeRange {
	var keep time.Duration
	if view != nil && view.GetKeepDuration() != "" && view.GetKeepDuration() != "0" {
		parsed, err := time.ParseDuration(view.GetKeepDuration())
		if err == nil && parsed > 0 {
			keep = parsed
		}
	}
	if minimumLookback > keep {
		keep = minimumLookback
	}
	if keep <= 0 {
		return []*pb.TimeRange{nil}
	}
	now := time.Now().UTC()
	// Market bars are minute/hour aligned. Align the lower boundary before
	// paging so a rebuild does not miss the first bar merely because the wall
	// clock included fractional seconds.
	start := now.Add(-keep).Truncate(time.Minute)
	const chunk = 5 * time.Minute
	ranges := make([]*pb.TimeRange, 0, int(keep/chunk)+1)
	for start.Before(now) {
		end := start.Add(chunk)
		if end.After(now) {
			end = now
		}
		ranges = append(ranges, &pb.TimeRange{StartTime: start.Format(time.RFC3339Nano), EndTime: end.Format(time.RFC3339Nano)})
		start = end
	}
	return ranges
}

func backfillSorts(engine string) []*pb.SortSpec {
	if engine == "bleve" {
		return []*pb.SortSpec{{FieldName: "record_id"}, {FieldName: "version"}}
	}
	return []*pb.SortSpec{
		{FieldName: "data_time"}, {FieldName: "subject_id"}, {FieldName: "freq"}, {FieldName: "series_tag"},
		{FieldName: "record_id"}, {FieldName: "version"},
	}
}

func (s *Service) enrichBackfillRows(ctx context.Context, reader FieldReader, activeID, nextID string, writes []viewindex.RowWrite) error {
	s.mu.RLock()
	activeSchema := s.schemas[activeID]
	nextSchema := s.schemas[nextID]
	s.mu.RUnlock()
	activeColumns := make(map[string]viewColumnShape, len(activeSchema.Columns))
	for _, column := range activeSchema.Columns {
		if column != nil {
			activeColumns[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	type requestedField struct {
		source string
		target string
	}
	byDataset := make(map[string][]requestedField)
	for _, column := range nextSchema.Columns {
		if column == nil {
			continue
		}
		if active, exists := activeColumns[column.GetColumnName()]; exists && active.equal(viewColumnShapeOf(column)) {
			continue
		}
		datasetID := viewColumnDataset(column)
		source := viewColumnSource(column, datasetID)
		if datasetID != "" && source != "" {
			byDataset[datasetID] = append(byDataset[datasetID], requestedField{source: source, target: column.GetColumnName()})
		}
	}
	for datasetID, fields := range byDataset {
		fieldIDs := make([]string, 0, len(fields))
		targets := make(map[string]string, len(fields))
		for _, field := range fields {
			fieldIDs = append(fieldIDs, field.source)
			targets[field.source] = field.target
		}
		s.mu.RLock()
		auth := s.primaryAuth
		if auth != nil {
			auth = proto.Clone(auth).(*pb.AuthInfo)
		}
		s.mu.RUnlock()
		if auth == nil {
			return errors.New("primary auth is not configured")
		}
		// Primary bounds a read request at 100,000 key-field pairs. Keep
		// enrichment below that limit even for wide Views and large rebuild
		// pages instead of failing the whole build.
		chunkSize := len(writes)
		if len(fieldIDs) > 0 && chunkSize > 100000/len(fieldIDs) {
			chunkSize = 100000 / len(fieldIDs)
			if chunkSize == 0 {
				chunkSize = 1
			}
		}
		// The key-field limit is not a latency budget. A 100k-pair request
		// can still exceed the PrimaryStore RPC deadline on a busy Pebble
		// host, causing the whole A/B build to self-cancel. Keep individual
		// point-read chunks small; the outer history page remains 10k keys.
		if chunkSize > 512 {
			chunkSize = 512
		}
		for chunkStart := 0; chunkStart < len(writes); chunkStart += chunkSize {
			chunkEnd := chunkStart + chunkSize
			if chunkEnd > len(writes) {
				chunkEnd = len(writes)
			}
			keys := make([]*pb.RowKey, 0, chunkEnd-chunkStart)
			positions := make(map[string]int, chunkEnd-chunkStart)
			for index := chunkStart; index < chunkEnd; index++ {
				key := proto.Clone(writes[index].Key.Key).(*pb.RowKey)
				key.DatasetId = datasetID
				keys = append(keys, key)
				positions[viewindex.RowKeyID(key)] = index
			}
			rsp, err := reader.ReadFields(ctx, &pb.PrimaryReadFieldsReq{AuthInfo: auth, Keys: keys, FieldIds: fieldIDs})
			if err != nil {
				return err
			}
			if err := requireSuccess(rsp.GetRetInfo()); err != nil {
				return err
			}
			for _, row := range rsp.GetRows() {
				position, ok := positions[viewindex.RowKeyID(row.GetKey())]
				if !ok {
					continue
				}
				for _, field := range row.GetFields() {
					if target := targets[field.GetFieldId()]; target != "" {
						writes[position].Fields = append(writes[position].Fields, &pb.FieldValue{FieldId: target, Value: field.GetValue()})
					}
				}
			}
		}
	}
	return nil
}

type viewColumnShape struct {
	origin     string
	originType pb.ColumnOriginType
	valueType  pb.FieldValueType
}

func viewColumnShapeOf(column *pb.ViewColumn) viewColumnShape {
	if column == nil {
		return viewColumnShape{}
	}
	return viewColumnShape{origin: column.GetOriginId(), originType: column.GetOriginType(), valueType: column.GetValueType()}
}

func (s viewColumnShape) equal(other viewColumnShape) bool {
	return s.origin == other.origin && s.originType == other.originType && s.valueType == other.valueType
}

func projectBackfillFields(fields []*pb.FieldValue, active, next viewindex.ViewIndexSchema) []*pb.FieldValue {
	activeShapes := make(map[string]viewColumnShape, len(active.Columns))
	for _, column := range active.Columns {
		if column != nil {
			activeShapes[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	nextShapes := make(map[string]viewColumnShape, len(next.Columns))
	for _, column := range next.Columns {
		if column != nil {
			nextShapes[column.GetColumnName()] = viewColumnShapeOf(column)
		}
	}
	projected := make([]*pb.FieldValue, 0, len(fields))
	for _, field := range fields {
		if field == nil {
			continue
		}
		name := field.GetFieldId()
		nextShape, ok := nextShapes[name]
		activeShape, activeOK := activeShapes[name]
		if ok && activeOK && activeShape.equal(nextShape) {
			projected = append(projected, field)
		}
	}
	return projected
}

func (s *Service) SwitchView(ctx context.Context, spaceID, viewID string, grace time.Duration) error {
	_ = grace
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	_, _, err := s.switchViewLocked(context.WithoutCancel(ctx), runtime)
	if err != nil {
		runtime.mu.Unlock()
		return err
	}
	runtime.mu.Unlock()
	// The in-memory and metadata switch is already committed. Do not report a
	// cancellation from the caller's context as a failed build: doing so would
	// discard the newly active index after a successful Activate RPC.
	return nil
}

func (s *Service) switchViewLocked(ctx context.Context, runtime *viewRuntime) (string, uint64, error) {
	if runtime == nil || runtime.next == "" || runtime.status != "ready" || runtime.buildFailed {
		return "", 0, errors.New("no completed view build to switch")
	}
	oldID := runtime.active
	oldGeneration := s.indexGenerationOf(oldID)
	if oldID != "" {
		if err := s.retireIndex(ctx, oldID, oldGeneration); err != nil {
			return "", 0, fmt.Errorf("mark old view index retiring: %w", err)
		}
	}
	runtime.active = runtime.next
	runtime.activeDatasetIDs = append([]string(nil), runtime.nextDatasetIDs...)
	runtime.activePrimaryDatasetID = runtime.nextPrimaryDatasetID
	runtime.activeDatasetSet = true
	runtime.statsIndexID = ""
	runtime.stats = viewindex.ViewIndexStats{}
	runtime.next = ""
	runtime.nextDatasetIDs = nil
	runtime.nextPrimaryDatasetID = ""
	runtime.status = "active"
	runtime.buildID = ""
	runtime.ownerID = ""
	runtime.metadata = nil
	runtime.metadataAuth = nil
	runtime.buildFailed = false
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	runtime.buildCancel = nil
	runtime.buildContext = nil
	return oldID, oldGeneration, nil
}

func (s *Service) TrackViewBuild(ctx context.Context, spaceID, viewID, buildID, ownerID string, metadata MetadataClient, auth *pb.AuthInfo) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil || metadata == nil || buildID == "" || ownerID == "" {
		return errors.New("view build tracking requires runtime, metadata, build_id and owner_id")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	buildCtx, cancel := context.WithCancel(ctx)
	runtime.buildContext = buildCtx
	runtime.buildCancel = cancel
	runtime.buildFailed = false
	runtime.buildID = buildID
	runtime.ownerID = ownerID
	runtime.metadata = metadata
	if auth != nil {
		runtime.metadataAuth = proto.Clone(auth).(*pb.AuthInfo)
	}
	return nil
}

func (s *Service) failRuntimeBuild(ctx context.Context, key viewRef, runtime *viewRuntime, cause error) error {
	if runtime == nil || runtime.metadata == nil || runtime.buildID == "" || runtime.ownerID == "" {
		return errors.New("view build failure cannot be persisted")
	}
	if current, err := s.readActiveView(ctx, runtime.metadata, runtime.metadataAuth, key.spaceID, key.viewID); err == nil {
		if build := current.GetIndexBuild(); build != nil && build.GetBuildId() == runtime.buildID && build.GetState() == pb.ViewIndexBuild_FAILED {
			// A previous redelivery may have committed the failure while its RPC
			// response was lost. Treat the state as idempotently persisted so the
			// caller can discard the failed inactive slot.
			runtime.buildFailed = true
			runtime.status = "failing"
			return nil
		}
	}
	runtime.buildFailed = true
	runtime.status = "failing"
	if runtime.buildCancel != nil {
		runtime.buildCancel()
	}
	message := "new view live write failed"
	if cause != nil {
		message = cause.Error()
	}
	rsp, err := runtime.metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: runtime.metadataAuth, SpaceId: key.spaceID, ViewId: key.viewID,
		BuildId: runtime.buildID, OwnerId: runtime.ownerID, Error: message,
	})
	if err != nil {
		log.Printf("storage view failed to mark build %s/%s as failed: %v", key.spaceID, key.viewID, err)
		return err
	}
	if err := requireSuccess(rsp.GetRetInfo()); err != nil {
		return err
	}
	return nil
}

func (s *Service) AttachActiveView(view *pb.View) error {
	return s.AttachActiveViewWithGrace(context.Background(), view, 0)
}

func (s *Service) AttachActiveViewWithGrace(ctx context.Context, view *pb.View, grace time.Duration) error {
	_ = grace
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetActiveIndexId() == "" {
		return errors.New("active view metadata is required")
	}
	// A legacy metadata row may describe an in-flight rebuild without the
	// persisted active contract introduced for A/B views. Falling back to the
	// desired DatasetIds/PrimaryDatasetId would silently route markers and
	// queries to the next revision. Refuse that ambiguous state so startup or
	// reconciliation surfaces an actionable migration error instead.
	if view.GetActiveViewRevision() > 0 && view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		if len(persistedActiveDatasetIDs(view)) == 0 || strings.TrimSpace(view.GetAttributes()[activePrimaryDatasetAttr]) == "" {
			return fmt.Errorf("%w: active view %s/%s is rebuilding without persisted active contract", errActiveContractUnavailable, view.GetSpaceId(), view.GetViewId())
		}
	}
	engineName := strings.ToLower(strings.TrimSpace(view.GetEngine()))
	if s.engines[engineName] == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	columns := view.GetActiveColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		columns = view.GetColumns()
	}
	activePrimaryDatasetID := strings.TrimSpace(view.GetAttributes()[activePrimaryDatasetAttr])
	if activePrimaryDatasetID == "" {
		activePrimaryDatasetID = view.GetPrimaryDatasetId()
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetActiveViewRevision(),
		PrimaryDatasetID: activePrimaryDatasetID, Engine: engineName, Columns: columns, SchemaHash: view.GetActiveViewSchemaHash(),
	}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{}
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	previousActive := runtime.active
	activeChanged := previousActive != "" && previousActive != view.GetActiveIndexId()
	previousGeneration := uint64(0)
	if activeChanged {
		// Capture the generation while runtime.mu still excludes PrepareViewIndex
		// for this View. Reading it after unlock could accidentally associate the
		// retired-index candidate with a newly reused slot.
		previousGeneration = s.indexGenerationOf(previousActive)
	}
	if activeChanged {
		if err := s.retireIndex(context.WithoutCancel(ctx), previousActive, previousGeneration); err != nil {
			runtime.mu.Unlock()
			return fmt.Errorf("mark displaced view index retiring: %w", err)
		}
	}
	s.attachActiveViewLocked(view, runtime, schema, columns, activePrimaryDatasetID, engineName)
	runtime.mu.Unlock()
	return nil
}

// attachActiveViewLocked updates the in-memory active contract. The caller
// must hold runtime.mu; it is used by both restart recovery and the short
// activation critical section.
func (s *Service) attachActiveViewLocked(view *pb.View, runtime *viewRuntime, schema viewindex.ViewIndexSchema, columns []*pb.ViewColumn, activePrimaryDatasetID, engineName string) {
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	activeChanged := runtime.active != "" && runtime.active != view.GetActiveIndexId()
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[view.GetActiveIndexId()] = engineName
	s.schemas[view.GetActiveIndexId()] = schema
	if runtime.active != view.GetActiveIndexId() {
		runtime.statsIndexID = ""
		runtime.stats = viewindex.ViewIndexStats{}
	}
	runtime.active = view.GetActiveIndexId()
	// Only initialize the active contract when attaching an index for the first
	// time. During a desired-metadata refresh the same physical active index is
	// re-attached with the new desired DatasetIds; replacing the snapshot here
	// would let period markers observe the next revision before activation.
	if activeChanged || !runtime.activeDatasetSet {
		runtime.activeDatasetIDs = persistedActiveDatasetIDs(view)
		if len(runtime.activeDatasetIDs) == 0 {
			runtime.activeDatasetIDs = append([]string(nil), view.GetDatasetIds()...)
		}
		runtime.activePrimaryDatasetID = activePrimaryDatasetID
		if runtime.activePrimaryDatasetID == "" {
			runtime.activePrimaryDatasetID = view.GetPrimaryDatasetId()
		}
		runtime.activeDatasetSet = true
	}
	if runtime.next == view.GetActiveIndexId() {
		// First activation attaches the index that PrepareViewIndex stored as
		// next. Clear that alias so a live row is not written twice and a
		// transient second write cannot remove the newly active index.
		runtime.next = ""
		runtime.nextDatasetIDs = nil
		runtime.nextPrimaryDatasetID = ""
		runtime.buildID = ""
		runtime.ownerID = ""
		runtime.metadata = nil
		runtime.metadataAuth = nil
		runtime.buildFailed = false
		if runtime.buildCancel != nil {
			runtime.buildCancel()
		}
		runtime.buildCancel = nil
		runtime.buildContext = nil
	}
	runtime.status = "active"
	s.indexView[view.GetActiveIndexId()] = viewKey
	for _, column := range columns {
		if datasetID := viewColumnDataset(column); datasetID != "" {
			ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][view.GetActiveIndexId()] = struct{}{}
		}
	}
	// An explicit zero-column projection still owns its Dataset events. Route
	// those events to the index so rows/markers can be acknowledged without
	// pretending the mapping is missing; the index write is intentionally a
	// key/attribute-only no-op for the empty schema.
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] == "true" {
		s.attachExplicitEmptyDatasetMappingsLocked(view, view.GetActiveIndexId())
	}
	s.mu.Unlock()
}

func (s *Service) attachExplicitEmptyDatasetMappingsLocked(view *pb.View, indexID string) {
	if view == nil || indexID == "" {
		return
	}
	datasetIDs := append([]string(nil), view.GetDatasetIds()...)
	if len(datasetIDs) == 0 && view.GetPrimaryDatasetId() != "" {
		datasetIDs = []string{view.GetPrimaryDatasetId()}
	}
	for _, datasetID := range datasetIDs {
		if datasetID == "" {
			continue
		}
		ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
		if s.byData[ref] == nil {
			s.byData[ref] = make(map[string]struct{})
		}
		s.byData[ref][indexID] = struct{}{}
	}
}

func (s *Service) AttachPendingViewBuild(ctx context.Context, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" || view.GetIndexBuild() == nil || view.GetIndexBuild().GetIndexId() == "" {
		return errors.New("pending view build metadata is required")
	}
	build := view.GetIndexBuild()
	engineName := strings.ToLower(strings.TrimSpace(build.GetEngine()))
	if engineName == "" {
		engineName = strings.ToLower(strings.TrimSpace(view.GetEngine()))
	}
	engine := s.engines[engineName]
	if engine == nil {
		return fmt.Errorf("view engine %q is unavailable", engineName)
	}
	stats, err := engine.Stat(ctx, build.GetIndexId())
	if err != nil {
		return err
	}
	if !stats.Exists {
		return fmt.Errorf("pending view index %q is missing", build.GetIndexId())
	}
	if expected := build.GetTargetViewVersion(); expected > 0 && stats.ViewVersion != expected {
		return fmt.Errorf("pending view index %q revision mismatch: metadata=%d physical=%d", build.GetIndexId(), expected, stats.ViewVersion)
	}
	columns := build.GetColumns()
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] != "true" {
		columns = view.GetColumns()
	}
	version := build.GetTargetViewVersion()
	if version == 0 {
		version = view.GetDesiredViewRevision()
	}
	hash := build.GetSchemaHash()
	if hash == "" {
		hash = viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, PrimaryDatasetID: view.GetPrimaryDatasetId()})
	}
	physicalSchemaHash := viewindex.HashViewIndexSchema(viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, PrimaryDatasetID: view.GetPrimaryDatasetId()})
	if hash != physicalSchemaHash {
		return fmt.Errorf("pending view index %q metadata schema hash is stale: build=%q desired=%q", build.GetIndexId(), hash, physicalSchemaHash)
	}
	if stats.SchemaHash != physicalSchemaHash {
		return fmt.Errorf("pending view index %q schema hash mismatch: expected=%q physical=%q", build.GetIndexId(), physicalSchemaHash, stats.SchemaHash)
	}
	schema := viewindex.ViewIndexSchema{SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: version, Engine: engineName, Columns: columns, SchemaHash: hash, PrimaryDatasetID: view.GetPrimaryDatasetId()}
	viewKey := viewRef{spaceID: view.GetSpaceId(), viewID: view.GetViewId()}
	s.mu.Lock()
	runtime := s.views[viewKey]
	if runtime == nil {
		runtime = &viewRuntime{}
		s.views[viewKey] = runtime
	}
	s.mu.Unlock()
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	release, err := s.indexWriteGate(build.GetIndexId()).lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	// Recovery may attach a prepared slot without calling PrepareViewIndex in
	// this process. Treat it as a fresh generation so a stale cleanup candidate
	// cannot remove the recovered file.
	s.nextIndexGeneration(build.GetIndexId())
	s.mu.Lock()
	s.catalogViews[viewKey] = proto.Clone(view).(*pb.View)
	s.indexEngine[build.GetIndexId()] = engineName
	s.schemas[build.GetIndexId()] = schema
	s.indexView[build.GetIndexId()] = viewKey
	for _, column := range columns {
		if datasetID := viewColumnDataset(column); datasetID != "" {
			ref := datasetRef{spaceID: view.GetSpaceId(), datasetID: datasetID}
			if s.byData[ref] == nil {
				s.byData[ref] = make(map[string]struct{})
			}
			s.byData[ref][build.GetIndexId()] = struct{}{}
		}
	}
	if len(columns) == 0 && view.GetAttributes()[viewColumnsExplicitAttr] == "true" {
		s.attachExplicitEmptyDatasetMappingsLocked(view, build.GetIndexId())
	}
	s.mu.Unlock()
	if runtime.active == "" {
		runtime.next = build.GetIndexId()
		runtime.nextDatasetIDs = append([]string(nil), view.GetDatasetIds()...)
		runtime.nextPrimaryDatasetID = view.GetPrimaryDatasetId()
		runtime.status = "ready"
	} else if runtime.active != build.GetIndexId() {
		runtime.next = build.GetIndexId()
		runtime.nextDatasetIDs = append([]string(nil), view.GetDatasetIds()...)
		runtime.nextPrimaryDatasetID = view.GetPrimaryDatasetId()
		runtime.status = "ready"
	}
	return nil
}

func (s *Service) MarkViewBuildReady(spaceID, viewID string) error {
	s.mu.RLock()
	runtime := s.views[viewRef{spaceID: spaceID, viewID: viewID}]
	s.mu.RUnlock()
	if runtime == nil {
		return errors.New("view runtime is not prepared")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.next == "" {
		runtime.status = "ready"
		return nil
	}
	runtime.status = "ready"
	return nil
}
