package jobstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/types/known/structpb"
)

type Clock interface {
	Now() time.Time
}

type Options struct {
	Clock              Clock
	RecoverAfterMillis int64
	DefaultMaxAttempts int
	MaxCASRetries      int
}

type Store interface {
	CreatePending(ctx context.Context, item *pb.JobItem, meta QueueMeta) (*CreateResult, error)
	MarkPublished(ctx context.Context, spaceID, jobItemID string, meta QueueMeta) error
	MarkEnqueueFailed(ctx context.Context, spaceID, jobItemID string, message string) error
	Get(ctx context.Context, spaceID, jobItemID string) (*State, error)
	TryMarkRunning(ctx context.Context, req RunningRequest) (bool, RunningState, error)
	MarkCanceled(ctx context.Context, spaceID, jobItemID, reason string) error
	ClearCancelDirective(ctx context.Context, spaceID, jobItemID string, attemptNo int32) error
	MarkReported(ctx context.Context, event ReportEvent) (*State, error)
	MarkHistorySynced(ctx context.Context, spaceID, jobItemID string) error
	List(ctx context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error)
	ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error)
	ListCancelDirectives(ctx context.Context, spaceID, nodeID string, limit int) ([]*pb.ControlDirective, error)
}

type KVStore struct {
	kv                 nats.KeyValue
	clock              Clock
	recoverAfterMillis int64
	defaultMaxAttempts int
	maxCASRetries      int
}

func NewKVStore(kv nats.KeyValue, opts Options) *KVStore {
	maxAttempts := opts.DefaultMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	recoverAfter := opts.RecoverAfterMillis
	if recoverAfter <= 0 {
		recoverAfter = int64(10 * time.Minute / time.Millisecond)
	}
	retries := opts.MaxCASRetries
	if retries <= 0 {
		retries = 5
	}
	return &KVStore{
		kv:                 kv,
		clock:              opts.Clock,
		recoverAfterMillis: recoverAfter,
		defaultMaxAttempts: maxAttempts,
		maxCASRetries:      retries,
	}
}

