package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/snapshotcache"
	"google.golang.org/protobuf/proto"
)

const (
	RefreshDisabled time.Duration = -1

	indexKind    = "kind"
	indexPrimary = "primary"
)

const (
	kindSpace          = "space"
	kindView           = "view"
	kindViewColumn     = "view_column"
	kindDataSource     = "data_source"
	kindSubject        = "subject"
	kindSubjectSymbol  = "subject_symbol"
	kindDataSet        = "dataset"
	kindDataSetSubject = "dataset_subject"
	kindField          = "field"
	kindFactor         = "factor"
	kindDataSetColumn  = "dataset_column"
	kindStorageNode    = "storage_node"
	kindDevice         = "device"
	kindStorageRoute   = "storage_route"
	kindArchiveFile    = "archive_file"
)

type Options struct {
	RefreshInterval    time.Duration
	RefreshTimeout     time.Duration
	InitialLoadTimeout time.Duration
	RandomStartDelay   time.Duration
}

type Store struct {
	base  metadata.Reader
	cache *snapshotcache.Cache[entry]
}

type entry struct {
	Kind           string   `json:"kind"`
	SpaceID        string   `json:"space_id,omitempty"`
	ID             string   `json:"id,omitempty"`
	Owner          string   `json:"owner,omitempty"`
	Status         string   `json:"status,omitempty"`
	DataSourceID   string   `json:"data_source_id,omitempty"`
	DataSourceKind string   `json:"data_source_kind,omitempty"`
	Market         string   `json:"market,omitempty"`
	SubjectType    string   `json:"subject_type,omitempty"`
	SubjectID      string   `json:"subject_id,omitempty"`
	ExternalSymbol string   `json:"external_symbol,omitempty"`
	DataSetID      string   `json:"dataset_id,omitempty"`
	DataSetIDs     []string `json:"dataset_ids,omitempty"`
	DataKind       int32    `json:"data_kind,omitempty"`
	Freqs          []string `json:"freqs,omitempty"`
	ViewID         string   `json:"view_id,omitempty"`
	ValueType      int32    `json:"value_type,omitempty"`
	TextIndexed    bool     `json:"text_indexed,omitempty"`
	Algorithm      string   `json:"algorithm,omitempty"`
	NodeID         string   `json:"node_id,omitempty"`
	Engine         string   `json:"engine,omitempty"`
	Payload        []byte   `json:"payload"`
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	items, err := decodeEntries(s.list(kindView, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(status == "" || item.Status == status) &&
			(datasetID == "" || item.DataSetID == datasetID || containsString(item.DataSetIDs, datasetID))
	}), func() *pb.View { return &pb.View{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
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

func (s *Store) ListDataSources(ctx context.Context, spaceID string, kind string, market string, page *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDataSource, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(kind == "" || item.DataSourceKind == kind) &&
			(market == "" || item.Market == market)
	}), func() *pb.DataSource { return &pb.DataSource{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetSubject(ctx context.Context, spaceID string, subjectID string) (*pb.Subject, error) {
	return getProto(s, ctx, kindSubject, spaceID, subjectID, func() *pb.Subject { return &pb.Subject{} })
}

func (s *Store) ListSubjects(ctx context.Context, spaceID string, subjectType string, market string, subjectIDs []string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	allow := stringSet(subjectIDs)
	items, err := decodeEntries(s.list(kindSubject, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(subjectType == "" || item.SubjectType == subjectType) &&
			(market == "" || item.Market == market) &&
			(len(allow) == 0 || allow[item.ID])
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

func (s *Store) GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error) {
	return getProto(s, ctx, kindDataSet, spaceID, datasetID, func() *pb.DataSet { return &pb.DataSet{} })
}

func (s *Store) ListDataSets(ctx context.Context, spaceID string, dataSourceID string, dataKind pb.DataKind, freq string, page *pb.Page) ([]*pb.DataSet, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDataSet, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(dataSourceID == "" || item.DataSourceID == dataSourceID) &&
			(dataKind == pb.DataKind_DATA_KIND_UNSPECIFIED || item.DataKind == int32(dataKind)) &&
			(freq == "" || containsString(item.Freqs, freq))
	}), func() *pb.DataSet { return &pb.DataSet{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListDataSetSubjects(ctx context.Context, spaceID string, datasetID string) ([]*pb.DataSetSubject, error) {
	items, _, err := s.ListDataSetSubjectsPage(ctx, spaceID, datasetID, "", nil)
	return items, err
}

func (s *Store) ListDataSetSubjectsPage(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DataSetSubject, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDataSetSubject, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(datasetID == "" || item.DataSetID == datasetID) &&
			(subjectID == "" || item.SubjectID == subjectID)
	}), func() *pb.DataSetSubject { return &pb.DataSetSubject{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetField(ctx context.Context, spaceID string, fieldID string) (*pb.Field, error) {
	return getProto(s, ctx, kindField, spaceID, fieldID, func() *pb.Field { return &pb.Field{} })
}

func (s *Store) ListFields(ctx context.Context, spaceID string, valueType pb.FieldValueType, page *pb.Page) ([]*pb.Field, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindField, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(valueType == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED || item.ValueType == int32(valueType))
	}), func() *pb.Field { return &pb.Field{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
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

func (s *Store) ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDataSetColumn, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(datasetID == "" || item.DataSetID == datasetID) &&
			(!textIndexedOnly || item.TextIndexed)
	}), func() *pb.DataSetColumn { return &pb.DataSetColumn{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetStorageNode(ctx context.Context, nodeID string) (*pb.StorageNode, error) {
	return getProto(s, ctx, kindStorageNode, "", nodeID, func() *pb.StorageNode { return &pb.StorageNode{} })
}

func (s *Store) ListStorageNodes(ctx context.Context, page *pb.Page) ([]*pb.StorageNode, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindStorageNode, nil), func() *pb.StorageNode { return &pb.StorageNode{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetDevice(ctx context.Context, deviceID string) (*pb.Device, error) {
	return getProto(s, ctx, kindDevice, "", deviceID, func() *pb.Device { return &pb.Device{} })
}

func (s *Store) ListDevices(ctx context.Context, nodeID string, engine string, page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindDevice, func(item entry) bool {
		return (nodeID == "" || item.NodeID == nodeID) && (engine == "" || item.Engine == engine)
	}), func() *pb.Device { return &pb.Device{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) GetStorageRoute(ctx context.Context, spaceID string, routeID string) (*pb.StorageRoute, error) {
	return getProto(s, ctx, kindStorageRoute, spaceID, routeID, func() *pb.StorageRoute { return &pb.StorageRoute{} })
}

func (s *Store) ListStorageRoutes(ctx context.Context, spaceID string, datasetID string, subjectID string, nodeID string, page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindStorageRoute, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) &&
			(datasetID == "" || item.DataSetID == datasetID) &&
			(subjectID == "" || item.SubjectID == subjectID) &&
			(nodeID == "" || item.NodeID == nodeID)
	}), func() *pb.StorageRoute { return &pb.StorageRoute{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func (s *Store) ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	items, err := decodeEntries(s.list(kindArchiveFile, func(item entry) bool {
		return (spaceID == "" || item.SpaceID == spaceID) && (datasetID == "" || item.DataSetID == datasetID)
	}), func() *pb.ArchiveFile { return &pb.ArchiveFile{} })
	if err != nil {
		return nil, nil, err
	}
	return pageItems(items, page)
}

func getProto[T proto.Message](s *Store, ctx context.Context, kind string, spaceID string, id string, newMessage func() T) (T, error) {
	_ = ctx
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

func (s *Store) fetchEntries(ctx context.Context) ([]entry, error) {
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
	if out, err = s.fetchDataSets(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDataSetSubjects(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchFields(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchFactors(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDataSetColumns(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchViews(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchViewColumns(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchStorageNodes(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchDevices(ctx, out); err != nil {
		return nil, err
	}
	if out, err = s.fetchStorageRoutes(ctx, out); err != nil {
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
		return s.base.ListDataSources(ctx, "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataSource, SpaceID: item.GetSpaceId(), ID: item.GetDataSourceId(), DataSourceKind: item.GetKind(), Market: item.GetMarket(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchSubjects(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
		return s.base.ListSubjects(ctx, "", "", "", nil, page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindSubject, SpaceID: item.GetSpaceId(), ID: item.GetSubjectId(), SubjectType: item.GetSubjectType(), Market: item.GetMarket(), Status: item.GetStatus()}, item)
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

func (s *Store) fetchDataSets(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DataSet, *pb.PageResult, error) {
		return s.base.ListDataSets(ctx, "", "", pb.DataKind_DATA_KIND_UNSPECIFIED, "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataSet, SpaceID: item.GetSpaceId(), ID: item.GetDatasetId(), DataSourceID: item.GetDataSourceId(), DataKind: int32(item.GetDataKind()), Freqs: item.GetFreqs(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDataSetSubjects(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DataSetSubject, *pb.PageResult, error) {
		return s.base.ListDataSetSubjectsPage(ctx, "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataSetSubject, SpaceID: item.GetSpaceId(), ID: dataSetSubjectID(item), DataSetID: item.GetDatasetId(), SubjectID: item.GetSubjectId(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchFields(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Field, *pb.PageResult, error) {
		return s.base.ListFields(ctx, "", pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED, page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindField, SpaceID: item.GetSpaceId(), ID: item.GetFieldId(), ValueType: int32(item.GetValueType()), Status: item.GetStatus()}, item)
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

func (s *Store) fetchDataSetColumns(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error) {
		return s.base.ListDataSetColumns(ctx, "", "", false, page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDataSetColumn, SpaceID: item.GetSpaceId(), DataSetID: item.GetDatasetId(), ID: item.GetDatasetId() + "." + item.GetColumnName(), ValueType: int32(item.GetValueType()), TextIndexed: item.GetTextIndexed(), Status: item.GetStatus()}, item)
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
		out, err = appendEntry(out, entry{Kind: kindView, SpaceID: item.GetSpaceId(), ID: item.GetViewId(), DataSetID: item.GetPrimaryDatasetId(), DataSetIDs: item.GetDatasetIds(), Status: item.GetStatus()}, item)
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
		out, err = appendEntry(out, entry{Kind: kindViewColumn, SpaceID: item.GetSpaceId(), ViewID: item.GetViewId(), ID: item.GetViewId() + "." + item.GetColumnName(), ValueType: int32(item.GetValueType())}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchStorageNodes(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.StorageNode, *pb.PageResult, error) {
		return s.base.ListStorageNodes(ctx, page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindStorageNode, ID: item.GetNodeId(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchDevices(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
		return s.base.ListDevices(ctx, "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindDevice, ID: item.GetDeviceId(), NodeID: item.GetNodeId(), Engine: item.GetEngine(), Status: item.GetStatus()}, item)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) fetchStorageRoutes(ctx context.Context, out []entry) ([]entry, error) {
	items, err := collectPages(ctx, func(page *pb.Page) ([]*pb.StorageRoute, *pb.PageResult, error) {
		return s.base.ListStorageRoutes(ctx, "", "", "", "", page)
	})
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		out, err = appendEntry(out, entry{Kind: kindStorageRoute, SpaceID: item.GetSpaceId(), ID: item.GetRouteId(), DataSetID: item.GetDatasetId(), SubjectID: item.GetSubjectId(), NodeID: item.GetNodeId(), Status: item.GetStatus()}, item)
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
		out, err = appendEntry(out, entry{Kind: kindArchiveFile, SpaceID: item.GetSpaceId(), ID: item.GetArchiveFileId(), DataSetID: item.GetDatasetId(), Status: item.GetStatus()}, item)
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

func dataSetSubjectID(item *pb.DataSetSubject) string {
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
	start := int((pageNo - 1) * size)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint64(len(items)), HasMore: end < len(items)}, nil
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
