package search

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	deviceinfra "github.com/mooyang-code/moox/modules/storage/internal/service/datashard/contracts"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	devicebleve "github.com/mooyang-code/moox/modules/storage/internal/service/viewindex/bleve"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	trpc "trpc.group/trpc-go/trpc-go"
)

var ErrIndexClosing = errors.New("view index is closing")

type Options struct {
	Root string
}

type Service struct {
	root    string
	mu      sync.Mutex
	indexes map[string]*managedIndex
	closed  bool
}

type managedIndex struct {
	index      *devicebleve.Index
	refs       int
	closing    bool
	drained    chan struct{}
	removed    chan struct{}
	removeErr  error
	closeOnce  sync.Once
	closeError error
}

var _ viewindex.ViewIndexEngine = (*Service)(nil)

type SearchRequest struct {
	IndexID      string
	SpaceID      string
	DatasetID    string
	Keys         []*pb.RecordKey
	TextQuery    string
	VersionRange *pb.VersionRange
	Filters      []*pb.FilterExpr
	Page         *pb.Page
	Sorts        []*pb.SortSpec
}

func NewService(opts Options) *Service {
	return &Service{root: strings.TrimSpace(opts.Root), indexes: make(map[string]*managedIndex)}
}

func (s *Service) Engine() string {
	return "bleve"
}

func (s *Service) Prepare(ctx context.Context, indexID string, schema viewindex.ViewIndexSchema) error {
	if _, err := validateBleveIndex(indexID, schema); err != nil {
		return err
	}
	if err := s.Remove(ctx, indexID); err != nil {
		return err
	}
	index, release, err := s.acquire(indexID, true)
	if err != nil {
		return err
	}
	defer release()
	return index.SetSchema(ctx, schema.ViewVersion, schema.SchemaHash, schema.Columns)
}

func (s *Service) Write(ctx context.Context, indexID string, batch viewindex.BatchWrite) error {
	if len(batch.TimeSeriesRows) > 0 {
		return errors.New("bleve view index rejects time series rows")
	}
	indexed := make(map[string]bool, len(batch.Columns))
	for _, column := range batch.Columns {
		if name := strings.TrimSpace(column.GetColumnName()); name != "" {
			indexed[name] = true
		}
	}
	index, release, err := s.acquire(indexID, false)
	if err != nil {
		return err
	}
	defer release()
	return index.IndexRows(ctx, batch.RecordRows, indexed)
}

func (s *Service) Apply(ctx context.Context, indexID string, batch viewindex.ViewIndexApplyBatch) error {
	if err := batch.Validate(); err != nil {
		return err
	}
	index, release, err := s.acquire(indexID, false)
	if err != nil {
		return err
	}
	defer release()
	return index.ApplyRows(ctx, batch)
}

func (s *Service) DeleteRecordRows(ctx context.Context, indexID string, rows []*pb.RecordRow) error {
	index, release, err := s.acquire(indexID, false)
	if err != nil {
		return err
	}
	defer release()
	return index.DeleteRows(ctx, rows)
}

func (s *Service) Stat(ctx context.Context, indexID string) (viewindex.ViewIndexStats, error) {
	path, err := s.pathFor(indexID)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	freeBytes, err := deviceinfra.FreeDiskBytes(s.root)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return viewindex.ViewIndexStats{FreeDiskBytes: freeBytes}, nil
	} else if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	index, release, err := s.acquire(indexID, false)
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats, err := index.Stat(ctx)
	release()
	if err != nil {
		return viewindex.ViewIndexStats{}, err
	}
	stats.PhysicalBytes, err = directorySize(path)
	stats.FreeDiskBytes = freeBytes
	return stats, err
}

func (s *Service) Remove(ctx context.Context, indexID string) error {
	path, err := s.pathFor(indexID)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("bleve index manager is closed")
	}
	handle := s.indexes[indexID]
	if handle == nil {
		handle = &managedIndex{
			closing: true,
			drained: make(chan struct{}),
			removed: make(chan struct{}),
		}
		close(handle.drained)
		s.indexes[indexID] = handle
	} else if handle.closing {
		removed := handle.removed
		s.mu.Unlock()
		return s.waitForRemoval(ctx, handle, removed)
	} else {
		handle.closing = true
		handle.drained = make(chan struct{})
		handle.removed = make(chan struct{})
		handle.removeErr = nil
		if handle.refs == 0 {
			close(handle.drained)
		}
	}
	drained := handle.drained
	s.mu.Unlock()

	select {
	case <-ctx.Done():
		return s.abortRemoval(indexID, handle, ctx.Err())
	case <-drained:
	}

	removeErr := errors.Join(handle.close(), os.RemoveAll(path))
	s.mu.Lock()
	if s.indexes[indexID] == handle {
		delete(s.indexes, indexID)
	}
	handle.removeErr = removeErr
	close(handle.removed)
	s.mu.Unlock()
	return removeErr
}

func (s *Service) waitForRemoval(ctx context.Context, handle *managedIndex, removed <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-removed:
		s.mu.Lock()
		defer s.mu.Unlock()
		return handle.removeErr
	}
}

func (s *Service) abortRemoval(indexID string, handle *managedIndex, removeErr error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current := s.indexes[indexID]; current == handle && current.closing {
		if current.index == nil {
			delete(s.indexes, indexID)
		} else {
			current.closing = false
			current.drained = nil
		}
		current.removeErr = removeErr
		close(current.removed)
	}
	return removeErr
}

func (h *managedIndex) close() error {
	if h == nil || h.index == nil {
		return nil
	}
	h.closeOnce.Do(func() {
		h.closeError = h.index.Close()
	})
	return h.closeError
}

