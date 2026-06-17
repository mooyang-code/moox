package search

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	devicebleve "github.com/mooyang-code/moox/modules/storage/internal/infra/device/bleve"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type Options struct {
	Root      string
	BlevePath string
	Metadata  metadata.Store
}

type Service struct {
	root      string
	blevePath string
	metadata  metadata.Store
	indexes   sync.Map
}

type SearchRequest struct {
	SpaceID    string
	DatasetID  string
	SubjectIDs []string
	TextQuery  string
	TimeRange  *pb.TimeRange
	Page       *pb.Page
}

func NewService(opts Options) *Service {
	return &Service{
		root:      opts.Root,
		blevePath: opts.BlevePath,
		metadata:  opts.Metadata,
	}
}

func (s *Service) IndexRows(ctx context.Context, rows []*pb.DataRow) error {
	if len(rows) == 0 {
		return nil
	}
	if s == nil || s.metadata == nil {
		return errors.New("search metadata is required")
	}
	type datasetKey struct {
		spaceID   string
		datasetID string
	}
	grouped := make(map[datasetKey][]*pb.DataRow)
	for _, row := range rows {
		scope := row.GetKey().GetScope()
		key := datasetKey{spaceID: scope.GetSpaceId(), datasetID: scope.GetDatasetId()}
		grouped[key] = append(grouped[key], row)
	}
	for key, datasetRows := range grouped {
		columns, _, err := s.metadata.ListDataSetColumns(ctx, key.spaceID, key.datasetID, true, nil)
		if err != nil {
			return err
		}
		indexed := make(map[string]bool, len(columns))
		for _, column := range columns {
			indexed[column.GetColumnName()] = true
		}
		if len(indexed) == 0 {
			continue
		}
		index, err := s.searchIndex()
		if err != nil {
			return err
		}
		if err := index.IndexRows(ctx, datasetRows, indexed); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) SearchRows(ctx context.Context, req SearchRequest) ([]*pb.DataRow, *pb.PageResult, error) {
	if s == nil {
		return nil, nil, errors.New("search service is required")
	}
	index, err := s.searchIndex()
	if err != nil {
		return nil, nil, err
	}
	return index.SearchRows(ctx, devicebleve.SearchRequest{
		SpaceID:    req.SpaceID,
		DatasetID:  req.DatasetID,
		SubjectIDs: req.SubjectIDs,
		TextQuery:  req.TextQuery,
		TimeRange:  req.TimeRange,
		Page:       req.Page,
	})
}

func (s *Service) searchIndex() (*devicebleve.Index, error) {
	path := filepath.Join(s.root, "bleve", "default")
	if s.blevePath != "" {
		path = filepath.Join(s.blevePath, "default")
	}
	if value, ok := s.indexes.Load(path); ok {
		return value.(*devicebleve.Index), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	index, err := devicebleve.Open(devicebleve.Options{Path: path})
	if err != nil {
		return nil, err
	}
	actual, loaded := s.indexes.LoadOrStore(path, index)
	if loaded {
		_ = index.Close()
	}
	return actual.(*devicebleve.Index), nil
}
