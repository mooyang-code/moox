package rpc

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
)

type fakeScheduleService struct {
	calls   int
	spaceID string
	spaces  []string
}

func (f *fakeScheduleService) RecalculateAllTaskInstances(ctx context.Context, req *pb.RecalculateAllTaskInstancesReq) (*pb.RecalculateAllTaskInstancesRsp, error) {
	f.calls++
	f.spaceID = req.GetSpaceId()
	f.spaces = append(f.spaces, req.GetSpaceId())
	return &pb.RecalculateAllTaskInstancesRsp{RetInfo: retOK()}, nil
}

func TestHandleScheduleWithoutSpaceScansBuiltInMarkets(t *testing.T) {
	fake := &fakeScheduleService{}
	setDefaultScheduleService(fake)
	defer setDefaultScheduleService(nil)

	if err := HandleSchedule(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"stock_cn", "stock_us", "crypto_binance", "crypto_okx"}
	if len(fake.spaces) != len(want) {
		t.Fatalf("spaces = %#v", fake.spaces)
	}
	for index := range want {
		if fake.spaces[index] != want[index] {
			t.Fatalf("spaces = %#v, want %#v", fake.spaces, want)
		}
	}
}

func TestParseScheduleParamsSupportsSpaceID(t *testing.T) {
	got := parseScheduleParams("space_id=crypto;node_id=node-a")
	if got.SpaceID != "crypto" {
		t.Fatalf("SpaceID = %q, want crypto", got.SpaceID)
	}

	got = parseScheduleParams("crypto")
	if got.SpaceID != "crypto" {
		t.Fatalf("bare SpaceID = %q, want crypto", got.SpaceID)
	}
}

func TestHandleScheduleCallsDefaultService(t *testing.T) {
	fake := &fakeScheduleService{}
	setDefaultScheduleService(fake)
	defer setDefaultScheduleService(nil)

	if err := HandleSchedule(context.Background(), "space_id=crypto"); err != nil {
		t.Fatal(err)
	}
	if fake.calls != 1 || fake.spaceID != "crypto" {
		t.Fatalf("calls=%d space_id=%q, want one call for crypto", fake.calls, fake.spaceID)
	}
}
