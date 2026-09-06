package storageio

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/compiler"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/packages/commonpb"
)

// RPCClient is the narrow Storage Metadata/DataView adapter used by Strategy.
// It keeps generated transport messages outside compiler and evaluator code.
type RPCClient struct {
	SpaceID  string
	Metadata storagepb.MetadataClientProxy
	DataView storagepb.DataViewClientProxy
	// Auth is the Primary/Metadata caller identity. ViewAuth is intentionally
	// separate because Storage DataView validates MOOX_STORAGE_VIEW_AUTH_SECRET.
	Auth     *commonpb.AuthInfo
	ViewAuth *commonpb.AuthInfo
	PageSize uint32
}

// viewSnapshot pins the active index and subject selectors for every View
// used by one strategy evaluation. QueryTimeSeriesRows receives the pinned
// index on every request, so an index rebuild or cutover fails closed instead
// of producing a mixed-generation input frame.
type viewSnapshot struct {
	mu        sync.Mutex
	client    *RPCClient
	spaceID   string
	indexes   map[string]string
	revisions map[string]uint64
	selectors map[string][]*storagepb.TimeSeriesSelector
	subjects  map[string][]input.Subject
	// requireComplete is true for source Views in scheduled snapshots and false
	// for factor result Views or provenance-fenced event snapshots. A
	// factor-ready event is the completeness contract for its current period;
	// the View may still report incomplete historical coverage while the
	// just-published row is already readable.
	requireComplete map[string]bool
}

// ViewSnapshotProvenance exposes the actual active index generations latched
// by a snapshot after its first successful row read.
func (s *viewSnapshot) ViewProvenance(viewID string) (string, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	indexID, ok := s.indexes[viewID]
	if !ok {
		return "", 0, false
	}
	return indexID, s.revisions[viewID], true
}

func (c *RPCClient) BeginViewSnapshot(ctx context.Context, spaceID string, viewIDs []string) (ViewReader, error) {
	return c.beginViewSnapshot(ctx, spaceID, viewIDs, nil)
}

func (c *RPCClient) BeginViewSnapshotAt(ctx context.Context, spaceID string, viewIDs []string, expected map[string]string) (ViewReader, error) {
	return c.beginViewSnapshot(ctx, spaceID, viewIDs, expected)
}

func (c *RPCClient) beginViewSnapshot(ctx context.Context, spaceID string, viewIDs []string, expected map[string]string) (ViewReader, error) {
	if c == nil || c.Metadata == nil || c.DataView == nil {
		return nil, fmt.Errorf("storage metadata/data view clients are not configured")
	}
	uniqueViewIDs := uniqueStrings(viewIDs)
	snapshot := &viewSnapshot{client: c, spaceID: spaceID, indexes: make(map[string]string), revisions: make(map[string]uint64), selectors: make(map[string][]*storagepb.TimeSeriesSelector), subjects: make(map[string][]input.Subject), requireComplete: make(map[string]bool, len(uniqueViewIDs))}
	for index, viewID := range uniqueViewIDs {
		viewRsp, err := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, ViewId: viewID})
		if err != nil {
			return nil, err
		}
		if err := retError(viewRsp.GetRetInfo()); err != nil {
			return nil, err
		}
		view := viewRsp.GetView()
		if view == nil || view.GetPrimaryDatasetId() == "" {
			return nil, fmt.Errorf("storage view %s has no primary dataset", viewID)
		}
		indexID := strings.TrimSpace(view.GetActiveIndexId())
		if indexID == "" {
			return nil, fmt.Errorf("%w: storage view %s has no active index", input.ErrNotReady, viewID)
		}
		expectedID, expectedRevision := decodeViewExpectation(expected[viewID])
		if expectedID != "" && expectedID != indexID {
			return nil, fmt.Errorf("%w: storage view %s active index changed: expected=%s actual=%s", input.ErrStaleViewSnapshot, viewID, expectedID, indexID)
		}
		frequency := viewFrequency(view)
		if frequency == "" {
			return nil, fmt.Errorf("storage view %s has no frequency", viewID)
		}
		selectors, err := c.selectorsForDataset(ctx, spaceID, view.GetPrimaryDatasetId(), frequency)
		if err != nil {
			return nil, err
		}
		subjects, err := c.ListSubjects(ctx, spaceID, viewID)
		if err != nil {
			return nil, err
		}
		snapshot.indexes[viewID] = indexID
		snapshot.revisions[viewID] = expectedRevision
		snapshot.selectors[viewID] = selectors
		snapshot.subjects[viewID] = subjects
		// Event-driven Factor-ready evaluations carry the source/result index
		// provenance that proves the current period. Their source View may still
		// report partial historical coverage, so row presence/readiness checks
		// remain authoritative. Scheduled snapshots have no such event fence and
		// keep the strict source coverage requirement.
		snapshot.requireComplete[viewID] = index == 0 && expected == nil
	}
	return snapshot, nil
}

