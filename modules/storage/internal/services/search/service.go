package search

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	devicebleve "github.com/mooyang-code/moox/modules/storage/internal/infra/device/bleve"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Options 保存 Record 搜索服务创建时的依赖与路径配置。
type Options struct {
	Root      string
	BlevePath string
	Metadata  metadata.Reader
}

// Service 实现 Record 视图的索引写入和检索能力。
type Service struct {
	root      string
	blevePath string
	metadata  metadata.Reader
	openMu    sync.Mutex
	indexes   sync.Map
}

// SearchRequest 描述一次 Record 视图检索请求。
type SearchRequest struct {
	ResultName   string
	SpaceID      string
	DatasetID    string
	RecordIDs    []string
	TextQuery    string
	VersionRange *pb.VersionRange
	Page         *pb.Page
}

func NewService(opts Options) *Service {
	return &Service{
		root:      opts.Root,
		blevePath: opts.BlevePath,
		metadata:  opts.Metadata,
	}
}

func (s *Service) IndexRecordViewRows(ctx context.Context, resultName string, columns []*pb.ViewColumn, rows []*pb.RecordRow) error {
	if resultName == "" {
		return errors.New("result_name is required")
	}
	indexed := make(map[string]bool, len(columns))
	for _, column := range columns {
		if column.GetColumnName() != "" {
			indexed[column.GetColumnName()] = true
		}
	}
	index, err := s.searchIndex(resultName, true)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	return index.IndexRows(ctx, rows, indexed)
}

func (s *Service) SearchRecordRows(ctx context.Context, req SearchRequest) ([]*pb.RecordRow, *pb.PageResult, error) {
	if s == nil {
		return nil, nil, errors.New("search service is required")
	}
	if req.ResultName == "" {
		return nil, nil, errors.New("result_name is required")
	}
	index, err := s.searchIndex(req.ResultName, false)
	if err != nil {
		return nil, nil, err
	}
	return index.SearchRecordRows(ctx, devicebleve.SearchRequest{
		SpaceID:      req.SpaceID,
		DatasetID:    req.DatasetID,
		RecordIDs:    req.RecordIDs,
		TextQuery:    req.TextQuery,
		VersionRange: req.VersionRange,
		Page:         req.Page,
	})
}

func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	var firstErr error
	s.indexes.Range(func(key, value any) bool {
		path, _ := key.(string)
		if loaded, ok := s.indexes.LoadAndDelete(path); ok {
			value = loaded
		}
		if index, ok := value.(*devicebleve.Index); ok {
			if err := index.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return true
	})
	return firstErr
}

func (s *Service) searchIndex(resultName string, createIfMissing bool) (*devicebleve.Index, error) {
	resultName = sanitizeRecordIndexName(resultName)
	path := filepath.Join(s.root, "bleve", resultName)
	if s.blevePath != "" {
		path = filepath.Join(s.blevePath, resultName)
	}
	if value, ok := s.indexes.Load(path); ok {
		return value.(*devicebleve.Index), nil
	}
	s.openMu.Lock()
	defer s.openMu.Unlock()
	if value, ok := s.indexes.Load(path); ok {
		return value.(*devicebleve.Index), nil
	}
	var (
		index *devicebleve.Index
		err   error
	)
	if createIfMissing {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		index, err = devicebleve.Open(devicebleve.Options{Path: path})
	} else {
		index, err = devicebleve.OpenExisting(devicebleve.Options{Path: path})
	}
	if err != nil {
		return nil, err
	}
	s.indexes.Store(path, index)
	return index, nil
}

var invalidRecordIndexChar = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func RecordIndexName(spaceID string, viewID string, viewVersion uint64, now time.Time) string {
	if viewVersion == 0 {
		viewVersion = 1
	}
	raw := fmt.Sprintf("record_view_s%s_%s_v%d_r_%d", encodeRecordIndexPart(spaceID), encodeRecordIndexPart(viewID), viewVersion, now.UnixNano())
	name := sanitizeRecordIndexName(raw)
	if name == "" {
		return "record_view"
	}
	return name
}

func encodeRecordIndexPart(value string) string {
	if value == "" {
		return "_"
	}
	return invalidRecordIndexChar.ReplaceAllString(value, "_")
}

func sanitizeRecordIndexName(value string) string {
	value = invalidRecordIndexChar.ReplaceAllString(value, "_")
	if value == "" {
		return ""
	}
	return value
}
