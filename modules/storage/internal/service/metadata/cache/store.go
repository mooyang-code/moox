package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/snapshotcache"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

const (
	RefreshDisabled time.Duration = -1

	indexKind               = "kind"
	indexPrimary            = "primary"
	indexDataset            = "dataset"
	indexViewPrimaryDataset = "view_primary_dataset"
)

const (
	kindSpace          = "space"
	kindView           = "view"
	kindViewColumn     = "view_column"
	kindDataSource     = "data_source"
	kindSubject        = "subject"
	kindSubjectSymbol  = "subject_symbol"
	kindDataset        = "dataset"
	kindDatasetSubject = "dataset_subject"
	kindFieldGroup     = "field_group"
	kindField          = "field"
	kindFactor         = "factor"
	kindDatasetColumn  = "dataset_column"
	kindDataNode       = "data_node"
	kindDevice         = "device"
	kindArchiveFile    = "archive_file"
)

// Options 保存元数据缓存包装器配置。
type Options struct {
	RefreshInterval    time.Duration
	RefreshTimeout     time.Duration
	InitialLoadTimeout time.Duration
	RandomStartDelay   time.Duration
}

// Store 封装基于 snapshotcache 的元数据缓存读能力。
type Store struct {
	base  metadata.Reader
	cache *snapshotcache.Cache[entry]
}

// entry 保存一个缓存条目的值与错误信息。
type entry struct {
	Kind           string   `json:"kind"`
	SpaceID        string   `json:"space_id,omitempty"`
	ID             string   `json:"id,omitempty"`
	Owner          string   `json:"owner,omitempty"`
	Status         string   `json:"status,omitempty"`
	Name           string   `json:"name,omitempty"`
	Currency       string   `json:"currency,omitempty"`
	DataSourceID   string   `json:"data_source_id,omitempty"`
	DataSourceKind string   `json:"data_source_kind,omitempty"`
	Market         string   `json:"market,omitempty"`
	SubjectType    string   `json:"subject_type,omitempty"`
	SubjectID      string   `json:"subject_id,omitempty"`
	ExternalSymbol string   `json:"external_symbol,omitempty"`
	DatasetID      string   `json:"dataset_id,omitempty"`
	GroupID        string   `json:"group_id,omitempty"`
	ParentGroupID  string   `json:"parent_group_id,omitempty"`
	DatasetIDs     []string `json:"dataset_ids,omitempty"`
	DataKind       int32    `json:"data_kind,omitempty"`
	Freqs          []string `json:"freqs,omitempty"`
	KeepDuration   string   `json:"keep_duration,omitempty"`
	DataNodeID     string   `json:"data_node_id,omitempty"`
	BindingLocked  bool     `json:"binding_locked,omitempty"`
	Revision       uint64   `json:"revision,omitempty"`
	ViewID         string   `json:"view_id,omitempty"`
	ValueType      int32    `json:"value_type,omitempty"`
	Algorithm      string   `json:"algorithm,omitempty"`
	Engine         string   `json:"engine,omitempty"`
	Payload        []byte   `json:"payload"`
}

// MetadataSnapshot is an immutable read view captured from one cache
// publication. It is used by request-level validators to avoid mixing two
// metadata generations during one write.
type MetadataSnapshot struct {
	items []entry
}

func (s *Store) Snapshot() *MetadataSnapshot {
	if s == nil || s.cache == nil {
		return nil
	}
	raw := s.cache.Snapshot()
	if raw == nil {
		return nil
	}
	items := make([]entry, len(raw.Items))
	for i, item := range raw.Items {
		items[i] = item
		items[i].Payload = append([]byte(nil), item.Payload...)
		items[i].DatasetIDs = append([]string(nil), item.DatasetIDs...)
		items[i].Freqs = append([]string(nil), item.Freqs...)
	}
	return &MetadataSnapshot{items: items}
}

func (s *Store) SnapshotReader() metadata.SnapshotReader { return s.Snapshot() }

func (s *Store) RequestSnapshot() metadata.RequestSnapshot {
	return &requestSnapshot{snapshot: s.Snapshot()}
}

var _ metadata.Reader = (*Store)(nil)