func (s *viewSnapshot) ReadPeriod(ctx context.Context, spaceID, viewID string, period time.Time) ([]ViewRow, error) {
	return s.readPinnedRows(ctx, spaceID, viewID, period, period.Add(time.Nanosecond))
}

func (s *viewSnapshot) ListSubjects(_ context.Context, _, viewID string) ([]input.Subject, error) {
	subjects, ok := s.subjects[viewID]
	if !ok {
		return nil, fmt.Errorf("storage view %s is not pinned", viewID)
	}
	return append([]input.Subject(nil), subjects...), nil
}

func (s *viewSnapshot) HistoryPeriods(ctx context.Context, spaceID, viewID, _ string, start, end time.Time) (map[string]int, error) {
	// History is used only to count available bars for pool eligibility. The
	// current period read below remains strict, while an older partial index
	// coverage must not block a factor-ready snapshot that already proves the
	// requested current bar.
	rows, err := s.readPinnedRowsWithRequirement(ctx, spaceID, viewID, start, end, false)
	if err != nil {
		return nil, err
	}
	subjects := s.subjects[viewID]
	instrumentBySubject := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		instrumentBySubject[subject.SubjectID] = subject.InstrumentID
	}
	periods := make(map[string]map[int64]struct{})
	for _, row := range rows {
		instrumentID := row.InstrumentID
		if mapped := instrumentBySubject[row.SubjectID]; mapped != "" {
			instrumentID = mapped
		}
		if instrumentID == "" || row.DataTime.IsZero() {
			continue
		}
		at := row.DataTime.UnixNano()
		if periods[instrumentID] == nil {
			periods[instrumentID] = make(map[int64]struct{})
		}
		periods[instrumentID][at] = struct{}{}
		if row.SubjectID != "" {
			if periods[row.SubjectID] == nil {
				periods[row.SubjectID] = make(map[int64]struct{})
			}
			periods[row.SubjectID][at] = struct{}{}
		}
	}
	result := make(map[string]int, len(periods))
	for instrumentID, values := range periods {
		result[instrumentID] = len(values)
	}
	return result, nil
}

func (s *viewSnapshot) readPinnedRows(ctx context.Context, spaceID, viewID string, start, end time.Time) ([]ViewRow, error) {
	s.mu.Lock()
	requireComplete := s.requireComplete[viewID]
	s.mu.Unlock()
	return s.readPinnedRowsWithRequirement(ctx, spaceID, viewID, start, end, requireComplete)
}

func (s *viewSnapshot) readPinnedRowsWithRequirement(ctx context.Context, spaceID, viewID string, start, end time.Time, requireComplete bool) ([]ViewRow, error) {
	s.mu.Lock()
	indexID := s.indexes[viewID]
	revision := s.revisions[viewID]
	selectors := s.selectors[viewID]
	s.mu.Unlock()
	rows, servedRevision, err := s.client.readRowsAtWithRevision(ctx, spaceID, viewID, start, end, indexID, revision, selectors, requireComplete)
	if err != nil {
		return nil, err
	}
	if servedRevision != 0 {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.revisions[viewID] != 0 && s.revisions[viewID] != servedRevision {
			return nil, fmt.Errorf("%w: view %s active index revision changed: expected=%d actual=%d", input.ErrStaleViewSnapshot, viewID, s.revisions[viewID], servedRevision)
		}
		s.revisions[viewID] = servedRevision
	}
	return rows, nil
}

func (c *RPCClient) metadataAuth() *commonpb.AuthInfo {
	if c == nil || c.Auth == nil {
		return nil
	}
	return c.Auth
}

