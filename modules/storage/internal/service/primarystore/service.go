package primarystore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	primary "github.com/mooyang-code/moox/modules/storage/internal/service/datashard"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datashard/messagepublisher"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	metasqlite "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	"github.com/mooyang-code/moox/modules/storage/internal/service/primarystore/schema"
	"github.com/mooyang-code/moox/modules/storage/internal/service/primarystore/shardrouter"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现元数据、写入、权威读取和视图查询入口。
type Service struct {
	root              string
	metadata          metadata.Store
	metadataReader    metadata.Reader
	metadataCache     *metacache.Store
	validator         *schema.Validator
	router            *router.Resolver
	primary           primary.Client
	events            messagepublisher.Publisher
	report            ViewErrorReporter
	cleanupDeleteRows func(context.Context, *pb.DeleteTimeSeriesRowsReq) (*pb.DeleteTimeSeriesRowsRsp, error)
	topologyMu        sync.Mutex
	recordVersionMu   sync.Mutex
	lastRecordVersion time.Time
}

var (
	_ pb.MetadataService         = (*Service)(nil)
	_ pb.PrimaryStoreService     = (*Service)(nil)
	_ pb.PrimaryStoreScanService = (*Service)(nil)
)

func NewServiceWithOptions(opts Options) *Service {
	root := storageRoot(opts.Root)
	meta := opts.Metadata
	reader := opts.MetadataReader
	var cacheReader *metacache.Store
	if meta == nil {
		var err error
		meta, cacheReader, err = openDefaultMetadataStores(trpc.BackgroundContext(), root, opts.MetadataPath, opts.InitSchemaPath)
		if err != nil {
			panic(fmt.Sprintf("open storage metadata store: %v", err))
		}
	}
	if reader == nil {
		if cacheReader != nil {
			reader = cacheReader
		} else {
			reader = meta
		}
	}
	primaryClient := opts.PrimaryClient
	if primaryClient == nil && opts.PrimaryServiceName != "" {
		primaryClient = primary.NewRemoteClient(opts.PrimaryServiceName)
	}
	if primaryClient == nil {
		primaryClient = primary.NewLocalClient(primary.LocalClientOptions{Root: root, PebblePath: opts.PebblePath, ShardID: opts.ShardID})
	}
	events := opts.Events
	if events == nil {
		events = messagepublisher.NewMemoryBus()
	}
	reporter := opts.ViewErrors
	if reporter == nil {
		reporter = logViewError
	}
	svc := &Service{
		root:           root,
		metadata:       meta,
		metadataReader: reader,
		metadataCache:  cacheReader,
		validator:      schema.NewValidator(reader),
		router:         router.NewResolver(reader),
		primary:        primaryClient,
		events:         events,
		report:         reporter,
	}
	return svc
}

func (s *Service) MetadataStore() metadata.Store {
	if s == nil {
		return nil
	}
	return s.metadata
}

func (s *Service) MetadataReader() metadata.Reader {
	if s == nil {
		return nil
	}
	return s.metadataReader
}

func (s *Service) refreshMetadataCache(ctx context.Context) error {
	if s == nil || s.metadataCache == nil {
		return nil
	}
	return s.metadataCache.Refresh(ctx)
}

func (s *Service) refreshMetadataCacheAfterCommit(ctx context.Context, operation string) {
	if err := s.refreshMetadataCache(ctx); err != nil {
		log.ErrorContextf(ctx, "%s committed but metadata cache refresh failed: %v", operation, err)
	}
}

// Close releases dependencies owned by the access service. Event transport is
// bootstrap-owned and is deliberately not closed here.
func (s *Service) Close() error {
	var firstErr error
	if closer, ok := s.primary.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.metadataCache != nil {
		if err := s.metadataCache.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if closer, ok := s.metadata.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Ready verifies that the metadata schema is queryable and the configured
// primary client exists. Filesystem checks alone cannot establish that the
// access service is able to route or serve requests.
func (s *Service) Ready() bool {
	if s == nil || s.metadata == nil || s.primary == nil {
		return false
	}
	ctx := trpc.BackgroundContext()
	if _, err := s.metadata.TableNames(ctx); err != nil {
		return false
	}
	var routes []*pb.PrimaryStoreRoute
	for pageNo := uint32(1); ; pageNo++ {
		pageRoutes, page, err := s.metadata.ListPrimaryStoreRoutes(ctx, "", "", "", "", &pb.Page{Page: pageNo, Size: 1000})
		if err != nil {
			return false
		}
		routes = append(routes, pageRoutes...)
		if page == nil || !page.GetHasMore() || len(pageRoutes) == 0 {
			break
		}
	}
	if len(routes) == 0 {
		return true
	}
	headReader, ok := s.primary.(primary.HeadReader)
	if !ok {
		return false
	}
	for _, route := range routes {
		if route == nil || (route.GetStatus() != "" && route.GetStatus() != "active") {
			continue
		}
		target, err := s.router.Resolve(ctx, route.GetSpaceId(), route.GetDatasetId(), route.GetSubjectId())
		if err != nil {
			return false
		}
		if _, err := headReader.HeadSequence(ctx, target); err != nil {
			return false
		}
	}
	return true
}

func openDefaultMetadataStores(ctx context.Context, root string, metadataPath string, initSchemaPath string) (metadata.Store, *metacache.Store, error) {
	if metadataPath == "" {
		metadataPath = filepath.Join(root, "metadata", "storage_metadata.db")
	}
	metaDir := filepath.Dir(metadataPath)
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		return nil, nil, err
	}
	store, err := metasqlite.Open(ctx, metasqlite.Options{
		Path:       metadataPath,
		SchemaPath: initSchemaPath,
	})
	if err != nil {
		return nil, nil, err
	}
	if initSchemaPath != "" {
		if err := store.InitSchema(ctx); err != nil {
			_ = store.Close()
			return nil, nil, err
		}
	} else if err := requireMetadataSchema(ctx, store); err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	cached, err := metacache.New(ctx, store, metacache.Options{})
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return store, cached, nil
}

func requireMetadataSchema(ctx context.Context, store metadata.Store) error {
	tables, err := store.TableNames(ctx)
	if err != nil {
		return err
	}
	required := map[string]bool{
		"t_spaces":               false,
		"t_datasets":             false,
		"t_primary_store_routes": false,
	}
	for _, table := range tables {
		if _, ok := required[table]; ok {
			required[table] = true
		}
	}
	var missing []string
	for table, exists := range required {
		if !exists {
			missing = append(missing, table)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("metadata schema not initialized, missing tables: %s", strings.Join(missing, ", "))
	}
	return nil
}

func storageRoot(root string) string {
	if root != "" {
		return root
	}
	if env := os.Getenv("MOOX_STORAGE_HOME"); env != "" {
		return env
	}
	return "var/storage"
}

func logViewError(ctx context.Context, stage string, err error) {
	if err == nil {
		return
	}
	log.WarnContextf(ctx, "[StoragePrimary] view stage %s failed: %v", stage, err)
}
