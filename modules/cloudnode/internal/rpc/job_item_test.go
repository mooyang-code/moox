package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobstate"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type orderedQueue struct {
	steps []string
	err   error
}

func (q *orderedQueue) EnsureJobExecutionQueue(context.Context, cloudjobqueue.Identity) error {
	q.steps = append(q.steps, "ensure")
	return q.err
}
func (q *orderedQueue) Publish(context.Context, *pb.JobItem) error {
	q.steps = append(q.steps, "publish")
	return q.err
}
func (q *orderedQueue) Close() error { return nil }

type orderedStore struct {
	steps   []string
	state   jobstate.State
	create  *jobstate.CreateResult
	markErr error
}

func (s *orderedStore) CreatePending(context.Context, *pb.JobItem) (*jobstate.CreateResult, error) {
	s.steps = append(s.steps, "create")
	if s.create != nil {
		return s.create, nil
	}
	return &jobstate.CreateResult{JobItemID: "item-1", Status: pb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED, Created: true, ShouldPublish: true}, nil
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
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline", CodePackageId: "pkg",
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
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline", CodePackageId: "pkg",
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
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline", CodePackageId: "pkg",
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
		SpaceId: "crypto", JobId: "job-1", JobItemId: "item-1", JobType: "collect.kline", CodePackageId: "pkg",
	}}})
	if len(store.steps) != 0 {
		t.Fatalf("state side effects = %v", store.steps)
	}
}
