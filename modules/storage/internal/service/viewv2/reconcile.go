package viewv2

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"trpc.group/trpc-go/trpc-go/client"
)

type MetadataClient interface {
	ListViews(context.Context, *pb.ListViewsReq, ...client.Option) (*pb.ListViewsRsp, error)
	ListDatasetColumns(context.Context, *pb.ListDatasetColumnsReq, ...client.Option) (*pb.ListDatasetColumnsRsp, error)
	ClaimViewIndexBuild(context.Context, *pb.ClaimViewIndexBuildReq, ...client.Option) (*pb.ClaimViewIndexBuildRsp, error)
	UpdateViewIndexBuild(context.Context, *pb.UpdateViewIndexBuildReq, ...client.Option) (*pb.UpdateViewIndexBuildRsp, error)
	ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq, ...client.Option) (*pb.ActivateViewIndexRsp, error)
	FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq, ...client.Option) (*pb.FailViewIndexBuildRsp, error)
}

type FieldReader interface {
	ReadFields(context.Context, *pb.PrimaryReadFieldsReq, ...client.Option) (*pb.PrimaryReadFieldsRsp, error)
}

type ReconcilerOptions struct {
	Metadata MetadataClient
	Primary  FieldReader
	Interval time.Duration
	OwnerID  string
	Grace    time.Duration
}

func (s *Service) StartReconciler(ctx context.Context, opts ReconcilerOptions) (func(), error) {
	if opts.Metadata == nil {
		return nil, errors.New("metadata client is required")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if strings.TrimSpace(opts.OwnerID) == "" {
		opts.OwnerID = "storage-view"
	}
	if err := s.reconcileOnce(ctx, opts); err != nil {
		return nil, err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(opts.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-loopCtx.Done():
				return
			case <-ticker.C:
				_ = s.reconcileOnce(loopCtx, opts)
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}

func (s *Service) reconcileOnce(ctx context.Context, opts ReconcilerOptions) error {
	auth := s.internalAuth()
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := opts.Metadata.ListViews(ctx, &pb.ListViewsReq{AuthInfo: auth, Status: "active", Page: &pb.Page{Page: pageNo, Size: 100}})
		if err != nil {
			return err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return err
		}
		for _, view := range rsp.GetViews() {
			if err := s.reconcileView(ctx, opts, auth, view); err != nil {
				continue
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			return nil
		}
	}
}

func (s *Service) reconcileView(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View) error {
	if view == nil || view.GetSpaceId() == "" || view.GetViewId() == "" {
		return nil
	}
	var stats viewindex.ViewIndexStats
	if view.GetActiveIndexId() != "" {
		if err := s.AttachActiveView(view); err != nil {
			return err
		}
		engine, err := s.engineFor(view.GetActiveIndexId())
		if err != nil {
			return err
		}
		stats, err = engine.Stat(ctx, view.GetActiveIndexId())
		if err != nil {
			return err
		}
	}
	if build := view.GetIndexBuild(); build != nil {
		switch build.GetState() {
		case pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_CATCHING_UP, pb.ViewIndexBuild_FAILED:
			return nil
		}
	}
	if !needsRebuild(view, stats) {
		return nil
	}
	columns := view.GetColumns()
	if len(columns) == 0 {
		var err error
		columns, err = loadDefaultViewColumns(ctx, opts.Metadata, auth, view)
		if err != nil {
			return err
		}
	}
	schema := viewindex.ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), ViewVersion: view.GetDesiredViewRevision(),
		PrimaryDatasetID: view.GetPrimaryDatasetId(), Engine: strings.ToLower(view.GetEngine()), Columns: columns,
	}
	schema.SchemaHash = viewindex.HashViewIndexSchema(schema)
	indexID := viewindex.InactiveViewIndexID(view.GetSpaceId(), view.GetViewId(), view.GetActiveIndexId())
	buildID := "build-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	claim, err := opts.Metadata.ClaimViewIndexBuild(ctx, &pb.ClaimViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID,
		IndexId: indexID, Engine: schema.Engine, TargetViewVersion: schema.ViewVersion,
		OwnerId: opts.OwnerID, SchemaHash: schema.SchemaHash, Columns: columns,
		ExpectedActiveIndexId: view.GetActiveIndexId(),
	})
	if err != nil {
		return err
	}
	if err := requireSuccess(claim.GetRetInfo()); err != nil {
		return err
	}
	if view.GetActiveIndexId() != "" {
		if err := s.TrackViewBuild(view.GetSpaceId(), view.GetViewId(), buildID, opts.OwnerID, opts.Metadata, auth); err != nil {
			return err
		}
	}
	prepared, err := s.PrepareViewIndex(ctx, &pb.PrepareViewIndexReq{
		AuthInfo: auth, IndexId: indexID, Engine: schema.Engine,
		Schema: &pb.ViewIndexSchema{
			SpaceId: schema.SpaceID, ViewId: schema.ViewID, ViewVersion: schema.ViewVersion,
			Engine: schema.Engine, Columns: schema.Columns, ViewSchemaHash: schema.SchemaHash, PrimaryDatasetId: schema.PrimaryDatasetID,
		},
	})
	if err != nil || prepared.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		prepareErr := err
		if prepareErr == nil {
			prepareErr = requireSuccess(prepared.GetRetInfo())
		}
		s.failBuild(ctx, opts, auth, view, buildID, fmt.Errorf("prepare view index: %w", prepareErr))
		return prepareErr
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_PREPARING, pb.ViewIndexBuild_BUILDING, 0); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, err)
		return err
	}
	if view.GetActiveIndexId() != "" {
		if err := s.BackfillViewWithReader(ctx, view.GetSpaceId(), view.GetViewId(), 100, opts.Primary); err != nil {
			s.failBuild(ctx, opts, auth, view, buildID, err)
			return err
		}
	} else if err := s.MarkViewBuildReady(view.GetSpaceId(), view.GetViewId()); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, err)
		return err
	}
	if err := s.updateBuild(ctx, opts, auth, view, buildID, pb.ViewIndexBuild_BUILDING, pb.ViewIndexBuild_READY, uint64(stats.EntryCount)); err != nil {
		s.failBuild(ctx, opts, auth, view, buildID, err)
		return err
	}
	activated, err := opts.Metadata.ActivateViewIndex(ctx, &pb.ActivateViewIndexReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, OwnerId: opts.OwnerID,
	})
	if err != nil {
		return err
	}
	if err := requireSuccess(activated.GetRetInfo()); err != nil {
		return err
	}
	if view.GetActiveIndexId() != "" {
		return s.SwitchView(ctx, view.GetSpaceId(), view.GetViewId(), opts.Grace)
	}
	return nil
}

