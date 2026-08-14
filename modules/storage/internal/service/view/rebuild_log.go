package view

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/viewindex"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

var rebuildSensitiveValue = regexp.MustCompile(`(?i)(password|passwd|secret|token|authorization|api[_-]?key)=([^\s,;]+)`)
var rebuildJSONSensitiveValue = regexp.MustCompile(`(?i)(["']?(?:password|passwd|secret|token|authorization|api[_-]?key)["']?\s*:\s*["'])([^"']*)(["'])`)
var rebuildAuthHeaderValue = regexp.MustCompile(`(?i)(authorization\s*:\s*(?:basic|bearer)\s+)[^\s,;]+`)

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

func (s *Service) finishRunningRebuildLog(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, buildID string, result pb.ViewRebuildResult, entries uint64, cause error) (found, updated, lookupOK bool) {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || view == nil || strings.TrimSpace(buildID) == "" {
		return false, false, false
	}
	for page := uint32(1); ; page++ {
		rsp, err := client.ListViewRebuildLogs(ctx, &pb.ListViewRebuildLogsReq{
			AuthInfo: auth, SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(),
			Result: pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING,
			Page:   &pb.Page{Page: page, Size: 100},
		})
		if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			if err == nil && rsp != nil {
				err = fmt.Errorf("list rebuild logs: %s", rsp.GetRetInfo().GetMsg())
			}
			log.Printf("storage view rebuild log lookup failed for %s/%s: %v", view.GetSpaceId(), view.GetViewId(), err)
			s.metrics.IncRebuildAuditFailure()
			s.queueRebuildLogRetry(pendingRebuildLog{
				opts: opts, auth: auth, view: proto.Clone(view).(*pb.View), buildID: buildID,
				result: result, entries: entries, cause: cause,
			})
			return found, false, false
		}
		for _, item := range rsp.GetLogs() {
			if item == nil || item.GetBuildId() != buildID || item.GetResult() != pb.ViewRebuildResult_VIEW_REBUILD_RESULT_RUNNING {
				continue
			}
			found = true
			if s.finishRebuildLog(ctx, opts, auth, item, result, entries, cause) {
				updated = true
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetLogs()) == 0 {
			break
		}
	}
	return found, updated, true
}

const maxRebuildLogRetries = 256

func rebuildLogRetryKey(item *pb.ViewRebuildLog) string {
	if item == nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d", item.GetSpaceId(), item.GetViewId(), item.GetBuildId(), item.GetLogId())
}

func rebuildLogLookupRetryKey(view *pb.View, buildID string, result pb.ViewRebuildResult) string {
	if view == nil || strings.TrimSpace(buildID) == "" {
		return ""
	}
	return fmt.Sprintf("lookup\x00%s\x00%s\x00%s\x00%d", view.GetSpaceId(), view.GetViewId(), buildID, result)
}

func (s *Service) queueRebuildLogRetry(entry pendingRebuildLog) {
	key := rebuildLogRetryKey(entry.item)
	if key == "" {
		key = rebuildLogLookupRetryKey(entry.view, entry.buildID, entry.result)
	}
	if key == "" {
		return
	}
	s.rebuildMu.Lock()
	if s.rebuildLogRetry == nil {
		s.rebuildLogRetry = make(map[string]pendingRebuildLog)
	}
	if _, exists := s.rebuildLogRetry[key]; !exists && len(s.rebuildLogRetry) >= maxRebuildLogRetries {
		s.rebuildMu.Unlock()
		s.metrics.IncRebuildAuditDropped()
		log.Printf("storage view rebuild audit retry queue is full; dropping %s", key)
		return
	}
	s.rebuildLogRetry[key] = entry
	pending := len(s.rebuildLogRetry)
	s.rebuildMu.Unlock()
	s.metrics.SetRebuildAuditPending(int64(pending))
}

func (s *Service) attachRebuildLogFallback(view *pb.View, buildID string, result pb.ViewRebuildResult, fallback *pb.ViewRebuildLog) {
	key := rebuildLogLookupRetryKey(view, buildID, result)
	if key == "" || fallback == nil {
		return
	}
	s.rebuildMu.Lock()
	if entry, ok := s.rebuildLogRetry[key]; ok {
		entry.fallback = fallback
		s.rebuildLogRetry[key] = entry
	}
	s.rebuildMu.Unlock()
}