func (c *RPCClient) viewAuth() *commonpb.AuthInfo {
	if c == nil {
		return nil
	}
	if c.ViewAuth != nil {
		return c.ViewAuth
	}
	// Keep test and embedding compatibility for callers that predate the
	// split identity. Production bootstrap always supplies ViewAuth.
	return c.Auth
}

func (c *RPCClient) pageSize() uint32 {
	if c != nil && c.PageSize > 0 {
		return c.PageSize
	}
	return 500
}

func (c *RPCClient) GetView(ctx context.Context, id string) (compiler.ViewDescriptor, error) {
	if c == nil || c.Metadata == nil {
		return compiler.ViewDescriptor{}, fmt.Errorf("storage metadata client is not configured")
	}
	rsp, err := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.metadataAuth(), SpaceId: c.SpaceID, ViewId: id})
	if err != nil {
		return compiler.ViewDescriptor{}, err
	}
	if err := retError(rsp.GetRetInfo()); err != nil {
		return compiler.ViewDescriptor{}, err
	}
	view := rsp.GetView()
	if view == nil {
		return compiler.ViewDescriptor{}, fmt.Errorf("storage view %s is empty", id)
	}
	return compiler.ViewDescriptor{ID: view.GetViewId(), Status: view.GetStatus(), Frequency: viewFrequency(view)}, nil
}