func loadDefaultViewColumns(ctx context.Context, metadata MetadataClient, auth *pb.AuthInfo, view *pb.View) ([]*pb.ViewColumn, error) {
	var columns []*pb.ViewColumn
	for pageNo := uint32(1); ; pageNo++ {
		rsp, err := metadata.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{
			AuthInfo: auth, SpaceId: view.GetSpaceId(), DatasetId: view.GetPrimaryDatasetId(),
			Page: &pb.Page{Page: pageNo, Size: 1000},
		})
		if err != nil {
			return nil, err
		}
		if err := requireSuccess(rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, column := range rsp.GetColumns() {
			if column == nil || (column.GetStatus() != "" && column.GetStatus() != "active") {
				continue
			}
			columns = append(columns, &pb.ViewColumn{
				SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), ColumnName: column.GetColumnName(),
				OriginType: pb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN,
				OriginId:   view.GetPrimaryDatasetId() + "." + column.GetColumnName(),
				ValueType:  column.GetValueType(),
			})
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetColumns()) == 0 {
			return columns, nil
		}
	}
}

func (s *Service) updateBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID string, from, to pb.ViewIndexBuild_State, rows uint64) error {
	rsp, err := opts.Metadata.UpdateViewIndexBuild(ctx, &pb.UpdateViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID,
		OwnerId: opts.OwnerID, ExpectedState: from, NextState: to, EntriesWritten: rows,
	})
	if err != nil {
		return err
	}
	return requireSuccess(rsp.GetRetInfo())
}

func (s *Service) failBuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID string, cause error) {
	message := "view build failed"
	if cause != nil {
		message = cause.Error()
	}
	_, _ = opts.Metadata.FailViewIndexBuild(ctx, &pb.FailViewIndexBuildReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(),
		BuildId: buildID, OwnerId: opts.OwnerID, Error: message,
	})
}

func needsRebuild(view *pb.View, stats viewindex.ViewIndexStats) bool {
	if view == nil {
		return false
	}
	if view.GetActiveIndexId() == "" || view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		return true
	}
	keep, err := time.ParseDuration(view.GetKeepDuration())
	if err != nil || keep <= 0 || stats.IndexedFrom == "" || stats.IndexedTo == "" {
		return false
	}
	from, err := time.Parse(time.RFC3339Nano, stats.IndexedFrom)
	if err != nil {
		return false
	}
	to, err := time.Parse(time.RFC3339Nano, stats.IndexedTo)
	return err == nil && to.Sub(from) > 2*keep
}

func (s *Service) internalAuth() *pb.AuthInfo {
	const appID = "storage-view"
	return &pb.AuthInfo{AppId: appID, AppKey: datanode.ServiceAuthKey(s.authSecret, appID)}
}

func requireSuccess(ret *pb.RetInfo) error {
	if ret == nil {
		return errors.New("empty ret_info")
	}
	if ret.GetCode() != pb.ErrorCode_SUCCESS {
		return errors.New(ret.GetMsg())
	}
	return nil
}
