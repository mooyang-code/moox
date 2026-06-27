package access

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"google.golang.org/protobuf/proto"
)

// viewDirtyBuild 记录某个 View 正在处理的脏版本构建任务。
type viewDirtyBuild struct {
	kind       pb.DataKind
	spaceID    string
	viewID     string
	version    uint64
	resultName string
	view       *pb.View
	datasets   map[string]bool
	timeSeries map[string]*pb.TimeSeriesKey
}

func (s *Service) startViewDirtyTracking(kind pb.DataKind, item *pb.View, targetVersion uint64, resultName string) string {
	if s == nil || item == nil || resultName == "" {
		return ""
	}
	handle := viewDirtyHandle(kind, item.GetSpaceId(), item.GetViewId(), targetVersion, resultName)
	build := &viewDirtyBuild{
		kind:       kind,
		spaceID:    item.GetSpaceId(),
		viewID:     item.GetViewId(),
		version:    targetVersion,
		resultName: resultName,
		view:       proto.Clone(item).(*pb.View),
		datasets:   viewDirtyDatasets(item),
		timeSeries: make(map[string]*pb.TimeSeriesKey),
	}
	s.viewDirtyMu.Lock()
	if s.viewDirtyBuilds == nil {
		s.viewDirtyBuilds = make(map[string]*viewDirtyBuild)
	}
	s.viewDirtyBuilds[handle] = build
	s.viewDirtyMu.Unlock()
	return handle
}

func (s *Service) stopViewDirtyTracking(handle string) {
	if s == nil || handle == "" {
		return
	}
	s.viewDirtyMu.Lock()
	delete(s.viewDirtyBuilds, handle)
	s.viewDirtyMu.Unlock()
}

func (s *Service) drainTimeSeriesDirty(ctx context.Context, handle string) error {
	if s == nil || handle == "" {
		return nil
	}
	viewStore, err := s.viewStore()
	if err != nil {
		return err
	}
	for {
		keys, resultName, item := s.popTimeSeriesDirty(handle)
		if len(keys) == 0 {
			return nil
		}
		rows, err := s.currentTimeSeriesRows(ctx, keys)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			continue
		}
		mapped, ok, err := s.timeSeriesRowsForView(ctx, item, item.GetColumns(), rows)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("view %s/%s does not support incremental projection", item.GetSpaceId(), item.GetViewId())
		}
		if len(mapped) == 0 {
			continue
		}
		if err := viewStore.InsertRows(ctx, resultName, mapped); err != nil {
			return err
		}
	}
}

func (s *Service) popTimeSeriesDirty(handle string) ([]*pb.TimeSeriesKey, string, *pb.View) {
	s.viewDirtyMu.Lock()
	defer s.viewDirtyMu.Unlock()
	build := s.viewDirtyBuilds[handle]
	if build == nil || len(build.timeSeries) == 0 {
		return nil, "", nil
	}
	keys := make([]*pb.TimeSeriesKey, 0, len(build.timeSeries))
	for key, item := range build.timeSeries {
		keys = append(keys, proto.Clone(item).(*pb.TimeSeriesKey))
		delete(build.timeSeries, key)
	}
	item := proto.Clone(build.view).(*pb.View)
	return keys, build.resultName, item
}

func viewDirtyHandle(kind pb.DataKind, spaceID string, viewID string, targetVersion uint64, resultName string) string {
	return fmt.Sprintf("%d|%s|%s|%d|%s", kind, spaceID, viewID, targetVersion, resultName)
}

func viewDirtyDatasets(item *pb.View) map[string]bool {
	out := make(map[string]bool, len(item.GetDatasetIds())+1)
	if datasetID := strings.TrimSpace(item.GetPrimaryDatasetId()); datasetID != "" {
		out[datasetID] = true
	}
	for _, datasetID := range item.GetDatasetIds() {
		if datasetID = strings.TrimSpace(datasetID); datasetID != "" {
			out[datasetID] = true
		}
	}
	return out
}

func timeSeriesDirtyKey(key *pb.TimeSeriesKey) string {
	if key == nil {
		return ""
	}
	return strings.Join([]string{
		key.GetSpaceId(),
		key.GetDatasetId(),
		key.GetSubjectId(),
		key.GetFreq(),
		factkey.DimensionsHash(key.GetDimensions()),
		key.GetDataTime(),
	}, "\x00")
}