func (s *KVStore) CreatePending(ctx context.Context, item *pb.JobItem, meta QueueMeta) (*CreateResult, error) {
	if s == nil || s.kv == nil {
		return nil, ErrInvalid
	}
	if err := validateJobItem(item); err != nil {
		return nil, err
	}
	now := s.now()
	state := State{
		SchemaVersion: 1,
		SpaceID:       strings.TrimSpace(item.GetSpaceId()),
		JobID:         strings.TrimSpace(item.GetJobId()),
		JobItemID:     strings.TrimSpace(item.GetJobItemId()),
		JobType:       strings.TrimSpace(item.GetJobType()),
		CodePackageID: strings.TrimSpace(item.GetCodePackageId()),
		Params:        structToMap(item.GetParams()),
		Priority:      item.GetPriority(),
		Status:        StatusPending,
		Queue:         meta,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	raw, err := encodeState(state)
	if err != nil {
		return nil, err
	}
	if _, err := s.kv.Create(JobKey(state.SpaceID, state.JobItemID), raw); err != nil {
		if errors.Is(err, nats.ErrKeyExists) {
			if updated, changed, err := s.reopenEnqueueFailed(ctx, item, meta); err != nil {
				return nil, err
			} else if changed {
				return &CreateResult{
					JobItemID: updated.JobItemID,
					Status:    pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
					Created:   true,
				}, nil
			}
			return &CreateResult{
				JobItemID:    state.JobItemID,
				Status:       pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED,
				Deduplicated: true,
			}, nil
		}
		return nil, mapKVError(err)
	}
	return &CreateResult{
		JobItemID: state.JobItemID,
		Status:    pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED,
		Created:   true,
	}, nil
}

func (s *KVStore) reopenEnqueueFailed(ctx context.Context, item *pb.JobItem, meta QueueMeta) (State, bool, error) {
	return s.withStateCAS(ctx, JobKey(item.GetSpaceId(), item.GetJobItemId()), func(state State) (State, bool, error) {
		if state.Status != StatusEnqueueFailed {
			return state, false, nil
		}
		state.JobID = strings.TrimSpace(item.GetJobId())
		state.JobType = strings.TrimSpace(item.GetJobType())
		state.CodePackageID = strings.TrimSpace(item.GetCodePackageId())
		state.Params = structToMap(item.GetParams())
		state.Priority = item.GetPriority()
		state.Status = StatusPending
		state.Queue = meta
		state.LastErrorKind = ""
		state.LastErrorCode = ""
		state.LastErrorMessage = ""
		return state, true, nil
	})
}

func (s *KVStore) MarkPublished(ctx context.Context, spaceID, jobItemID string, meta QueueMeta) error {
	_, _, err := s.withStateCAS(ctx, JobKey(spaceID, jobItemID), func(state State) (State, bool, error) {
		state.Queue.Subject = firstNonEmpty(meta.Subject, state.Queue.Subject)
		state.Queue.Stream = firstNonEmpty(meta.Stream, state.Queue.Stream)
		if meta.StreamSeq > 0 {
			state.Queue.StreamSeq = meta.StreamSeq
		}
		if meta.AckSubject != "" {
			state.Queue.AckSubject = meta.AckSubject
		}
		if state.Status == StatusEnqueueFailed {
			state.Status = StatusPending
		}
		return state, true, nil
	})
	return err
}

func (s *KVStore) MarkEnqueueFailed(ctx context.Context, spaceID, jobItemID string, message string) error {
	_, _, err := s.withStateCAS(ctx, JobKey(spaceID, jobItemID), func(state State) (State, bool, error) {
		if state.IsTerminal() {
			return state, false, nil
		}
		state.Status = StatusEnqueueFailed
		state.LastErrorKind = ErrorRetryable
		state.LastErrorMessage = message
		return state, true, nil
	})
	return err
}

func (s *KVStore) Get(_ context.Context, spaceID, jobItemID string) (*State, error) {
	if s == nil || s.kv == nil {
		return nil, ErrInvalid
	}
	entry, err := s.kv.Get(JobKey(spaceID, jobItemID))
	if err != nil {
		return nil, mapKVError(err)
	}
	state, err := decodeState(entry.Value())
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *KVStore) TryMarkRunning(ctx context.Context, req RunningRequest) (bool, RunningState, error) {
	var running RunningState
	now := s.now()
	updated, changed, err := s.withStateCAS(ctx, JobKey(req.SpaceID, req.JobItemID), func(state State) (State, bool, error) {
		if state.IsTerminal() {
			return state, false, nil
		}
		if state.Status == StatusRunning && state.RecoverAt != nil && state.RecoverAt.After(now) {
			return state, false, nil
		}
		if state.Status == StatusRunning {
			state.markRunningAttemptLost(now)
		}
		if state.AttemptNo >= s.defaultMaxAttempts {
			state.Status = StatusFailed
			state.LastErrorKind = ErrorPermanent
			state.LastErrorCode = "MAX_ATTEMPTS_EXCEEDED"
			state.LastErrorMessage = "max attempts exceeded"
			state.FinishedAt = &now
			return state, true, nil
		}
		state.AttemptNo++
		state.Status = StatusRunning
		state.RunningNode = strings.TrimSpace(req.NodeID)
		if state.StartedAt == nil {
			state.StartedAt = &now
		}
		recoverAt := now.Add(time.Duration(s.recoverAfterMillis) * time.Millisecond)
		state.RecoverAt = &recoverAt
		if req.AckSubject != "" {
			state.Queue.AckSubject = req.AckSubject
		}
		if req.StreamSeq > 0 {
			state.Queue.StreamSeq = req.StreamSeq
		}
		state.Attempts = append(state.Attempts, Attempt{
			AttemptNo: state.AttemptNo,
			NodeID:    state.RunningNode,
			Status:    AttemptRunning,
			StartedAt: now,
		})
		return state, true, nil
	})
	if err != nil || !changed || updated.Status != StatusRunning {
		return false, running, err
	}
	running.AttemptNo = updated.AttemptNo
	running.AckSubject = updated.Queue.AckSubject
	if updated.RecoverAt != nil {
		running.RecoverAt = *updated.RecoverAt
	}
	return true, running, nil
}

func (s *KVStore) MarkCanceled(ctx context.Context, spaceID, jobItemID, reason string) error {
	_, _, err := s.withStateCAS(ctx, JobKey(spaceID, jobItemID), func(state State) (State, bool, error) {
		if state.Status == StatusSuccess || state.Status == StatusFailed {
			return state, false, nil
		}
		state.Status = StatusCanceled
		state.CancelReason = strings.TrimSpace(reason)
		if state.CancelReason == "" {
			state.CancelReason = "canceled"
		}
		if state.AttemptNo == 0 {
			now := s.now()
			state.FinishedAt = &now
		}
		return state, true, nil
	})
	return err
}

func (s *KVStore) ClearCancelDirective(ctx context.Context, spaceID, jobItemID string, attemptNo int32) error {
	_, _, err := s.withStateCAS(ctx, JobKey(spaceID, jobItemID), func(state State) (State, bool, error) {
		if state.AttemptNo != int(attemptNo) || state.CancelReason == "" {
			return state, false, nil
		}
		state.CancelReason = ""
		return state, true, nil
	})
	return err
}

func (s *KVStore) MarkReported(ctx context.Context, event ReportEvent) (*State, error) {
	now := event.Time
	if now.IsZero() {
		now = s.now()
	}
	updated, _, err := s.withStateCAS(ctx, JobKey(event.SpaceID, event.JobItemID), func(state State) (State, bool, error) {
		if state.Status != StatusRunning && !(state.Status == StatusCanceled && event.Status == StatusCanceled) {
			return state, false, ErrInactive
		}
		if state.RunningNode != strings.TrimSpace(event.NodeID) || state.AttemptNo != int(event.AttemptNo) {
			return state, false, ErrStaleAttempt
		}
		attempt := state.findAttempt(int(event.AttemptNo))
		if attempt == nil || attempt.Status != AttemptRunning {
			return state, false, ErrStaleAttempt
		}
		attempt.ErrorKind = event.ErrorKind
		attempt.ErrorCode = event.ErrorCode
		attempt.ErrorMessage = event.ErrorMessage
		attempt.ResultSummary = event.ResultSummary
		attempt.FinishedAt = &now
		state.LastErrorKind = event.ErrorKind
		state.LastErrorCode = event.ErrorCode
		state.LastErrorMessage = event.ErrorMessage
		switch event.Status {
		case StatusSuccess:
			attempt.Status = AttemptSuccess
			state.Status = StatusSuccess
			state.ResultSummary = event.ResultSummary
			state.FinishedAt = &now
			state.clearRunning()
		case StatusCanceled:
			attempt.Status = AttemptCanceled
			state.Status = StatusCanceled
			if state.CancelReason == "" {
				state.CancelReason = firstNonEmpty(event.ErrorMessage, "canceled")
			}
			state.FinishedAt = &now
			state.clearRunning()
		case StatusFailed:
			attempt.Status = AttemptFailed
			if event.ErrorKind == ErrorRetryable && state.AttemptNo < s.defaultMaxAttempts {
				state.Status = StatusPending
				state.clearRunning()
			} else {
				state.Status = StatusFailed
				if state.LastErrorKind == "" {
					state.LastErrorKind = ErrorPermanent
				}
				state.FinishedAt = &now
				state.clearRunning()
			}
		default:
			return state, false, ErrInvalid
		}
		return state, true, nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (s *KVStore) MarkHistorySynced(ctx context.Context, spaceID, jobItemID string) error {
	_, _, err := s.withStateCAS(ctx, JobKey(spaceID, jobItemID), func(state State) (State, bool, error) {
		if state.HistorySynced {
			return state, false, nil
		}
		state.HistorySynced = true
		return state, true, nil
	})
	return err
}

func (s *KVStore) List(_ context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error) {
	if s == nil || s.kv == nil {
		return nil, nil, ErrInvalid
	}
	keys, err := s.kv.Keys()
	if err != nil {
		if errors.Is(err, nats.ErrNoKeysFound) {
			return nil, pageResult(req.GetPage(), 0), nil
		}
		return nil, nil, mapKVError(err)
	}
	prefix := SpacePrefix(req.GetSpaceId())
	states := make([]State, 0, len(keys))
	for _, key := range keys {
		if req.GetSpaceId() != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		entry, err := s.kv.Get(key)
		if err != nil {
			continue
		}
		state, err := decodeState(entry.Value())
		if err != nil || !matchesListFilter(state, req) {
			continue
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool {
		return states[i].UpdatedAt.After(states[j].UpdatedAt)
	})
	page, size := normalizePage(req.GetPage())
	start := int((page - 1) * size)
	if start > len(states) {
		start = len(states)
	}
	end := start + int(size)
	if end > len(states) {
		end = len(states)
	}
	out := make([]*pb.JobItemDetail, 0, end-start)
	for _, state := range states[start:end] {
		out = append(out, state.ToDetail())
	}
	return out, &commonpb.PageResult{
		Page:    page,
		Size:    size,
		Total:   uint32(len(states)),
		HasMore: end < len(states),
	}, nil
}

func (s *KVStore) ListAttempts(ctx context.Context, req *pb.ListJobItemAttemptsReq) ([]*pb.JobItemAttempt, error) {
	state, err := s.Get(ctx, req.GetSpaceId(), req.GetJobItemId())
	if err != nil {
		return nil, err
	}
	out := make([]*pb.JobItemAttempt, 0, len(state.Attempts))
	for _, attempt := range state.Attempts {
		out = append(out, attempt.ToProto())
	}
	return out, nil
}

func (s *KVStore) ListCancelDirectives(_ context.Context, spaceID, nodeID string, limit int) ([]*pb.ControlDirective, error) {
	if s == nil || s.kv == nil {
		return nil, ErrInvalid
	}
	if limit <= 0 {
		limit = 20
	}
	keys, err := s.kv.Keys()
	if err != nil {
		if errors.Is(err, nats.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, mapKVError(err)
	}
	prefix := SpacePrefix(spaceID)
	out := make([]*pb.ControlDirective, 0)
	for _, key := range keys {
		if spaceID != "" && !strings.HasPrefix(key, prefix) {
			continue
		}
		entry, err := s.kv.Get(key)
		if err != nil {
			continue
		}
		state, err := decodeState(entry.Value())
		if err != nil {
			continue
		}
		if state.Status != StatusCanceled || state.RunningNode != nodeID || state.AttemptNo <= 0 || state.CancelReason == "" {
			continue
		}
		out = append(out, &pb.ControlDirective{
			Type:      pb.ControlDirectiveType_CONTROL_DIRECTIVE_CANCEL,
			JobItemId: state.JobItemID,
			AttemptNo: int32(state.AttemptNo),
			Reason:    state.CancelReason,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *KVStore) withStateCAS(_ context.Context, key string, mutate func(State) (State, bool, error)) (State, bool, error) {
	if s == nil || s.kv == nil {
		return State{}, false, ErrInvalid
	}
	for i := 0; i < s.maxCASRetries; i++ {
		entry, err := s.kv.Get(key)
		if err != nil {
			return State{}, false, mapKVError(err)
		}
		state, err := decodeState(entry.Value())
		if err != nil {
			return State{}, false, err
		}
		next, changed, err := mutate(state)
		if err != nil || !changed {
			return next, changed, err
		}
		next.UpdatedAt = s.now()
		raw, err := encodeState(next)
		if err != nil {
			return State{}, false, err
		}
		if _, err := s.kv.Update(key, raw, entry.Revision()); err == nil {
			return next, true, nil
		} else if !errors.Is(err, nats.ErrKeyExists) {
			return State{}, false, mapKVError(err)
		}
	}
	return State{}, false, ErrConflict
}

func encodeState(state State) ([]byte, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal job state: %w", err)
	}
	return raw, nil
}

func decodeState(raw []byte) (State, error) {
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return State{}, fmt.Errorf("unmarshal job state: %w", err)
	}
	return state, nil
}

func mapKVError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, nats.ErrKeyNotFound), errors.Is(err, nats.ErrKeyDeleted):
		return ErrNotFound
	case errors.Is(err, nats.ErrKeyExists):
		return ErrConflict
	default:
		return err
	}
}

func validateJobItem(item *pb.JobItem) error {
	if item == nil ||
		strings.TrimSpace(item.GetSpaceId()) == "" ||
		strings.TrimSpace(item.GetJobId()) == "" ||
		strings.TrimSpace(item.GetJobItemId()) == "" ||
		strings.TrimSpace(item.GetJobType()) == "" ||
		strings.TrimSpace(item.GetCodePackageId()) == "" {
		return ErrInvalid
	}
	return nil
}

func (s *KVStore) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

func (s *State) findAttempt(attemptNo int) *Attempt {
	for i := range s.Attempts {
		if s.Attempts[i].AttemptNo == attemptNo {
			return &s.Attempts[i]
		}
	}
	return nil
}

func (s *State) markRunningAttemptLost(now time.Time) {
	for i := range s.Attempts {
		if s.Attempts[i].AttemptNo == s.AttemptNo && s.Attempts[i].Status == AttemptRunning {
			s.Attempts[i].Status = AttemptLost
			s.Attempts[i].FinishedAt = &now
			return
		}
	}
}

func (s *State) clearRunning() {
	s.RunningNode = ""
	s.RecoverAt = nil
	s.Queue.AckSubject = ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func matchesListFilter(state State, req *pb.ListJobItemsReq) bool {
	if req.GetSpaceId() != "" && state.SpaceID != req.GetSpaceId() {
		return false
	}
	if req.GetJobId() != "" && state.JobID != req.GetJobId() {
		return false
	}
	if req.GetJobType() != "" && state.JobType != req.GetJobType() {
		return false
	}
	if req.GetStatus() != pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED && state.Status != StatusFromPB(req.GetStatus()) {
		return false
	}
	return true
}

func normalizePage(page *commonpb.Page) (uint32, uint32) {
	p, size := uint32(1), uint32(20)
	if page != nil {
		if page.GetPage() > 0 {
			p = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	if size > 100 {
		size = 100
	}
	return p, size
}

func pageResult(page *commonpb.Page, total uint32) *commonpb.PageResult {
	p, size := normalizePage(page)
	return &commonpb.PageResult{Page: p, Size: size, Total: total}
}

func structToMap(st *structpb.Struct) map[string]any {
	if st == nil {
		return map[string]any{}
	}
	return st.AsMap()
}
