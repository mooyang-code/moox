package healthz

import (
	"context"
	"testing"

	"trpc.group/trpc-go/trpc-go/errs"
)

func TestDrainGateRejectsNewRequestsButNotInFlightOnes(t *testing.T) {
	gate := &DrainGate{}
	handler := func(context.Context, interface{}) (interface{}, error) { return "ok", nil }
	if rsp, err := gate.Filter()(context.Background(), nil, handler); err != nil || rsp != "ok" {
		t.Fatalf("未排空时请求应通过: %v %v", rsp, err)
	}
	gate.Begin()
	called := false
	_, err := gate.Filter()(context.Background(), nil, func(context.Context, interface{}) (interface{}, error) { called = true; return nil, nil })
	if called || errs.Code(err) != errs.RetServerNoService {
		t.Fatalf("排空后新请求必须在进入业务前被拒绝: called=%v err=%v", called, err)
	}
}
