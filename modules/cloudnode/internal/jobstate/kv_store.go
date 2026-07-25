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
	"github.com/mooyang-code/moox/packages/jetstream"
	"google.golang.org/protobuf/types/known/structpb"
)

type Clock interface{ Now() time.Time }

type Options struct {
	Clock         Clock
	MaxCASRetries int
}

type Store interface {
	CreatePending(context.Context, *pb.JobItem) (*CreateResult, error)
	MarkEnqueueFailed(context.Context, string, string, string) error
	Get(context.Context, string, string) (*State, error)
	MarkReported(context.Context, ReportEvent) (*State, bool, error)
	List(context.Context, *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error)
}

type KVStore struct {
	kv            jetstream.KeyValue
	clock         Clock
	maxCASRetries int
}

func NewKVStore(kv jetstream.KeyValue, opts Options) *KVStore {
	retries := opts.MaxCASRetries
	if retries <= 0 {
		retries = 5
	}
	return &KVStore{kv: kv, clock: opts.Clock, maxCASRetries: retries}
}

func (s *KVStore) CreatePending(ctx context.Context, item *pb.JobItem) (*CreateResult, error) {
	if s == nil || s.kv == nil || ValidateJobItem(item) != nil {
		return nil, ErrInvalid
	}
	now := s.now()
	state := State{
		SchemaVersion: 1, SpaceID: item.GetSpaceId(), JobID: item.GetJobId(), JobItemID: item.GetJobItemId(),
		JobType: item.GetJobType(), CodePackageID: item.GetCodePackageId(), Params: structToMap(item.GetParams()),
		Priority: item.GetPriority(), Status: StatusPending, CreatedAt: now, UpdatedAt: now,
	}
	raw, err := encodeState(state)
	if err != nil {
		return nil, err
	}
	if _, err := s.kv.Create(JobKey(state.SpaceID, state.JobItemID), raw); err != nil {
		if !errors.Is(err, jetstream.ErrKVKeyExists) {
			return nil, mapKVError(err)
		}
		updated, changed, err := s.withStateCAS(ctx, JobKey(state.SpaceID, state.JobItemID), func(current State) (State, bool, error) {
			if current.Status != StatusEnqueueFailed {
				return current, false, nil
			}
			state.CreatedAt = current.CreatedAt
			return state, true, nil
		})
		if err != nil {
			return nil, err
		}
		if changed {
			return &CreateResult{JobItemID: updated.JobItemID, Status: pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED, Created: true}, nil
		}
		return &CreateResult{JobItemID: state.JobItemID, Status: pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED, Deduplicated: true}, nil
	}
	return &CreateResult{JobItemID: state.JobItemID, Status: pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED, Created: true}, nil
}

func (s *KVStore) MarkEnqueueFailed(ctx context.Context, spaceID, jobItemID, message string) error {
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
	return &state, err
}

// MarkReported is idempotent and first-terminal-wins. Missing items are accepted.
func (s *KVStore) MarkReported(ctx context.Context, event ReportEvent) (*State, bool, error) {
	if event.Status != StatusSuccess && event.Status != StatusFailed {
		return nil, false, ErrInvalid
	}
	now := event.Time
	if now.IsZero() {
		now = s.now()
	}
	updated, changed, err := s.withStateCAS(ctx, JobKey(event.SpaceID, event.JobItemID), func(state State) (State, bool, error) {
		if state.IsTerminal() {
			return state, false, nil
		}
		state.Status = event.Status
		state.ResultSummary = event.ResultSummary
		state.LastErrorKind = event.ErrorKind
		state.LastErrorCode = event.ErrorCode
		state.LastErrorMessage = event.ErrorMessage
		state.DurationMS = event.DurationMS
		state.ExecutionNode = strings.TrimSpace(event.NodeID)
		state.FinishedAt = &now
		return state, true, nil
	})
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &updated, changed, nil
}

func (s *KVStore) List(_ context.Context, req *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error) {
	keys, err := s.kv.Keys()
	if errors.Is(err, jetstream.ErrKVNoKeys) {
		return nil, pageResult(req.GetPage(), 0), nil
	}
	if err != nil {
		return nil, nil, mapKVError(err)
	}
	states := make([]State, 0, len(keys))
	for _, key := range keys {
		entry, getErr := s.kv.Get(key)
		if getErr != nil {
			continue
		}
		state, decodeErr := decodeState(entry.Value())
		if decodeErr == nil && matchesListFilter(state, req) {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].CreatedAt.After(states[j].CreatedAt) })
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
	return out, pageResult(req.GetPage(), uint32(len(states))), nil
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
		} else if !errors.Is(err, jetstream.ErrKVKeyExists) {
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
	case errors.Is(err, jetstream.ErrKVKeyNotFound):
		return ErrNotFound
	case errors.Is(err, jetstream.ErrKVKeyExists):
		return ErrConflict
	default:
		return err
	}
}

func ValidateJobItem(item *pb.JobItem) error {
	if item == nil {
		return ErrInvalid
	}
	for _, value := range []string{item.GetSpaceId(), item.GetJobId(), item.GetJobItemId(), item.GetJobType(), item.GetCodePackageId()} {
		if value == "" || strings.TrimSpace(value) != value {
			return ErrInvalid
		}
	}
	return nil
}

func (s *KVStore) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now()
	}
	return time.Now().UTC()
}

func matchesListFilter(state State, req *pb.ListJobItemsReq) bool {
	return (req.GetSpaceId() == "" || state.SpaceID == req.GetSpaceId()) &&
		(req.GetJobId() == "" || state.JobID == req.GetJobId()) &&
		(req.GetJobType() == "" || state.JobType == req.GetJobType()) &&
		(req.GetStatus() == pb.JobItemStatus_JOB_ITEM_STATUS_UNSPECIFIED || state.Status == StatusFromPB(req.GetStatus()))
}

func normalizePage(page *commonpb.Page) (uint32, uint32) {
	number, size := uint32(1), uint32(20)
	if page != nil {
		if page.GetPage() > 0 {
			number = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	if size > 100 {
		size = 100
	}
	return number, size
}

func pageResult(page *commonpb.Page, total uint32) *commonpb.PageResult {
	number, size := normalizePage(page)
	return &commonpb.PageResult{Page: number, Size: size, Total: total}
}

func structToMap(value *structpb.Struct) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value.AsMap()
}