func (c *RPCClient) ListViewColumns(ctx context.Context, id string) ([]compiler.ViewColumn, error) {
	if c == nil || c.Metadata == nil {
		return nil, fmt.Errorf("storage metadata client is not configured")
	}
	var result []compiler.ViewColumn
	for page := uint32(1); ; page++ {
		rsp, err := c.Metadata.ListViewColumns(ctx, &storagepb.ListViewColumnsReq{AuthInfo: c.metadataAuth(), SpaceId: c.SpaceID, ViewId: id, Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if err != nil {
			return nil, err
		}
		if err := retError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, column := range rsp.GetColumns() {
			if column == nil {
				continue
			}
			attrs := make(map[string]string, len(column.GetAttributes()))
			for key, value := range column.GetAttributes() {
				attrs[key] = value
			}
			result = append(result, compiler.ViewColumn{Name: column.GetColumnName(), Attributes: attrs})
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return result, nil
}

func (c *RPCClient) ListSubjects(ctx context.Context, spaceID, viewID string) ([]input.Subject, error) {
	if c == nil || c.Metadata == nil {
		return nil, fmt.Errorf("storage metadata client is not configured")
	}
	view, err := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return nil, err
	}
	if err := retError(view.GetRetInfo()); err != nil {
		return nil, err
	}
	if view.GetView() == nil || view.GetView().GetPrimaryDatasetId() == "" {
		return nil, fmt.Errorf("storage view %s has no primary dataset", viewID)
	}
	datasetID := view.GetView().GetPrimaryDatasetId()
	var result []input.Subject
	for page := uint32(1); ; page++ {
		rsp, listErr := c.Metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, DatasetId: datasetID, Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if listErr != nil {
			return nil, listErr
		}
		if err := retError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, binding := range rsp.GetDatasetSubjects() {
			if binding == nil || binding.GetStatus() != "active" || binding.GetSubjectId() == "" {
				continue
			}
			subjectRsp, getErr := c.Metadata.GetSubject(ctx, &storagepb.GetSubjectReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, SubjectId: binding.GetSubjectId()})
			if getErr != nil {
				return nil, getErr
			}
			if err := retError(subjectRsp.GetRetInfo()); err != nil {
				return nil, err
			}
			subject := subjectRsp.GetSubject()
			if subject == nil {
				continue
			}
			attrs := subject.GetAttributes()
			instrumentID := firstNonEmpty(attrs["instrument_id"], subject.GetSubjectId())
			result = append(result, input.Subject{SubjectID: subject.GetSubjectId(), InstrumentID: instrumentID, Exchange: attrs["exchange"], Market: firstNonEmpty(attrs["market_type"], subject.GetMarket()), QuoteAsset: firstNonEmpty(attrs["quote_asset"], subject.GetCurrency()), SeriesTag: subjectSeriesTag(attrs), Active: subject.GetStatus() == "active"})
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	if len(result) == 0 {
		// Collector may intentionally omit one DatasetSubject row per K-line
		// symbol. In that mode the authoritative Subject catalog is used as the
		// immutable universe instead of treating an empty binding list as a
		// permanently unavailable market.
		return c.listCatalogSubjects(ctx, spaceID, datasetID)
	}
	return result, nil
}

func (c *RPCClient) ReadPeriod(ctx context.Context, spaceID, viewID string, period time.Time) ([]ViewRow, error) {
	return c.readRows(ctx, spaceID, viewID, period, period.Add(time.Nanosecond))
}

func (c *RPCClient) HistoryPeriods(ctx context.Context, spaceID, viewID, frequency string, start, end time.Time) (map[string]int, error) {
	rows, err := c.readRows(ctx, spaceID, viewID, start, end)
	if err != nil {
		return nil, err
	}
	subjects, err := c.ListSubjects(ctx, spaceID, viewID)
	if err != nil {
		return nil, err
	}
	instrumentBySubject := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		instrumentBySubject[subject.SubjectID] = subject.InstrumentID
	}
	periods := make(map[string]map[int64]struct{})
	for _, row := range rows {
		instrumentID := row.InstrumentID
		if mapped := instrumentBySubject[row.SubjectID]; mapped != "" {
			instrumentID = mapped
		}
		if instrumentID == "" || row.DataTime.IsZero() {
			continue
		}
		at := row.DataTime.UnixNano()
		if periods[instrumentID] == nil {
			periods[instrumentID] = make(map[int64]struct{})
		}
		periods[instrumentID][at] = struct{}{}
		if row.SubjectID != "" {
			if periods[row.SubjectID] == nil {
				periods[row.SubjectID] = make(map[int64]struct{})
			}
			periods[row.SubjectID][at] = struct{}{}
		}
	}
	result := make(map[string]int, len(periods))
	for instrumentID, values := range periods {
		result[instrumentID] = len(values)
	}
	return result, nil
}

func (c *RPCClient) readRows(ctx context.Context, spaceID, viewID string, start, end time.Time) ([]ViewRow, error) {
	if c == nil || c.DataView == nil || c.Metadata == nil {
		return nil, fmt.Errorf("storage metadata/data view clients are not configured")
	}
	viewRsp, err := c.Metadata.GetView(ctx, &storagepb.GetViewReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, ViewId: viewID})
	if err != nil {
		return nil, err
	}
	if err := retError(viewRsp.GetRetInfo()); err != nil {
		return nil, err
	}
	view := viewRsp.GetView()
	if view == nil || view.GetPrimaryDatasetId() == "" {
		return nil, fmt.Errorf("storage view %s has no primary dataset", viewID)
	}
	expectedIndexID := strings.TrimSpace(view.GetActiveIndexId())
	if expectedIndexID == "" {
		return nil, fmt.Errorf("%w: storage view %s has no active index", input.ErrNotReady, viewID)
	}
	frequency := viewFrequency(view)
	if frequency == "" {
		return nil, fmt.Errorf("storage view %s has no frequency", viewID)
	}
	selectors, selectorErr := c.selectorsForDataset(ctx, spaceID, view.GetPrimaryDatasetId(), frequency)
	if selectorErr != nil {
		return nil, selectorErr
	}
	return c.readRowsAt(ctx, spaceID, viewID, start, end, expectedIndexID, 0, selectors)
}

func (c *RPCClient) readRowsAt(ctx context.Context, spaceID, viewID string, start, end time.Time, expectedIndexID string, expectedRevision uint64, selectors []*storagepb.TimeSeriesSelector) ([]ViewRow, error) {
	rows, _, err := c.readRowsAtWithRevision(ctx, spaceID, viewID, start, end, expectedIndexID, expectedRevision, selectors, true)
	return rows, err
}

func (c *RPCClient) readRowsAtWithRevision(ctx context.Context, spaceID, viewID string, start, end time.Time, expectedIndexID string, expectedRevision uint64, selectors []*storagepb.TimeSeriesSelector, requireComplete bool) ([]ViewRow, uint64, error) {
	if c == nil || c.DataView == nil {
		return nil, 0, fmt.Errorf("storage data view client is not configured")
	}
	if strings.TrimSpace(expectedIndexID) == "" {
		return nil, 0, fmt.Errorf("%w: storage view %s has no active index", input.ErrNotReady, viewID)
	}
	if len(selectors) == 0 {
		return nil, 0, fmt.Errorf("%w: storage view %s has no active selectors", input.ErrNotReady, viewID)
	}
	// A strategy period is a cross-sectional point-in-time read. Keep the active
	// index and served revision pinned while walking all pages so a large
	// wildcard subject set cannot be silently truncated. The extra slot lets us
	// detect an exact selector that unexpectedly has multiple series rows.
	singlePeriod := end.Sub(start) == time.Nanosecond
	pageSize := c.pageSize()
	if singlePeriod && uint32(len(selectors)+1) > pageSize {
		pageSize = uint32(len(selectors) + 1)
	}
	var result []ViewRow
	currentExpectedRevision := expectedRevision
	for page := uint32(1); ; page++ {
		rsp, queryErr := c.DataView.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{AuthInfo: c.viewAuth(), SpaceId: spaceID, ViewId: viewID, Selectors: selectors, TimeRange: &storagepb.TimeRange{StartTime: start.UTC().Format(time.RFC3339Nano), EndTime: end.UTC().Format(time.RFC3339Nano)}, Page: &commonpb.Page{Page: page, Size: pageSize}, TotalMode: commonpb.TotalMode_NONE, ExpectedActiveIndexId: expectedIndexID, ExpectedActiveIndexRevision: currentExpectedRevision})
		if queryErr != nil {
			return nil, 0, queryErr
		}
		if err := retErrorForView(rsp.GetRetInfo(), viewID); err != nil {
			return nil, 0, err
		}
		if servedRevision := rsp.GetServedActiveIndexRevision(); servedRevision != 0 {
			if currentExpectedRevision != 0 && currentExpectedRevision != servedRevision {
				return nil, 0, fmt.Errorf("%w: view %s active index revision changed: expected=%d actual=%d", input.ErrStaleViewSnapshot, viewID, currentExpectedRevision, servedRevision)
			}
			currentExpectedRevision = servedRevision
		}
		if requireComplete && !rsp.GetComplete() {
			return nil, 0, fmt.Errorf("%w: view %s is not complete", input.ErrNotReady, viewID)
		}
		// A selector without series_tag intentionally matches every series for
		// that subject.  Such a wildcard is needed by factor-backed strategies
		// whose source factor combines several venue rows.  Exact selectors are
		// one-row contracts, so a second page or an extra row is incomplete.
		// Wildcard pages are validated by Loader when the view contributes values
		// to the strategy; source-only presence checks may safely ignore extras.
		if singlePeriod && selectorsHaveExactSeriesTags(selectors) && (rsp.GetPageResult().GetHasMore() || len(result)+len(rsp.GetRows()) > len(selectors)) {
			return nil, 0, fmt.Errorf("%w: view %s period has more rows than active selectors", input.ErrStrictIncomplete, viewID)
		}
		for _, row := range rsp.GetRows() {
			converted, convertErr := convertRow(row)
			if convertErr != nil {
				return nil, 0, convertErr
			}
			result = append(result, converted)
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return result, currentExpectedRevision, nil
}

func selectorsHaveExactSeriesTags(selectors []*storagepb.TimeSeriesSelector) bool {
	if len(selectors) == 0 {
		return false
	}
	for _, selector := range selectors {
		if selector == nil || selector.SeriesTag == nil {
			return false
		}
	}
	return true
}

func (c *RPCClient) selectorsForDataset(ctx context.Context, spaceID, datasetID, frequency string) ([]*storagepb.TimeSeriesSelector, error) {
	if c == nil || c.Metadata == nil {
		return nil, fmt.Errorf("storage metadata client is not configured")
	}
	var selectors []*storagepb.TimeSeriesSelector
	for page := uint32(1); ; page++ {
		rsp, err := c.Metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, DatasetId: datasetID, Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if err != nil {
			return nil, err
		}
		if err := retError(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, binding := range rsp.GetDatasetSubjects() {
			if binding == nil || binding.GetStatus() != "active" || binding.GetSubjectId() == "" {
				continue
			}
			seriesTag, subjectErr := c.subjectSeriesTag(ctx, spaceID, binding.GetSubjectId())
			if subjectErr != nil {
				return nil, subjectErr
			}
			selector := &storagepb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: binding.GetSubjectId(), Freq: frequency}
			if seriesTag != "" {
				selector.SeriesTag = &seriesTag
			}
			selectors = append(selectors, selector)
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	if len(selectors) == 0 {
		subjects, fallbackErr := c.listCatalogSubjects(ctx, spaceID, datasetID)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		for _, subject := range subjects {
			if !subject.Active || subject.SubjectID == "" {
				continue
			}
			selector := &storagepb.TimeSeriesSelector{SpaceId: spaceID, DatasetId: datasetID, SubjectId: subject.SubjectID, Freq: frequency}
			if subject.SeriesTag != "" {
				seriesTag := subject.SeriesTag
				selector.SeriesTag = &seriesTag
			}
			selectors = append(selectors, selector)
		}
	}
	if len(selectors) == 0 {
		return nil, fmt.Errorf("%w: view dataset %s has no active subjects or catalog entries", input.ErrNotReady, datasetID)
	}
	return selectors, nil
}

func (c *RPCClient) subjectSeriesTag(ctx context.Context, spaceID, subjectID string) (string, error) {
	rsp, err := c.Metadata.GetSubject(ctx, &storagepb.GetSubjectReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, SubjectId: subjectID})
	if err != nil {
		return "", err
	}
	if err := retError(rsp.GetRetInfo()); err != nil {
		return "", err
	}
	if subject := rsp.GetSubject(); subject != nil {
		return subjectSeriesTag(subject.GetAttributes()), nil
	}
	return "", nil
}

func (c *RPCClient) listCatalogSubjects(ctx context.Context, spaceID, datasetID string) ([]input.Subject, error) {
	marketType := ""
	if rsp, err := c.Metadata.GetDataset(ctx, &storagepb.GetDatasetReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, DatasetId: datasetID}); err == nil && rsp != nil {
		if retErr := retError(rsp.GetRetInfo()); retErr == nil && rsp.GetDataset() != nil {
			marketType = strings.ToLower(strings.TrimSpace(rsp.GetDataset().GetAttributes()["market_type"]))
		}
	}
	var result []input.Subject
	for page := uint32(1); ; page++ {
		rsp, err := c.Metadata.ListSubjects(ctx, &storagepb.ListSubjectsReq{AuthInfo: c.metadataAuth(), SpaceId: spaceID, SubjectType: "crypto_pair", Market: marketType, Page: &commonpb.Page{Page: page, Size: c.pageSize()}})
		if err != nil {
			return nil, err
		}
		if retErr := retError(rsp.GetRetInfo()); retErr != nil {
			return nil, retErr
		}
		for _, subject := range rsp.GetSubjects() {
			if subject == nil || subject.GetStatus() != "active" || subject.GetSubjectId() == "" {
				continue
			}
			attrs := subject.GetAttributes()
			subjectMarketType := strings.ToLower(strings.TrimSpace(firstNonEmpty(attrs["market_type"], subject.GetMarket())))
			if marketType != "" && subjectMarketType != "" && subjectMarketType != marketType {
				continue
			}
			result = append(result, input.Subject{SubjectID: subject.GetSubjectId(), InstrumentID: firstNonEmpty(attrs["instrument_id"], subject.GetSubjectId()), Exchange: attrs["exchange"], Market: firstNonEmpty(attrs["market_type"], subject.GetMarket()), QuoteAsset: firstNonEmpty(attrs["quote_asset"], subject.GetCurrency()), SeriesTag: subjectSeriesTag(attrs), Active: true})
		}
		if !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("%w: view dataset %s has no active subjects or catalog entries", input.ErrNotReady, datasetID)
	}
	return result, nil
}

func subjectSeriesTag(attrs map[string]string) string {
	if tag := strings.TrimSpace(attrs["series_tag"]); tag != "" {
		return tag
	}
	// Collector symbol metadata predates the explicit series_tag attribute.
	// Derive the canonical venue tag from its exchange so a multi-provider
	// View cannot silently mix rows when reading the fallback catalog.
	if exchange := strings.TrimSpace(attrs["exchange"]); exchange != "" {
		return "venue:" + strings.ToLower(exchange)
	}
	return ""
}

func convertRow(row *storagepb.TimeSeriesRow) (ViewRow, error) {
	if row == nil || row.GetKey() == nil {
		return ViewRow{}, fmt.Errorf("storage returned an invalid time-series row")
	}
	at, err := time.Parse(time.RFC3339Nano, row.GetKey().GetDataTime())
	if err != nil {
		return ViewRow{}, fmt.Errorf("parse storage data_time: %w", err)
	}
	values := make(map[string]string, len(row.GetFields()))
	for _, field := range row.GetFields() {
		if field == nil || field.GetValue() == nil {
			continue
		}
		value, valueErr := typedValueString(field.GetValue())
		if valueErr != nil {
			return ViewRow{}, fmt.Errorf("field %s: %w", field.GetFieldId(), valueErr)
		}
		values[field.GetFieldId()] = value
	}
	return ViewRow{InstrumentID: row.GetKey().GetSubjectId(), SubjectID: row.GetKey().GetSubjectId(), SeriesTag: row.GetKey().GetSeriesTag(), DataTime: at.UTC(), Values: values, Attributes: row.GetAttributes()}, nil
}

func typedValueString(value *storagepb.TypedValue) (string, error) {
	switch typed := value.GetValue().(type) {
	case *storagepb.TypedValue_StringValue:
		return typed.StringValue, nil
	case *storagepb.TypedValue_IntValue:
		return strconv.FormatInt(typed.IntValue, 10), nil
	case *storagepb.TypedValue_DoubleValue:
		if math.IsNaN(typed.DoubleValue) || math.IsInf(typed.DoubleValue, 0) {
			return "", fmt.Errorf("non-finite double")
		}
		return strconv.FormatFloat(typed.DoubleValue, 'f', -1, 64), nil
	case *storagepb.TypedValue_TimeValue:
		return typed.TimeValue, nil
	case *storagepb.TypedValue_JsonValue:
		return typed.JsonValue, nil
	default:
		return "", fmt.Errorf("unsupported typed value %T", value.GetValue())
	}
}

func viewFrequency(view *storagepb.View) string {
	if view == nil {
		return ""
	}
	var filter struct {
		Freq string `json:"freq"`
	}
	if json.Unmarshal([]byte(view.GetFilterJson()), &filter) == nil {
		return filter.Freq
	}
	return view.GetAttributes()["frequency"]
}

func retError(info *commonpb.RetInfo) error {
	if info == nil || info.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	msg := info.GetMsg()
	// Storage reports both a genuinely unavailable View and a pinned-generation
	// mismatch as VIEW_NOT_READY. Only the latter is terminal for this delivery:
	// preserve the stale-generation sentinel so the processor can ACK it instead
	// of retrying forever, while ordinary "no active index" remains retryable.
	if info.GetCode() == commonpb.ErrorCode_VIEW_NOT_READY {
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "active view index revision changed") ||
			strings.Contains(lower, "active view index was updated during query") {
			return fmt.Errorf("%w: storage rpc %s: %s", input.ErrStaleViewSnapshot, info.GetCode().String(), msg)
		}
		// A View that is still being built or has no active index is a
		// transient readiness condition, not a permanent dependency mismatch.
		return fmt.Errorf("storage rpc %s: %s", info.GetCode().String(), msg)
	}
	err := fmt.Errorf("storage rpc %s: %s", info.GetCode().String(), msg)
	if info.GetCode() == commonpb.ErrorCode_INNER_ERR {
		return err
	}
	return compiler.DependencyMismatchError(err)
}

func retErrorForView(info *commonpb.RetInfo, viewID string) error {
	err := retError(info)
	if err == nil || info.GetCode() != commonpb.ErrorCode_VIEW_NOT_READY {
		return err
	}
	if !strings.Contains(strings.ToLower(info.GetMsg()), "active view index changed") {
		return err
	}
	// During Storage startup the in-memory runtime temporarily reports the
	// logical View ID as its active index. That is a transient not-ready state,
	// not a superseded generation. A physical A/B mismatch remains stale.
	parts := strings.SplitN(info.GetMsg(), "actual=", 2)
	if len(parts) == 2 {
		actual := strings.TrimSpace(parts[1])
		if actual == "" || actual == viewID {
			return err
		}
	}
	return fmt.Errorf("%w: storage rpc %s: %s", input.ErrStaleViewSnapshot, info.GetCode().String(), info.GetMsg())
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func decodeViewExpectation(raw string) (string, uint64) {
	parts := strings.SplitN(raw, "\x00", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(raw), 0
	}
	revision, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil {
		return strings.TrimSpace(parts[0]), 0
	}
	return strings.TrimSpace(parts[0]), revision
}