func (s *Service) drainRebuildLogRetries(ctx context.Context) {
	s.rebuildMu.Lock()
	pending := make([]pendingRebuildLog, 0, 4)
	for _, entry := range s.rebuildLogRetry {
		pending = append(pending, entry)
		if len(pending) == cap(pending) {
			break
		}
	}
	s.rebuildMu.Unlock()
	retryCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	for _, entry := range pending {
		key := rebuildLogRetryKey(entry.item)
		if entry.item == nil {
			found, updated, lookupOK := s.finishRunningRebuildLog(retryCtx, entry.opts, entry.auth, entry.view, entry.buildID, entry.result, entry.entries, entry.cause)
			key = rebuildLogLookupRetryKey(entry.view, entry.buildID, entry.result)
			if !lookupOK {
				continue
			}
			if found && !updated {
				// The concrete log update was re-queued by finishRunningRebuildLog;
				// remove this lookup placeholder to avoid duplicate retries.
				s.rebuildMu.Lock()
				delete(s.rebuildLogRetry, key)
				pending := len(s.rebuildLogRetry)
				s.rebuildMu.Unlock()
				s.metrics.SetRebuildAuditPending(int64(pending))
				continue
			}
			if !found && entry.fallback != nil {
				if !s.createRebuildLogFallback(retryCtx, entry.opts, entry.auth, entry.fallback) {
					continue
				}
			}
		} else if !s.finishRebuildLog(retryCtx, entry.opts, entry.auth, entry.item, entry.result, entry.entries, entry.cause) {
			continue
		}
		if key != "" {
			s.rebuildMu.Lock()
			delete(s.rebuildLogRetry, key)
			pending := len(s.rebuildLogRetry)
			s.rebuildMu.Unlock()
			s.metrics.SetRebuildAuditPending(int64(pending))
		}
	}
}

func (s *Service) createRebuildLogFallback(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, item *pb.ViewRebuildLog) bool {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || item == nil {
		return false
	}
	if s.rebuildLogExists(ctx, opts, auth, item) {
		return true
	}
	rsp, err := client.CreateViewRebuildLog(ctx, &pb.CreateViewRebuildLogReq{AuthInfo: auth, Log: item})
	if err == nil && rsp != nil && rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		return true
	}
	if err == nil && rsp != nil {
		err = fmt.Errorf("create rebuild log: %s", rsp.GetRetInfo().GetMsg())
	}
	log.Printf("storage view rebuild fallback log failed for %s/%s: %v", item.GetSpaceId(), item.GetViewId(), err)
	s.metrics.IncRebuildAuditFailure()
	return false
}

func (s *Service) rebuildLogExists(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, item *pb.ViewRebuildLog) bool {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || item == nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	for page := uint32(1); ; page++ {
		rsp, err := client.ListViewRebuildLogs(checkCtx, &pb.ListViewRebuildLogsReq{
			AuthInfo: auth, SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(),
			Page: &pb.Page{Page: page, Size: 1000},
		})
		if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return false
		}
		for _, candidate := range rsp.GetLogs() {
			if candidate != nil && candidate.GetBuildId() == item.GetBuildId() && candidate.GetResult() == item.GetResult() {
				return true
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetLogs()) == 0 {
			return false
		}
	}
}

func rebuildErrorSummary(cause error) string {
	if cause == nil {
		return ""
	}
	message := strings.Join(strings.Fields(cause.Error()), " ")
	message = rebuildSensitiveValue.ReplaceAllString(message, `$1=<redacted>`)
	message = rebuildJSONSensitiveValue.ReplaceAllString(message, `${1}<redacted>${3}`)
	message = rebuildAuthHeaderValue.ReplaceAllString(message, `${1}<redacted>`)
	upper := strings.ToUpper(message)
	for _, keyword := range []string{"SELECT ", "INSERT ", "UPDATE ", "DELETE ", "DROP TABLE ", "CREATE TABLE "} {
		if strings.Contains(upper, keyword) {
			message = "rebuild operation failed (details redacted)"
			break
		}
	}
	const maxErrorSummary = 2048
	if len([]rune(message)) > maxErrorSummary {
		message = string([]rune(message)[:maxErrorSummary])
	}
	return message
}

