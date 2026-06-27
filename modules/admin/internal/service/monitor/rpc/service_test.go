package rpc

import (
	"context"
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// stubMonitor 实现 monitor.Service，用于 RPC 单测。
type stubMonitor struct {
	enableErr        error
	disableErr       error
	isEnabled        bool
	isEnabledErr     error
	currentMetrics   []*model.HostMetrics
	currentErr       error
	history          []*model.HistoryPoint
	historyErr       error
	testResult       *pb.TestResult
	testErr          error
	lastEnableID     int
	lastDisableID    int
	lastStatusID     int
	lastHistoryAddr  string
	lastHistoryDur   string
	lastCurrentIDs   []int
	lastTestID       int
}

func (s *stubMonitor) EnableMonitor(ctx context.Context, hostID int) error {
	s.lastEnableID = hostID
	return s.enableErr
}
func (s *stubMonitor) DisableMonitor(ctx context.Context, hostID int) error {
	s.lastDisableID = hostID
	return s.disableErr
}
func (s *stubMonitor) IsMonitorEnabled(ctx context.Context, hostID int) (bool, error) {
	s.lastStatusID = hostID
	return s.isEnabled, s.isEnabledErr
}
func (s *stubMonitor) GetCurrentMetrics(ctx context.Context, hostIDs []int) ([]*model.HostMetrics, error) {
	s.lastCurrentIDs = hostIDs
	return s.currentMetrics, s.currentErr
}
func (s *stubMonitor) GetHistoryMetrics(ctx context.Context, hostAddress, duration string) ([]*model.HistoryPoint, error) {
	s.lastHistoryAddr = hostAddress
	s.lastHistoryDur = duration
	return s.history, s.historyErr
}
func (s *stubMonitor) TestNodeExporter(ctx context.Context, hostID int) (*pb.TestResult, error) {
	s.lastTestID = hostID
	return s.testResult, s.testErr
}
func (s *stubMonitor) CollectAll(ctx context.Context) error      { return nil }
func (s *stubMonitor) CleanHistory(ctx context.Context, k int) error { return nil }

func TestEnableMonitorRejectsZeroHostID(t *testing.T) {
	svc := NewService(&stubMonitor{})
	rsp, err := svc.EnableMonitor(context.Background(), &pb.EnableMonitorReq{HostId: 0})
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("zero host_id should be rejected")
	}
}

func TestEnableMonitorSuccess(t *testing.T) {
	stub := &stubMonitor{}
	svc := NewService(stub)
	rsp, err := svc.EnableMonitor(context.Background(), &pb.EnableMonitorReq{HostId: 42})
	if err != nil {
		t.Fatalf("unexpected go error: %v", err)
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("expected success, got code=%v", rsp.GetRetInfo().GetCode())
	}
	if stub.lastEnableID != 42 {
		t.Fatalf("service not called with 42, got %d", stub.lastEnableID)
	}
}

func TestEnableMonitorPropagatesServiceErrorAsRetInfo(t *testing.T) {
	stub := &stubMonitor{enableErr: errors.New("db down")}
	svc := NewService(stub)
	rsp, _ := svc.EnableMonitor(context.Background(), &pb.EnableMonitorReq{HostId: 1})
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_INNER_ERR {
		t.Fatalf("expected INNER_ERR, got %v", rsp.GetRetInfo().GetCode())
	}
}

func TestGetMonitorStatusReturnsEnabled(t *testing.T) {
	stub := &stubMonitor{isEnabled: true}
	svc := NewService(stub)
	rsp, _ := svc.GetMonitorStatus(context.Background(), &pb.GetMonitorStatusReq{HostId: 7})
	if !rsp.GetEnabled() {
		t.Fatalf("expected enabled=true")
	}
	if rsp.GetRetInfo().GetCode() != pb.ErrorCode_SUCCESS {
		t.Fatalf("expected success")
	}
}

func TestGetCurrentMetricsParsesHostIDs(t *testing.T) {
	stub := &stubMonitor{currentMetrics: []*model.HostMetrics{{HostID: 1, HostName: "n1"}}}
	svc := NewService(stub)
	rsp, _ := svc.GetCurrentMetrics(context.Background(), &pb.GetCurrentMetricsReq{HostIds: "1,2,3"})
	if len(stub.lastCurrentIDs) != 3 {
		t.Fatalf("expected 3 ids, got %v", stub.lastCurrentIDs)
	}
	if len(rsp.GetMetrics()) != 1 || rsp.GetMetrics()[0].GetHostId() != 1 {
		t.Fatalf("unexpected metrics: %v", rsp.GetMetrics())
	}
}

func TestGetHistoryMetricsDefaultsDuration(t *testing.T) {
	stub := &stubMonitor{}
	svc := NewService(stub)
	svc.GetHistoryMetrics(context.Background(), &pb.GetHistoryMetricsReq{HostAddress: "1.2.3.4"})
	if stub.lastHistoryDur != "1h" {
		t.Fatalf("expected default 1h, got %s", stub.lastHistoryDur)
	}
	if stub.lastHistoryAddr != "1.2.3.4" {
		t.Fatalf("expected addr 1.2.3.4, got %s", stub.lastHistoryAddr)
	}
}

func TestGetHistoryMetricsRejectsEmptyAddress(t *testing.T) {
	svc := NewService(&stubMonitor{})
	rsp, _ := svc.GetHistoryMetrics(context.Background(), &pb.GetHistoryMetricsReq{})
	if rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("empty address should be rejected")
	}
}