func New(ctx context.Context, base metadata.Reader, opts Options) (*Store, error) {
	if base == nil {
		return nil, errors.New("metadata base store is required")
	}
	store := &Store{base: base}
	cache, err := snapshotcache.New(snapshotcache.Options[entry]{
		Name: "storage-metadata",
		Source: snapshotcache.SourceFunc[entry](func(ctx context.Context) ([]entry, error) {
			return store.fetchEntries(ctx)
		}),
		Indexes: []snapshotcache.Index[entry]{
			{Name: indexKind, Key: func(item entry) []string {
				return []string{item.Kind}
			}},
			{Name: indexPrimary, Unique: true, Key: func(item entry) []string {
				return []string{item.Kind, item.SpaceID, item.ID}
			}},
			{Name: indexDataset, Key: func(item entry) []string {
				if item.DatasetID == "" {
					return nil
				}
				return []string{item.Kind, item.SpaceID, item.DatasetID}
			}},
			{Name: indexViewPrimaryDataset, Key: func(item entry) []string {
				if item.Kind != kindView || item.DatasetID == "" {
					return nil
				}
				return []string{item.Kind, item.SpaceID, item.DatasetID}
			}},
		},
		Startup: snapshotcache.StartupOptions{
			FailIfNoSnapshot: true,
		},
		RefreshInterval:    opts.RefreshInterval,
		RefreshTimeout:     opts.RefreshTimeout,
		InitialLoadTimeout: opts.InitialLoadTimeout,
		RandomStartDelay:   opts.RandomStartDelay,
	})
	if err != nil {
		return nil, err
	}
	store.cache = cache
	if err := cache.Start(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(trpc.BackgroundContext(), 5*time.Second)
	defer cancel()
	var stopErr error
	if s.cache != nil {
		stopErr = s.cache.Stop(ctx)
	}
	return stopErr
}

func (s *Store) Refresh(ctx context.Context) error {
	if s == nil || s.cache == nil {
		return errors.New("metadata cache is not open")
	}
	return s.cache.Refresh(ctx)
}

func (s *MetadataSnapshot) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	return snapshotGetProto(s, ctx, kindDataset, spaceID, datasetID, func() *pb.Dataset { return &pb.Dataset{} })
}

func (s *MetadataSnapshot) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	items, err := snapshotDecodeEntries(s.snapshotList(kindDatasetColumn, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) && (datasetID == "" || item.DatasetID == datasetID)
	}), func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

type requestSnapshot struct{ snapshot *MetadataSnapshot }

func (s *requestSnapshot) GetDataset(spaceID string, datasetID string) (*pb.Dataset, bool) {
	if s == nil || s.snapshot == nil {
		return nil, false
	}
	item, err := s.snapshot.GetDataset(context.Background(), spaceID, datasetID)
	return item, err == nil
}

func (s *requestSnapshot) GetDataNode(nodeID string) (*pb.DataNode, bool) {
	if s == nil || s.snapshot == nil {
		return nil, false
	}
	item, err := s.snapshot.getDataNode(context.Background(), nodeID)
	return item, err == nil
}

func (s *MetadataSnapshot) getDataNode(ctx context.Context, nodeID string) (*pb.DataNode, error) {
	return snapshotGetProto(s, ctx, kindDataNode, "", nodeID, func() *pb.DataNode { return &pb.DataNode{} })
}

func (s *MetadataSnapshot) GetDataNode(nodeID string) (*pb.DataNode, bool) {
	item, err := s.getDataNode(context.Background(), nodeID)
	return item, err == nil
}

func (s *Store) GetSpace(ctx context.Context, spaceID string) (*pb.Space, error) {
	return getProto(s, ctx, kindSpace, "", spaceID, func() *pb.Space { return &pb.Space{} })
}

