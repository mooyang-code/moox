package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

func rebuildTriggerReason(view *pb.View, stats viewindex.ViewIndexStats, sizeLimitOnly bool) pb.ViewRebuildTriggerReason {
	if view == nil || view.GetActiveIndexId() == "" {
		return pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_INITIAL_BUILD
	}
	if !stats.Exists {
		return pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_ACTIVE_MISSING
	}
	if view.GetDesiredViewRevision() > view.GetActiveViewRevision() {
		return pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_DEFINITION_CHANGE
	}
	if sizeLimitOnly {
		return pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_SIZE_LIMIT
	}
	return pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_COVERAGE_REPAIR
}

func (s *Service) finishRunningRebuildLog(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID string, result pb.ViewRebuildResult, entries uint64, cause error) {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || view == nil || strings.TrimSpace(buildID) == "" {
		return
	}
	rsp, err := client.ListViewRebuildLogs(ctx, &pb.ListViewRebuildLogsReq{
		AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(),
		Result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING,
		Page:   &pb.Page{Page: 1, Size: 100},
	})
	if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		if err == nil && rsp != nil {
			err = fmt.Errorf("list rebuild logs: %s", rsp.GetRetInfo().GetMsg())
		}
		log.Printf("storage view rebuild log lookup failed for %s/%s: %v", view.GetSpaceId(), view.GetViewId(), err)
		return
	}
	for _, item := range rsp.GetLogs() {
		if item == nil || item.GetBuildId() != buildID || item.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING {
			continue
		}
		s.finishRebuildLog(ctx, opts, auth, item, result, entries, cause)
	}
}

func (s *Service) recordFailedRebuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, reason pb.ViewRebuildTriggerReason, cause error, stats viewindex.ViewIndexStats) {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || view == nil {
		return
	}
	item := &pb.ViewRebuildLog{
		SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), TriggerReason: reason,
		Result:             pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED,
		TargetViewRevision: view.GetDesiredViewRevision(), ActiveViewRevision: view.GetActiveViewRevision(),
		PhysicalBytes: stats.PhysicalBytes, ErrorSummary: cause.Error(), DetailsJson: `{"phase":"preflight"}`,
	}
	rsp, err := client.CreateViewRebuildLog(ctx, &pb.CreateViewRebuildLogReq{AuthInfo: auth, Log: item})
	if err == nil && (rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS) {
		if rsp == nil {
			err = errors.New("empty ret_info")
		} else {
			err = fmt.Errorf("create rebuild log: %s", rsp.GetRetInfo().GetMsg())
		}
	}
	if err != nil {
		log.Printf("storage view rebuild preflight log failed for %s/%s: %v", view.GetSpaceId(), view.GetViewId(), err)
	}
}

func (s *Service) createRunningRebuildLog(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID, indexID string, reason pb.ViewRebuildTriggerReason, stats viewindex.ViewIndexStats, physicalBytes, pending, ackPending uint64) *pb.ViewRebuildLog {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok {
		return nil
	}
	started := time.Now().UTC().Format(time.RFC3339Nano)
	item := &pb.ViewRebuildLog{
		SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: buildID, IndexId: indexID,
		TriggerReason: reason, Result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING,
		TargetViewRevision: view.GetDesiredViewRevision(), ActiveViewRevision: view.GetActiveViewRevision(),
		PhysicalBytes: physicalBytes, NumPending: pending, NumAckPending: ackPending, StartedAt: started, DetailsJson: `{"phase":"reconcile"}`,
	}
	rsp, err := client.CreateViewRebuildLog(ctx, &pb.CreateViewRebuildLogReq{AuthInfo: auth, Log: item})
	if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		if err == nil && rsp != nil {
			err = fmt.Errorf("create rebuild log: %s", rsp.GetRetInfo().GetMsg())
		}
		log.Printf("storage view rebuild log create failed for %s/%s: %v", view.GetSpaceId(), view.GetViewId(), err)
		return nil
	}
	return rsp.GetLog()
}

func (s *Service) finishRebuildLog(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, item *pb.ViewRebuildLog, result pb.ViewRebuildResult, entries uint64, cause error) {
	if item == nil {
		return
	}
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok {
		return
	}
	updated := proto.Clone(item).(*pb.ViewRebuildLog)
	updated.Result = result
	updated.EntriesWritten = entries
	updated.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if cause != nil {
		updated.ErrorSummary = cause.Error()
	}
	rsp, err := client.UpdateViewRebuildLog(ctx, &pb.UpdateViewRebuildLogReq{AuthInfo: auth, Log: updated})
	if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		if err == nil && rsp != nil {
			err = fmt.Errorf("update rebuild log: %s", rsp.GetRetInfo().GetMsg())
		}
		log.Printf("storage view rebuild log update failed for %s/%s: %v", item.GetSpaceId(), item.GetViewId(), err)
	}
}

func (s *Service) recordSkippedRebuild(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, reason pb.ViewRebuildTriggerReason, blockReason string, stats viewindex.ViewIndexStats, physicalBytes, pending, ackPending uint64) {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || view == nil {
		return
	}
	item := &pb.ViewRebuildLog{
		SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), TriggerReason: reason,
		Result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_SKIPPED, BlockReason: blockReason,
		TargetViewRevision: view.GetDesiredViewRevision(), ActiveViewRevision: view.GetActiveViewRevision(),
		PhysicalBytes: physicalBytes, NumPending: pending, NumAckPending: ackPending,
		DetailsJson: `{"phase":"gate"}`,
	}
	rsp, err := client.UpsertSkippedViewRebuildLog(ctx, &pb.UpsertSkippedViewRebuildLogReq{AuthInfo: auth, Log: item})
	if err == nil && (rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS) {
		if rsp == nil {
			err = errors.New("empty ret_info")
		} else {
			err = fmt.Errorf("upsert skipped rebuild log: %s", rsp.GetRetInfo().GetMsg())
		}
	}
	if err != nil {
		log.Printf("storage view skipped rebuild log failed for %s/%s: %v", view.GetSpaceId(), view.GetViewId(), err)
	}
}