func interruptedRebuildFailureItem(view *pb.View, build *pb.ViewIndexBuild, cause error) *pb.ViewRebuildLog {
	if view == nil || build == nil {
		return nil
	}
	return &pb.ViewRebuildLog{
		SpaceId: view.GetSpaceId(), ViewId: view.GetViewId(), BuildId: build.GetBuildId(), IndexId: build.GetIndexId(),
		TriggerReason:      pb.ViewRebuildTriggerReason_VIEW_REBUILD_TRIGGER_INTERRUPTED_RETRY,
		Result:             pb.ViewRebuildResult_VIEW_REBUILD_RESULT_FAILED,
		TargetViewRevision: build.GetTargetViewVersion(), ActiveViewRevision: view.GetActiveViewRevision(),
		EntriesWritten: build.GetEntriesWritten(), StartedAt: build.GetStartedAt(), FinishedAt: time.Now().UTC().Format(time.RFC3339Nano),
		ErrorSummary: rebuildErrorSummary(cause), DetailsJson: `{"phase":"interrupted"}`,
	}
}

func (s *Service) recordInterruptedRebuildFailure(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, view *pb.View, build *pb.ViewIndexBuild, cause error) {
	item := interruptedRebuildFailureItem(view, build, cause)
	if item == nil || !s.createRebuildLogFallback(ctx, opts, auth, item) {
		if view != nil {
			log.Printf("storage view interrupted rebuild log failed for %s/%s", view.GetSpaceId(), view.GetViewId())
		}
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
		PhysicalBytes: stats.PhysicalBytes, ErrorSummary: rebuildErrorSummary(cause), DetailsJson: `{"phase":"preflight"}`,
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
		s.metrics.IncRebuildAuditFailure()
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
		s.metrics.IncRebuildAuditFailure()
		return nil
	}
	return rsp.GetLog()
}

func (s *Service) finishRebuildLog(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, item *pb.ViewRebuildLog, result pb.ViewRebuildResult, entries uint64, cause error) bool {
	if item == nil {
		return false
	}
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok {
		return false
	}
	updated := proto.Clone(item).(*pb.ViewRebuildLog)
	updated.Result = result
	updated.EntriesWritten = entries
	updated.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if cause != nil {
		updated.ErrorSummary = rebuildErrorSummary(cause)
	}
	rsp, err := client.UpdateViewRebuildLog(ctx, &pb.UpdateViewRebuildLogReq{AuthInfo: auth, Log: updated})
	if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		if err == nil && rsp != nil {
			err = fmt.Errorf("update rebuild log: %s", rsp.GetRetInfo().GetMsg())
		}
		if s.rebuildLogAlreadyTerminal(ctx, opts, auth, item, result, entries) {
			return true
		}
		log.Printf("storage view rebuild log update failed for %s/%s: %v", item.GetSpaceId(), item.GetViewId(), err)
		s.metrics.IncRebuildAuditFailure()
		s.queueRebuildLogRetry(pendingRebuildLog{opts: opts, auth: auth, item: item, result: result, entries: entries, cause: cause})
		return false
	}
	key := rebuildLogRetryKey(item)
	s.rebuildMu.Lock()
	delete(s.rebuildLogRetry, key)
	pending := len(s.rebuildLogRetry)
	s.rebuildMu.Unlock()
	s.metrics.SetRebuildAuditPending(int64(pending))
	return true
}

func (s *Service) rebuildLogAlreadyTerminal(ctx context.Context, opts ReconcilerOptions, auth *pb.AuthInfo, item *pb.ViewRebuildLog, result pb.ViewRebuildResult, entries uint64) bool {
	client, ok := opts.Metadata.(rebuildLogMetadataClient)
	if !ok || item == nil || item.GetLogId() == 0 {
		return false
	}
	readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	for page := uint32(1); ; page++ {
		rsp, err := client.ListViewRebuildLogs(readCtx, &pb.ListViewRebuildLogsReq{
			AuthInfo: auth, SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(),
			Result: result, Page: &pb.Page{Page: page, Size: 1000},
		})
		if err != nil || rsp == nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
			return false
		}
		for _, candidate := range rsp.GetLogs() {
			if candidate != nil && candidate.GetLogId() == item.GetLogId() && candidate.GetResult() == result && candidate.GetEntriesWritten() == entries {
				return true
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetLogs()) == 0 {
			return false
		}
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
		s.metrics.IncRebuildAuditFailure()
	}
}