func (s *Store) ListSpaces(ctx context.Context, owner string, page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindSpace, func(item entry) bool {
		return owner == "" || item.Owner == owner
	}), func() *pb.Space { return &pb.Space{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetView(ctx context.Context, spaceID string, viewID string) (*pb.View, error) {
	view, err := getProto(s, ctx, kindView, spaceID, viewID, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, err
	}
	columns, _, err := s.ListViewColumns(ctx, spaceID, viewID, nil)
	if err != nil {
		return nil, err
	}
	view.Columns = columns
	return view, nil
}

func (s *Store) ListViews(ctx context.Context, spaceID string, datasetID string, status string, page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	var raw []entry
	if spaceID != "" && datasetID != "" {
		// Primary dataset hits use the dedicated index; secondary DatasetIDs still need a scan.
		raw = append(raw, s.listByIndex(indexViewPrimaryDataset, kindView, spaceID, datasetID)...)
		raw = append(raw, s.list(kindView, func(item entry) bool {
			return item.SpaceID == spaceID && item.DatasetID != datasetID && containsString(item.DatasetIDs, datasetID)
		})...)
		if status != "" {
			filtered := make([]entry, 0, len(raw))
			for _, item := range raw {
				if item.Status == status {
					filtered = append(filtered, item)
				}
			}
			raw = filtered
		}
	} else {
		raw = s.list(kindView, func(item entry) bool {
			return (spaceID == "" || item.SpaceID == spaceID) &&
				(status == "" || item.Status == status) &&
				(datasetID == "" || item.DatasetID == datasetID || containsString(item.DatasetIDs, datasetID))
		})
	}
	items, err := decodeEntries(raw, func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListViewsByDataset(ctx context.Context, spaceID string, datasetID string) ([]*pb.View, error) {
	const pageSize = uint32(1000)
	var out []*pb.View
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := s.ListViews(ctx, spaceID, datasetID, "active", &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			return out, nil
		}
	}
}

func (s *Store) ListViewColumns(ctx context.Context, spaceID string, viewID string, page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindViewColumn, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) && (viewID == "" || item.ViewID == viewID)
	}), func() *pb.ViewColumn { return &pb.ViewColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetDataSource(ctx context.Context, spaceID string, dataSourceID string) (*pb.DataSource, error) {
	return getProto(s, ctx, kindDataSource, spaceID, dataSourceID, func() *pb.DataSource { return &pb.DataSource{} })
}

func (s *Store) ListDataSources(ctx context.Context, spaceID string, kind string, market string, keyword string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	items, err := decodeEntries(s.list(kindDataSource, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(kind == "" || item.DataSourceKind == kind) &&
			(market == "" || item.Market == market) &&
			(keyword == "" || strings.Contains(strings.ToLower(item.ID), keyword) || strings.Contains(strings.ToLower(item.Name), keyword) || strings.Contains(strings.ToLower(item.DataSourceKind), keyword) || strings.Contains(strings.ToLower(item.Market), keyword))
	}), func() *pb.DataSource { return &pb.DataSource{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error) {
	return getProto(s, ctx, kindSubject, spaceID, subjectID, func() *pb.Subject { return &pb.Subject{} })
}

func (s *Store) ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, keyword string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	keyword = strings.ToLower(strings.TrimSpace(keyword))
	allow := stringSet(subjectIDs)
	items, err := decodeEntries(s.list(kindSubject, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(subjectType == "" || item.SubjectType == subjectType) &&
			(market == "" || item.Market == market) &&
			(len(allow) == 0 || allow[item.ID]) &&
			(keyword == "" || strings.Contains(strings.ToLower(item.ID), keyword) || strings.Contains(strings.ToLower(item.SubjectType), keyword) || strings.Contains(strings.ToLower(item.Name), keyword) || strings.Contains(strings.ToLower(item.Market), keyword))
	}), func() *pb.Subject { return &pb.Subject{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListSubjectSymbols(ctx context.Context, spaceID string, subjectID string, dataSourceID string, externalSymbol string, page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindSubjectSymbol, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(subjectID == "" || item.SubjectID == subjectID) &&
			(dataSourceID == "" || item.DataSourceID == dataSourceID) &&
			(externalSymbol == "" || item.ExternalSymbol == externalSymbol)
	}), func() *pb.SubjectSymbol { return &pb.SubjectSymbol{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	return getProto(s, ctx, kindDataset, spaceID, datasetID, func() *pb.Dataset { return &pb.Dataset{} })
}

// GetDatasetColumn returns one immutable Dataset column from the current snapshot.
// The column key is scoped by Dataset, so callers cannot accidentally cross spaces.
func (s *Store) GetDatasetColumn(ctx context.Context, spaceID string, datasetID string, columnName string) (*pb.DatasetColumn, error) {
	return getProto(s, ctx, kindDatasetColumn, spaceID, compositeID(datasetID, columnName), func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
}

func (s *Store) ListDatasets(ctx context.Context, query metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDataset, func(item entry) bool {
		if query.SpaceID != "" && item.SpaceID != query.SpaceID {
			return false
		}
		if query.DataSourceID != "" && item.DataSourceID != query.DataSourceID {
			return false
		}
		if query.DataNodeID != "" && item.DataNodeID != query.DataNodeID {
			return false
		}
		if len(query.DataNodeIDs) > 0 && !containsString(query.DataNodeIDs, item.DataNodeID) {
			return false
		}
		return (query.DataKind == pb.DataKind_DATA_KIND_UNSPECIFIED || item.DataKind == int32(query.DataKind)) &&
			(query.Freq == "" || containsString(item.Freqs, query.Freq))
	}), func() *pb.Dataset { return &pb.Dataset{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, query.Page)
}

func (s *Store) ListDatasetSubjects(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	var raw []entry
	if spaceID != "" && datasetID != "" {
		raw = s.listByIndex(indexDataset, kindDatasetSubject, spaceID, datasetID)
		if subjectID != "" {
			filtered := make([]entry, 0, len(raw))
			for _, item := range raw {
				if item.SubjectID == subjectID {
					filtered = append(filtered, item)
				}
			}
			raw = filtered
		}
	} else {
		raw = s.list(kindDatasetSubject, func(item entry) bool {
			return (spaceID == "" || item.SpaceID == spaceID) &&
				(datasetID == "" || item.DatasetID == datasetID) &&
				(subjectID == "" || item.SubjectID == subjectID)
		})
	}
	items, err := decodeEntries(raw, func() *pb.DatasetSubject { return &pb.DatasetSubject{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error) {
	return getProto(s, ctx, kindField, spaceID, fieldID, func() *pb.Field { return &pb.Field{} })
}

func (s *Store) GetFieldGroup(ctx context.Context, spaceID string, groupID string) (*pb.FieldGroup, error) {
	return getProto(s, ctx, kindFieldGroup, spaceID, groupID, func() *pb.FieldGroup { return &pb.FieldGroup{} })
}

func (s *Store) ListFieldGroups(ctx context.Context, spaceID string, parentGroupID string, page *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindFieldGroup, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(parentGroupID == "" || item.ParentGroupID == parentGroupID)
	}), func() *pb.FieldGroup { return &pb.FieldGroup{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListFields(ctx context.Context, query metadata.FieldQuery) ([]*pb.Field, *pb.PageResult, error) {
	keyword := strings.ToLower(strings.TrimSpace(query.Keyword))
	groups := make(map[string]bool)
	if query.GroupID != "" && query.IncludeDescendants {
		groups[query.GroupID] = true
		for _, item := range s.list(kindFieldGroup, func(item entry) bool {
			return item.SpaceID == query.SpaceID && item.ParentGroupID == query.GroupID
		}) {
			groups[item.ID] = true
		}
	}
	items, err := decodeEntries(s.list(kindField, func(item entry) bool {
		if query.UngroupedOnly && item.GroupID != "" {
			return false
		}
		if query.GroupID != "" && query.IncludeDescendants && !groups[item.GroupID] {
			return false
		}
		if query.GroupID != "" && !query.IncludeDescendants && item.GroupID != query.GroupID {
			return false
		}
		if keyword != "" && !strings.Contains(strings.ToLower(string(item.Payload)), keyword) {
			return false
		}
		return (query.SpaceID == "" || item.SpaceID == query.SpaceID) &&
			(query.Status == "" || item.Status == query.Status) &&
			(query.ValueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED || item.ValueType == int32(query.ValueType))
	}), func() *pb.Field { return &pb.Field{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, query.Page)
}

func (s *Store) CountFieldsByGroup(ctx context.Context, spaceID string) (metadata.FieldGroupCounts, error) {
	result := metadata.FieldGroupCounts{ByGroup: make(map[string]uint64)}
	groups, err := decodeEntries(s.list(kindFieldGroup, func(item entry) bool { return spaceID == "" || item.SpaceID == spaceID }), func() *pb.FieldGroup { return &pb.FieldGroup{} })
	if err != nil {
		return result, err
	}
	fields, err := decodeEntries(s.list(kindField, func(item entry) bool { return spaceID == "" || item.SpaceID == spaceID }), func() *pb.Field { return &pb.Field{} })
	if err != nil {
		return result, err
	}
	parents := make(map[string]string, len(groups))
	for _, group := range groups {
		if group != nil {
			parents[group.GetGroupId()] = group.GetParentGroupId()
			if _, ok := result.ByGroup[group.GetGroupId()]; !ok {
				result.ByGroup[group.GetGroupId()] = 0
			}
		}
	}
	for _, field := range fields {
		if field == nil {
			continue
		}
		result.Total++
		groupID := field.GetGroupId()
		if groupID == "" {
			result.Ungrouped++
			continue
		}
		result.ByGroup[groupID]++
	}
	for groupID, parentID := range parents {
		if parentID != "" {
			result.ByGroup[parentID] += result.ByGroup[groupID]
		}
	}
	return result, nil
}

func (s *Store) GetFactor(ctx context.Context, spaceID string, factorID string) (*pb.Factor, error) {
	return getProto(s, ctx, kindFactor, spaceID, factorID, func() *pb.Factor { return &pb.Factor{} })
}

func (s *Store) ListFactors(ctx context.Context, spaceID string, algorithm string, page *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindFactor, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) && (algorithm == "" || item.Algorithm == algorithm)
	}), func() *pb.Factor { return &pb.Factor{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	var raw []entry
	if spaceID != "" && datasetID != "" {
		raw = s.listByIndex(indexDataset, kindDatasetColumn, spaceID, datasetID)
	} else {
		raw = s.list(kindDatasetColumn, func(item entry) bool {
			return (spaceID == "" || item.SpaceID == spaceID) &&
				(datasetID == "" || item.DatasetID == datasetID)
		})
	}
	items, err := decodeEntries(raw, func() *pb.DatasetColumn { return &pb.DatasetColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetDataNode(ctx context.Context, nodeID string) (*pb.DataNode, error) {
	return getProto(s, ctx, kindDataNode, "", nodeID, func() *pb.DataNode { return &pb.DataNode{} })
}

func (s *Store) ListDataNodes(ctx context.Context, page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDataNode, nil), func() *pb.DataNode { return &pb.DataNode{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (*pb.Device, error) {
	return getProto(s, ctx, kindDevice, "", deviceID, func() *pb.Device { return &pb.Device{} })
}

func (s *Store) ListDevices(ctx context.Context, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	raw := s.list(kindDevice, func(item entry) bool { return engine == "" || item.Engine == engine })
	items, err := decodeEntries(raw, func() *pb.Device { return &pb.Device{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	var raw []entry
	if spaceID != "" && datasetID != "" {
		raw = s.listByIndex(indexDataset, kindArchiveFile, spaceID, datasetID)
	} else {
		raw = s.list(kindArchiveFile, func(item entry) bool {
			return (spaceID == "" || item.SpaceID == spaceID) && (datasetID == "" || item.DatasetID == datasetID)
		})
	}
	items, err := decodeEntries(raw, func() *pb.ArchiveFile { return &pb.ArchiveFile{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func getProto[T proto.Message](s *Store, ctx context.Context, kind string, spaceID string, id string, newMessage func() T) (T, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
	}
	item, ok := s.cache.Get(indexPrimary, kind, spaceID, id)
	if !ok {
		var zero T
		return zero, notFound(kind, spaceID, id)
	}
	return decodeEntry(item, newMessage)
}

func (s *Store) list(kind string, predicate func(entry) bool) []entry {
	filters := []snapshotcache.Filter[entry]{snapshotcache.Eq[entry](indexKind, kind)}
	if predicate != nil {
		filters = append(filters, snapshotcache.Where(predicate))
	}
	return s.cache.List(snapshotcache.Query[entry]{Filters: filters})
}

func (s *Store) listByIndex(indexName string, key ...string) []entry {
	return s.cache.List(snapshotcache.Query[entry]{
		Filters: []snapshotcache.Filter[entry]{snapshotcache.Eq[entry](indexName, key...)},
	})
}

func (s *MetadataSnapshot) snapshotList(kind string, predicate func(entry) bool) []entry {
	if s == nil {
		return nil
	}
	out := make([]entry, 0)
	for _, item := range s.items {
		if item.Kind == kind && (predicate == nil || predicate(item)) {
			out = append(out, item)
		}
	}
	return out
}

func snapshotGetProto[T proto.Message](s *MetadataSnapshot, ctx context.Context, kind string, spaceID string, id string, newMessage func() T) (T, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
	}
	for _, item := range s.snapshotList(kind, func(item entry) bool {
		return item.SpaceID == spaceID && item.ID == id
	}) {
		return decodeEntry(item, newMessage)
	}
	var zero T
	return zero, notFound(kind, spaceID, id)
}

func snapshotDecodeEntries[T proto.Message](items []entry, newMessage func() T) ([]T, error) {
	out := make([]T, 0, len(items))
	for _, item := range items {
		value, err := decodeEntry(item, newMessage)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func (s *Store) fetchEntries(ctx context.Context) ([]entry, error) {
	if snapshotter, ok := s.base.(interface {
		WithReadSnapshot(context.Context, func(context.Context) error) error
	}); ok {
		var out []entry
		err := snapshotter.WithReadSnapshot(ctx, func(snapshotCtx context.Context) error {
			var err error
			out, err = s.fetchEntriesFrom(snapshotCtx)
			return err
		})
		return out, err
	}
	return s.fetchEntriesFrom(ctx)
}

func (s *Store) fetchEntriesFrom(ctx context.Context) ([]entry, error) {
	var out []entry
	var err error

	if out, err = s.fetchSpaces(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDataSources(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchSubjects(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchSubjectSymbols(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDatasets(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDatasetSubjects(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchFieldGroups(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchFields(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchFactors(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDatasetColumns(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchViews(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchViewColumns(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDataNodes(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDevices(ctx, out); err != nil {
		return nil, err
	}
	return s.fetchArchiveFiles(ctx, out)
}

func (s *Store) fetchSpaces(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
		return s.base.ListSpaces(ctx, "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindSpace, ID: item.GetSpaceId(), Owner: item.GetOwner(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDataSources(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
		return s.base.ListDataSources(ctx, "", "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataSource, SpaceID: item.GetSpaceId(), ID: item.GetDataSourceId(), Name: item.GetName(), DataSourceKind: item.GetKind(), Market: item.GetMarket(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchSubjects(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
		return s.base.ListSubjects(ctx, "", "", "", nil, "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindSubject, SpaceID: item.GetSpaceId(), ID: item.GetSubjectId(), Name: item.GetName(), SubjectType: item.GetSubjectType(), Market: item.GetMarket(), Currency: item.GetCurrency(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchSubjectSymbols(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
		return s.base.ListSubjectSymbols(ctx, "", "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindSubjectSymbol, SpaceID: item.GetSpaceId(), ID: subjectSymbolID(item), SubjectID: item.GetSubjectId(), DataSourceID: item.GetDataSourceId(), ExternalSymbol: item.GetExternalSymbol(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDatasets(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
		return s.base.ListDatasets(ctx, metadata.DatasetQuery{Page: page})
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataset, SpaceID: item.GetSpaceId(), ID: item.GetDatasetId(), DatasetID: item.GetDatasetId(), DataSourceID: item.GetDataSourceId(), DataKind: int32(item.GetDataKind()), Freqs: item.GetFreqs(), KeepDuration: item.GetKeepDuration(), DataNodeID: item.GetDataNodeId(), BindingLocked: item.GetBindingLocked(), Revision: item.GetRevision(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDatasetSubjects(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
		return s.base.ListDatasetSubjects(ctx, "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDatasetSubject, SpaceID: item.GetSpaceId(), ID: dataSetSubjectID(item), DatasetID: item.GetDatasetId(), SubjectID: item.GetSubjectId(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchFields(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Field, *pb.PageResult, error) {
		return s.base.ListFields(ctx, metadata.FieldQuery{Page: page})
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindField, SpaceID: item.GetSpaceId(), ID: item.GetFieldId(), GroupID: item.GetGroupId(), ValueType: int32(item.GetValueType()), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchFieldGroups(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.FieldGroup, *pb.PageResult, error) {
		return s.base.ListFieldGroups(ctx, "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindFieldGroup, SpaceID: item.GetSpaceId(), ID: item.GetGroupId(), ParentGroupID: item.GetParentGroupId(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchFactors(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
		return s.base.ListFactors(ctx, "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindFactor, SpaceID: item.GetSpaceId(), ID: item.GetFactorId(), Algorithm: item.GetAlgorithm(), ValueType: int32(item.GetValueType()), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDatasetColumns(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
		return s.base.ListDatasetColumns(ctx, "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDatasetColumn, SpaceID: item.GetSpaceId(), DatasetID: item.GetDatasetId(), ID: compositeID(item.GetDatasetId(), item.GetColumnName()), ValueType: int32(item.GetValueType()), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchViews(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.View, *pb.PageResult, error) {
		return s.base.ListViews(ctx, "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindView, SpaceID: item.GetSpaceId(), ID: item.GetViewId(), DatasetID: item.GetPrimaryDatasetId(), DatasetIDs: item.GetDatasetIds(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchViewColumns(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
		return s.base.ListViewColumns(ctx, "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindViewColumn, SpaceID: item.GetSpaceId(), ViewID: item.GetViewId(), ID: compositeID(item.GetViewId(), item.GetColumnName()), ValueType: int32(item.GetValueType())}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDataNodes(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DataNode, *pb.PageResult, error) {
		return s.base.ListDataNodes(ctx, page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataNode, ID: item.GetNodeId(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDevices(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
		return s.base.ListDevices(ctx, "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDevice, ID: item.GetDeviceId(), Engine: item.GetEngine(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchArchiveFiles(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
		return s.base.ListArchiveFiles(ctx, "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindArchiveFile, SpaceID: item.GetSpaceId(), ID: item.GetArchiveFileId(), DatasetID: item.GetDatasetId(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func appendEntry(out []entry, item entry, message proto.Message) ([]entry, error) {
	if message == nil {
		return out, nil
	}
	payload, err := proto.Marshal(message)
	if err != nil {
		return nil, err
	}
	item.Payload = payload
	return append(out, item), nil
}

func subjectSymbolID(item *pb.SubjectSymbol) string {
	return item.GetSubjectId() + "\x00" + item.GetDataSourceId() + "\x00" + item.GetExternalSymbol()
}

func dataSetSubjectID(item *pb.DatasetSubject) string {
	return item.GetDatasetId() + "\x00" + item.GetSubjectId()
}

func decodeEntry[T proto.Message](item entry, newMessage func() T) (T, error) {
	message := newMessage()
	if err := proto.Unmarshal(item.Payload, message); err != nil {
		var zero T
		return zero, err
	}
	return message, nil
}

func decodeEntries[T proto.Message](items []entry, newMessage func() T) ([]T, error) {
	out := make([]T, 0, len(items))
	for _, item := range items {
		decoded, err := decodeEntry(item, newMessage)
		if err != nil {
			return nil, err
		}
		out = append(out, decoded)
	}
	return out, nil
}

func collectPages[T any](ctx context.Context, list func(page *pb.Page) ([]T, *pb.PageResult, error)) ([]T, error) {
	const pageSize uint32 = 1000
	var out []T
	for pageNo := uint32(1); ; pageNo++ {
		items, result, err := list(&pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if result == nil || !result.GetHasMore() {
			return out, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

func pageItems[T any](items []T, page *pb.Page) ([]T, *pb.PageResult, error) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	offset64 := uint64(pageNo-1) * uint64(size)
	maxInt := int(^uint(0) >> 1)
	start := int(offset64)
	if offset64 > uint64(maxInt) || start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(items)), HasMore: end < len(items)}, nil
}

func notFound(kind string, spaceID string, id string) error {
	return fmt.Errorf("metadata row not found: %s/%s/%s: %w", kind, spaceID, id, sql.ErrNoRows)
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func compositeID(owner, name string) string {
	return owner + "\x00" + name
}

func stringSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}
