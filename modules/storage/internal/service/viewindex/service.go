//go:build legacy_viewindex

package viewindex

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/retinfo"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Service struct {
	engines map[string]ManagedEngine
}

func NewService(opts Options) *Service {
	out := &Service{engines: map[string]ManagedEngine{}}
	for n, e := range opts.Engines {
		if e != nil {
			out.engines[strings.ToLower(n)] = e
		}
	}
	return out
}

type Options struct{ Engines map[string]ManagedEngine }

var _ pb.ViewIndexService = (*Service)(nil)

func (s *Service) engine(name string) (ManagedEngine, error) {
	e := s.engines[strings.ToLower(strings.TrimSpace(name))]
	if e == nil {
		return nil, errors.New("unsupported view index engine")
	}
	return e, nil
}
func (s *Service) PrepareViewIndex(ctx context.Context, req *pb.PrepareViewIndexReq) (*pb.PrepareViewIndexRsp, error) {
	e, err := s.engine(req.GetEngine())
	if err == nil {
		sc := req.GetSchema()
		err = e.Prepare(ctx, req.GetIndexId(), ViewIndexSchema{SpaceID: sc.GetSpaceId(), ViewID: sc.GetViewId(), ViewVersion: sc.GetViewVersion(), Engine: sc.GetEngine(), Columns: sc.GetColumns(), SchemaHash: sc.GetViewSchemaHash()})
	}
	return &pb.PrepareViewIndexRsp{RetInfo: status(err)}, nil
}
func (s *Service) ApplyViewIndex(ctx context.Context, req *pb.ApplyViewIndexReq) (*pb.ApplyViewIndexRsp, error) {
	e, err := s.engine(req.GetEngine())
	if err == nil {
		err = e.Apply(ctx, req.GetIndexId(), batchFromProto(req.GetBatch()))
	}
	return &pb.ApplyViewIndexRsp{RetInfo: status(err)}, nil
}
func (s *Service) StatViewIndex(ctx context.Context, req *pb.StatViewIndexReq) (*pb.StatViewIndexRsp, error) {
	e, err := s.engine(req.GetEngine())
	var st ViewIndexStats
	if err == nil {
		st, err = e.Stat(ctx, req.GetIndexId())
	}
	return &pb.StatViewIndexRsp{RetInfo: status(err), Stats: &pb.ViewIndexStats{Exists: st.Exists, ViewVersion: st.ViewVersion, EntryCount: uint64(st.EntryCount), MinVersion: st.MinVersion, MaxVersion: st.MaxVersion, ViewSchemaHash: st.SchemaHash, PhysicalBytes: st.PhysicalBytes, UpdatedAt: st.UpdatedAt, FreeDiskBytes: st.FreeDiskBytes, IndexedFrom: st.IndexedFrom, IndexedTo: st.IndexedTo}}, nil
}
func (s *Service) RemoveViewIndex(ctx context.Context, req *pb.RemoveViewIndexReq) (*pb.RemoveViewIndexRsp, error) {
	e, err := s.engine(req.GetEngine())
	if err == nil {
		err = e.Remove(ctx, req.GetIndexId())
	}
	return &pb.RemoveViewIndexRsp{RetInfo: status(err)}, nil
}
func (s *Service) ListViewIndexes(ctx context.Context, req *pb.ListViewIndexesReq) (*pb.ListViewIndexesRsp, error) {
	e, err := s.engine(req.GetEngine())
	if err != nil {
		return &pb.ListViewIndexesRsp{RetInfo: status(err)}, nil
	}
	ids, err := e.List(ctx)
	out := make([]*pb.ViewIndexDescriptor, 0, len(ids))
	for _, id := range ids {
		if req.GetSpaceId() != "" {
			r, _ := ParseViewIndexID(id)
			if r.SpaceID != req.GetSpaceId() {
				continue
			}
		}
		out = append(out, &pb.ViewIndexDescriptor{IndexId: id, Engine: e.Engine()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GetIndexId() < out[j].GetIndexId() })
	return &pb.ListViewIndexesRsp{RetInfo: status(err), Indexes: out}, nil
}
func (s *Service) QueryTimeSeriesIndex(ctx context.Context, req *pb.QueryTimeSeriesIndexReq) (*pb.QueryTimeSeriesIndexRsp, error) {
	e, err := s.engine(req.GetEngine())
	var rows []*pb.RowFieldValues
	if err == nil {
		q, ok := e.(interface {
			Query(context.Context, string, []*pb.RowKey, []string) ([]*pb.RowFieldValues, error)
		})
		if !ok {
			err = errors.New("time series query unavailable")
		} else {
			rows, err = q.Query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
		}
	}
	return &pb.QueryTimeSeriesIndexRsp{RetInfo: status(err), Rows: rows}, nil
}
func (s *Service) SearchRecordIndex(ctx context.Context, req *pb.SearchRecordIndexReq) (*pb.SearchRecordIndexRsp, error) {
	e, err := s.engine(req.GetEngine())
	var rows []*pb.RowFieldValues
	if err == nil {
		q, ok := e.(interface {
			Query(context.Context, string, []*pb.RowKey, []string) ([]*pb.RowFieldValues, error)
		})
		if !ok {
			err = errors.New("record query unavailable")
		} else {
			rows, err = q.Query(ctx, req.GetIndexId(), req.GetKeys(), req.GetFieldIds())
		}
	}
	return &pb.SearchRecordIndexRsp{RetInfo: status(err), Rows: rows}, nil
}
func status(err error) *pb.RetInfo {
	if err == nil {
		return retinfo.Success("success")
	}
	return retinfo.Error(pb.ErrorCode_INNER_ERR, err)
}
func batchFromProto(in *pb.ViewIndexApplyBatch) ViewIndexApplyBatch {
	out := ViewIndexApplyBatch{}
	if in == nil {
		return out
	}
	out.ViewRevision = in.GetViewRevision()
	out.ViewSchemaHash = in.GetViewSchemaHash()
	if strings.EqualFold(in.GetWriteMode(), "BACKFILL") {
		out.WriteMode = Backfill
	} else if strings.EqualFold(in.GetWriteMode(), "REPLACE") {
		out.WriteMode = Replace
	} else {
		out.WriteMode = LiveWrite
	}
	for _, w := range in.GetRowWrites() {
		if w == nil || w.GetKey() == nil {
			continue
		}
		out.RowWrites = append(out.RowWrites, RowWrite{Key: RowKey{Key: w.GetKey().GetRowKey()}, Fields: w.GetFields(), Attributes: w.GetAttributes()})
	}
	return out
}
