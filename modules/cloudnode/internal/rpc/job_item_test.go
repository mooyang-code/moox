package rpc

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobpb"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/commonpb"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type orderedQueue struct {
	steps      []string
	identities []cloudjobqueue.Identity
	published  []*pb.JobItem
	err        error
}

func (q *orderedQueue) EnsureJobExecutionQueue(_ context.Context, identity cloudjobqueue.Identity) error {
	q.steps = append(q.steps, "ensure")
	q.identities = append(q.identities, identity)
	return q.err
}
func (q *orderedQueue) Publish(_ context.Context, item *pb.JobItem) error {
	q.steps = append(q.steps, "publish")
	q.published = append(q.published, item)
	return q.err
}
func (q *orderedQueue) Close() error { return nil }

type orderedStore struct {
	steps   []string
	state   jobstate.State
	create  *jobstate.CreateResult
	markErr error
}

func (s *orderedStore) CreatePending(_ context.Context, item *pb.JobItem) (*jobstate.CreateResult, error) {
	s.steps = append(s.steps, "create")
	if s.create != nil {
		return s.create, nil
	}
	return &jobstate.CreateResult{JobItemID: item.GetJobItemId(), Status: pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED, Created: true, ShouldPublish: true}, nil
}
func (s *orderedStore) MarkEnqueueFailed(context.Context, string, string, string) error {
	return s.markErr
}
func (s *orderedStore) Get(context.Context, string, string) (*jobstate.State, error) {
	return &s.state, nil
}

func TestSubmitDoesNotRepublishDeduplicatedPendingItem(t *testing.T) {
	queue := &orderedQueue{}
	store := &orderedStore{create: &jobstate.CreateResult{
		JobItemID: "item-1", Status: pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED,
		Deduplicated: true, ShouldPublish: false,
	}}
	svc := &Service{executionQueue: queue, jobState: store}
	rsp, err := svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
	}}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		len(queue.steps) != 1 || queue.steps[0] != "ensure" {
		t.Fatalf("rsp=%+v err=%v queue=%v", rsp, err, queue.steps)
	}
}

func TestSubmitReturnsStateFailureWhenPublishAndMarkFail(t *testing.T) {
	queue := &orderedQueue{err: errors.New("publish failed")}
	store := &orderedStore{markErr: errors.New("state failed")}
	svc := &Service{executionQueue: queue, jobState: store}
	rsp, err := svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
	}}})
	if err != nil || rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%+v err=%v", rsp, err)
	}
}
func (s *orderedStore) MarkReported(context.Context, jobstate.ReportEvent) (*jobstate.State, bool, error) {
	return &s.state, true, nil
}
func (s *orderedStore) List(context.Context, *pb.ListJobItemsReq) ([]*pb.JobItemDetail, *commonpb.PageResult, error) {
	return nil, &commonpb.PageResult{}, nil
}

func TestSubmitEnsuresBeforeCreatingAndPublishing(t *testing.T) {
	queue := &orderedQueue{}
	store := &orderedStore{}
	svc := &Service{executionQueue: queue, jobState: store}
	rsp, err := svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
	}}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%+v err=%v", rsp, err)
	}
	if len(queue.steps) != 2 || queue.steps[0] != "ensure" || queue.steps[1] != "publish" || len(store.steps) != 1 {
		t.Fatalf("queue=%v store=%v", queue.steps, store.steps)
	}
}

func TestSubmitEnsureFailureHasNoStateSideEffect(t *testing.T) {
	queue := &orderedQueue{err: errors.New("ensure failed")}
	store := &orderedStore{}
	svc := &Service{executionQueue: queue, jobState: store}
	_, _ = svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline",
	}}})
	if len(store.steps) != 0 {
		t.Fatalf("state side effects = %v", store.steps)
	}
}