func (s *Service) List(ctx context.Context) ([]string, error) {
	_ = ctx
	root := filepath.Join(s.root, "bleve")
	spaces, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, spaceDir := range spaces {
		if !spaceDir.IsDir() {
			continue
		}
		spaceID, err := decodePathID(spaceDir.Name())
		if err != nil {
			continue
		}
		views, err := os.ReadDir(filepath.Join(root, spaceDir.Name()))
		if err != nil {
			return nil, err
		}
		for _, viewDir := range views {
			if !viewDir.IsDir() {
				continue
			}
			viewID, err := decodePathID(viewDir.Name())
			if err != nil {
				continue
			}
			for _, slot := range []viewindex.Slot{viewindex.SlotA, viewindex.SlotB} {
				path := filepath.Join(root, spaceDir.Name(), viewDir.Name(), string(slot))
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					out = append(out, viewindex.ViewIndexID(spaceID, viewID, slot))
				} else if err != nil && !errors.Is(err, os.ErrNotExist) {
					return nil, err
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *Service) SearchRecordRows(ctx context.Context, req SearchRequest) ([]*pb.RecordRow, *pb.PageResult, error) {
	if s == nil {
		return nil, nil, errors.New("search service is required")
	}
	if strings.TrimSpace(req.IndexID) == "" {
		return nil, nil, errors.New("index_id is required")
	}
	index, release, err := s.acquire(req.IndexID, false)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	return index.SearchRecordRows(ctx, devicebleve.SearchRequest{
		SpaceID:      req.SpaceID,
		DatasetID:    req.DatasetID,
		Keys:         req.Keys,
		TextQuery:    req.TextQuery,
		VersionRange: req.VersionRange,
		Filters:      req.Filters,
		Page:         req.Page,
		Sorts:        req.Sorts,
	})
}

func (s *Service) QueryRecordRows(ctx context.Context, indexID string, datasetID string, req *pb.SearchRecordRowsReq) ([]*pb.ResultColumn, []*pb.RecordRow, *pb.PageResult, error) {
	if req == nil {
		return nil, nil, nil, errors.New("record query is required")
	}
	rows, page, err := s.SearchRecordRows(ctx, SearchRequest{
		IndexID:      indexID,
		SpaceID:      req.GetSpaceId(),
		DatasetID:    datasetID,
		Keys:         req.GetKeys(),
		TextQuery:    req.GetTextQuery(),
		VersionRange: req.GetVersionRange(),
		Filters:      req.GetFilters(),
		Page:         req.GetPage(),
		Sorts:        req.GetSorts(),
	})
	return nil, rows, page, err
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	handles := make([]*managedIndex, 0, len(s.indexes))
	for _, handle := range s.indexes {
		if !handle.closing {
			handle.closing = true
			handle.drained = make(chan struct{})
			if handle.refs == 0 {
				close(handle.drained)
			}
		}
		handles = append(handles, handle)
	}
	s.mu.Unlock()
	for _, handle := range handles {
		<-handle.drained
	}
	s.mu.Lock()
	s.indexes = make(map[string]*managedIndex)
	s.mu.Unlock()
	var err error
	for _, handle := range handles {
		err = errors.Join(err, handle.close())
	}
	return err
}

func (s *Service) acquire(indexID string, create bool) (*devicebleve.Index, func(), error) {
	path, err := s.pathFor(indexID)
	if err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("bleve index manager is closed")
	}
	if handle := s.indexes[indexID]; handle != nil {
		if handle.closing {
			return nil, nil, ErrIndexClosing
		}
		handle.refs++
		return handle.index, s.releaseFunc(indexID, handle), nil
	}
	var index *devicebleve.Index
	if create {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, nil, err
		}
		index, err = devicebleve.Open(devicebleve.Options{Path: path})
	} else {
		index, err = devicebleve.OpenExisting(devicebleve.Options{Path: path})
	}
	if err != nil {
		return nil, nil, err
	}
	handle := &managedIndex{index: index, refs: 1}
	s.indexes[indexID] = handle
	return index, s.releaseFunc(indexID, handle), nil
}

func (s *Service) releaseFunc(indexID string, handle *managedIndex) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if handle.refs == 0 {
				return
			}
			handle.refs--
			if handle.refs == 0 && handle.closing && handle.drained != nil {
				close(handle.drained)
			}
		})
	}
}

func (s *Service) pathFor(indexID string) (string, error) {
	if s.root == "" {
		return "", errors.New("view index root is required")
	}
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return "", err
	}
	return viewindex.BlevePath(s.root, ref), nil
}

func (s *Service) indexPath(indexID string) string {
	path, _ := s.pathFor(indexID)
	return path
}

func validateBleveIndex(indexID string, schema viewindex.ViewIndexSchema) (viewindex.Ref, error) {
	ref, err := viewindex.ParseViewIndexID(indexID)
	if err != nil {
		return viewindex.Ref{}, err
	}
	if schema.Engine != "" && !strings.EqualFold(schema.Engine, "bleve") {
		return viewindex.Ref{}, fmt.Errorf("bleve manager rejects engine %q", schema.Engine)
	}
	if schema.SpaceID != "" && schema.SpaceID != ref.SpaceID {
		return viewindex.Ref{}, errors.New("schema space_id does not match index_id")
	}
	if schema.ViewID != "" && schema.ViewID != ref.ViewID {
		return viewindex.Ref{}, errors.New("schema view_id does not match index_id")
	}
	return ref, nil
}

func directorySize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 0 {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}

func decodePathID(value string) (string, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) == 0 {
		return "", errors.New("invalid encoded view path")
	}
	return string(raw), nil
}
