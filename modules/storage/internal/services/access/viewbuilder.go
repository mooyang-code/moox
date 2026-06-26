package access

import (
	"context"

	"github.com/mooyang-code/moox/modules/storage/internal/services/view"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func (s *Service) InitViewBuilder() error {
	return s.InitViewBuilderWithFacts(s.timeSeriesFactReaderOrDefault())
}

func (s *Service) InitViewBuilderWithFacts(facts view.FactReader) error {
	views, err := s.viewStore()
	if err != nil {
		return err
	}
	if facts == nil {
		facts = s.timeSeriesFactReaderOrDefault()
	}
	s.timeSeriesFactReader = facts
	if reader, ok := facts.(viewFactReadService); ok {
		s.viewFactReader = reader
	}
	view.SetDefaultBuilder(view.NewBuilder(view.Options{
		Metadata: s.metadata,
		Facts:    facts,
		Views:    views,
		Search:   s.search,
		OnBuildStarted: func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) {
			s.startViewDirtyTracking(pb.DataKind_DATA_KIND_TIME_SERIES, item, targetVersion, resultName)
		},
		BeforeComplete: func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) error {
			return s.drainTimeSeriesDirty(ctx, viewDirtyHandle(pb.DataKind_DATA_KIND_TIME_SERIES, item.GetSpaceId(), item.GetViewId(), targetVersion, resultName))
		},
		OnBuildFinished: func(ctx context.Context, item *pb.View, targetVersion uint64, resultName string) {
			s.stopViewDirtyTracking(viewDirtyHandle(pb.DataKind_DATA_KIND_TIME_SERIES, item.GetSpaceId(), item.GetViewId(), targetVersion, resultName))
		},
	}))
	return nil
}

func (s *Service) viewFactReaderOrDefault() viewFactReadService {
	if s != nil && s.viewFactReader != nil {
		return s.viewFactReader
	}
	return s
}

func (s *Service) timeSeriesFactReaderOrDefault() view.FactReader {
	if s != nil && s.timeSeriesFactReader != nil {
		return s.timeSeriesFactReader
	}
	return s
}