func TestSubmitRejectsInvalidExecuteAtAndContinuesBatch(t *testing.T) {
	queue := &orderedQueue{}
	store := &orderedStore{}
	svc := &Service{executionQueue: queue, jobState: store}
	invalid := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "invalid-time", JobType: "collect.symbol",
		ExecuteAt: &timestamppb.Timestamp{Seconds: 253402300800},
	}
	valid := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "valid-time", JobType: "collect.kline",
	}

	rsp, err := svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{invalid, valid}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("rsp=%+v err=%v", rsp, err)
	}
	if rsp.GetCreated() != 1 || rsp.GetRejected() != 1 || rsp.GetDeduplicated() != 0 {
		t.Fatalf("counts: created=%d deduplicated=%d rejected=%d", rsp.GetCreated(), rsp.GetDeduplicated(), rsp.GetRejected())
	}
	if len(rsp.GetAcks()) != 2 ||
		rsp.GetAcks()[0].GetJobItemId() != "invalid-time" ||
		rsp.GetAcks()[0].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_REJECTED ||
		!strings.Contains(rsp.GetAcks()[0].GetRejectReason(), "execute_at") ||
		rsp.GetAcks()[1].GetJobItemId() != "valid-time" ||
		rsp.GetAcks()[1].GetStatus() != pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED {
		t.Fatalf("acks=%+v", rsp.GetAcks())
	}
	if len(queue.identities) != 1 || queue.identities[0].JobType != "collect.kline" ||
		len(store.steps) != 1 || len(queue.published) != 1 || queue.published[0].GetJobItemId() != "valid-time" {
		t.Fatalf("identities=%+v store=%v published=%+v", queue.identities, store.steps, queue.published)
	}
}

func TestSubmitAcceptsMissingExecuteAt(t *testing.T) {
	queue := &orderedQueue{}
	store := &orderedStore{}
	svc := &Service{executionQueue: queue, jobState: store}
	item := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "immediate", JobType: "collect.kline",
	}

	rsp, err := svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{item}})
	if err != nil || rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		rsp.GetCreated() != 1 || len(queue.published) != 1 || queue.published[0].GetExecuteAt() != nil {
		t.Fatalf("rsp=%+v err=%v published=%+v", rsp, err, queue.published)
	}
}

func TestFutureExecuteAtSurvivesSubmitAndGet(t *testing.T) {
	executeAt := time.Date(2026, 7, 28, 1, 2, 3, 456, time.UTC)
	queue := &orderedQueue{}
	store := &orderedStore{state: jobstate.State{
		SpaceID: "crypto", JobID: "job-1", JobItemID: "scheduled", JobType: "collect.kline",
		ExecuteAt: &executeAt,
	}}
	svc := &Service{executionQueue: queue, jobState: store}
	item := &pb.JobItem{
		SpaceId: "crypto", JobId: "job-1", JobItemId: "scheduled", JobType: "collect.kline",
		ExecuteAt: timestamppb.New(executeAt),
	}

	submitRsp, err := svc.SubmitJobItems(context.Background(), &pb.SubmitJobItemsReq{Items: []*pb.JobItem{item}})
	if err != nil || submitRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("submit rsp=%+v err=%v", submitRsp, err)
	}
	if len(queue.published) != 1 || !queue.published[0].GetExecuteAt().AsTime().Equal(executeAt) {
		t.Fatalf("published execute_at=%v, want %v", queue.published, executeAt)
	}
	getRsp, err := svc.GetJobItem(context.Background(), &pb.GetJobItemReq{SpaceId: "crypto", JobItemId: "scheduled"})
	if err != nil || getRsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS ||
		getRsp.GetItem().GetExecuteAt() == nil || !getRsp.GetItem().GetExecuteAt().AsTime().Equal(executeAt) {
		t.Fatalf("get rsp=%+v err=%v", getRsp, err)
	}
}

func TestJobQueueContractsExcludeCodePackageRouting(t *testing.T) {
	for name, message := range map[string]protoreflect.Message{
		"JobItem":               (&pb.JobItem{}).ProtoReflect(),
		"JobExecutionRequested": (&cloudjobpb.JobExecutionRequested{}).ProtoReflect(),
	} {
		if field := message.Descriptor().Fields().ByName("code_package_id"); field != nil {
			t.Fatalf("%s unexpectedly contains field %s", name, field.FullName())
		}
	}
	if _, ok := reflect.TypeOf(cloudjobqueue.Identity{}).FieldByName("CodePackageID"); ok {
		t.Fatal("cloudjobqueue.Identity unexpectedly contains CodePackageID")
	}
}
