// Package rpc 提供 monitor 对外的 trpc 普通 RPC 服务实现，
// 由统一 HTTP 转发层（/api/admin/monitor/{method}）调度。
package rpc

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor"
	"github.com/mooyang-code/moox/modules/admin/internal/service/monitor/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现 pb.MonitorService，承载 monitor 业务逻辑。
type Service struct {
	pb.UnimplementedMonitor
	svc monitor.Service
}

// NewService 创建 Monitor RPC 实现。
func NewService(svc monitor.Service) *Service {
	return &Service{svc: svc}
}

// EnableMonitor 启用主机监控。
func (s *Service) EnableMonitor(ctx context.Context, req *pb.EnableMonitorReq) (*pb.EnableMonitorRsp, error) {
	if req.GetHostId() == 0 {
		return &pb.EnableMonitorRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid host_id")}, nil
	}
	if err := s.svc.EnableMonitor(ctx, int(req.GetHostId())); err != nil {
		log.ErrorContextf(ctx, "[Monitor] EnableMonitor failed: %v", err)
		return &pb.EnableMonitorRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "enable monitor failed")}, nil
	}
	return &pb.EnableMonitorRsp{RetInfo: retOK()}, nil
}

// DisableMonitor 禁用主机监控。
func (s *Service) DisableMonitor(ctx context.Context, req *pb.DisableMonitorReq) (*pb.DisableMonitorRsp, error) {
	if req.GetHostId() == 0 {
		return &pb.DisableMonitorRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid host_id")}, nil
	}
	if err := s.svc.DisableMonitor(ctx, int(req.GetHostId())); err != nil {
		log.ErrorContextf(ctx, "[Monitor] DisableMonitor failed: %v", err)
		return &pb.DisableMonitorRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "disable monitor failed")}, nil
	}
	return &pb.DisableMonitorRsp{RetInfo: retOK()}, nil
}

// GetMonitorStatus 获取主机监控状态。
func (s *Service) GetMonitorStatus(ctx context.Context, req *pb.GetMonitorStatusReq) (*pb.GetMonitorStatusRsp, error) {
	if req.GetHostId() == 0 {
		return &pb.GetMonitorStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid host_id")}, nil
	}
	enabled, err := s.svc.IsMonitorEnabled(ctx, int(req.GetHostId()))
	if err != nil {
		log.ErrorContextf(ctx, "[Monitor] GetMonitorStatus failed: %v", err)
		return &pb.GetMonitorStatusRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "get monitor status failed")}, nil
	}
	return &pb.GetMonitorStatusRsp{RetInfo: retOK(), Enabled: enabled}, nil
}

// GetCurrentMetrics 获取当前监控指标。
func (s *Service) GetCurrentMetrics(ctx context.Context, req *pb.GetCurrentMetricsReq) (*pb.GetCurrentMetricsRsp, error) {
	hostIDs := parseHostIDs(req.GetHostIds())
	metrics, err := s.svc.GetCurrentMetrics(ctx, hostIDs)
	if err != nil {
		log.ErrorContextf(ctx, "[Monitor] GetCurrentMetrics failed: %v", err)
		return &pb.GetCurrentMetricsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "get metrics failed")}, nil
	}
	pbMetrics := make([]*pb.HostMetrics, 0, len(metrics))
	for _, m := range metrics {
		pbMetrics = append(pbMetrics, toHostMetrics(m))
	}
	return &pb.GetCurrentMetricsRsp{RetInfo: retOK(), Metrics: pbMetrics}, nil
}

// GetHistoryMetrics 获取历史监控数据。
func (s *Service) GetHistoryMetrics(ctx context.Context, req *pb.GetHistoryMetricsReq) (*pb.GetHistoryMetricsRsp, error) {
	if req.GetHostAddress() == "" {
		return &pb.GetHistoryMetricsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "host_address is required")}, nil
	}
	duration := req.GetDuration()
	if duration == "" {
		duration = "1h"
	}
	history, err := s.svc.GetHistoryMetrics(ctx, req.GetHostAddress(), duration)
	if err != nil {
		log.ErrorContextf(ctx, "[Monitor] GetHistoryMetrics failed: %v", err)
		return &pb.GetHistoryMetricsRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "get history failed")}, nil
	}
	pbHistory := make([]*pb.HistoryPoint, 0, len(history))
	for _, h := range history {
		pbHistory = append(pbHistory, toHistoryPoint(h))
	}
	return &pb.GetHistoryMetricsRsp{RetInfo: retOK(), History: pbHistory}, nil
}

// TestNodeExporter 测试 Node Exporter 连通性。
func (s *Service) TestNodeExporter(ctx context.Context, req *pb.TestNodeExporterReq) (*pb.TestNodeExporterRsp, error) {
	if req.GetHostId() == 0 {
		return &pb.TestNodeExporterRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "invalid host_id")}, nil
	}
	result, err := s.svc.TestNodeExporter(ctx, int(req.GetHostId()))
	if err != nil {
		log.ErrorContextf(ctx, "[Monitor] TestNodeExporter failed: %v", err)
		return &pb.TestNodeExporterRsp{RetInfo: retErr(pb.ErrorCode_INNER_ERR, "test failed")}, nil
	}
	return &pb.TestNodeExporterRsp{RetInfo: retOK(), Result: result}, nil
}

// parseHostIDs 解析逗号分隔的 host_ids 字符串为 ID 列表。
func parseHostIDs(s string) []int {
	if s == "" {
		return nil
	}
	var ids []int
	for _, part := range strings.Split(s, ",") {
		if id, err := strconv.Atoi(strings.TrimSpace(part)); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func toHostMetrics(m *model.HostMetrics) *pb.HostMetrics {
	if m == nil {
		return nil
	}
	rsp := &pb.HostMetrics{
		HostId:    int32(m.HostID),
		HostName:  m.HostName,
		Address:   m.Address,
		Status:    m.Status,
		Timestamp: m.Timestamp.Format(time.RFC3339),
		ErrorMsg:  m.ErrorMsg,
	}
	if m.CPU != nil {
		rsp.Cpu = &pb.CPUMetrics{Usage: m.CPU.Usage, Cores: int32(m.CPU.Cores)}
	}
	if m.Memory != nil {
		rsp.Memory = &pb.MemoryMetrics{
			Total:     m.Memory.Total,
			Available: m.Memory.Available,
			Used:      m.Memory.Used,
			Free:      m.Memory.Free,
			Buffers:   m.Memory.Buffers,
			Cached:    m.Memory.Cached,
			Percent:   m.Memory.Percent,
		}
	}
	for _, d := range m.Disks {
		rsp.Disks = append(rsp.Disks, &pb.DiskMetrics{
			Device:     d.Device,
			Mountpoint: d.Mountpoint,
			Fstype:     d.FSType,
			Total:      d.Total,
			Used:       d.Used,
			Available:  d.Available,
			Percent:    d.Percent,
		})
	}
	for _, n := range m.Networks {
		rsp.Networks = append(rsp.Networks, &pb.NetworkSpeed{
			Device:  n.Device,
			RxSpeed: n.RxSpeed,
			TxSpeed: n.TxSpeed,
		})
	}
	if m.Load != nil {
		rsp.Load = &pb.LoadMetrics{Load1: m.Load.Load1, Load5: m.Load.Load5, Load15: m.Load.Load15}
	}
	return rsp
}

func toHistoryPoint(h *model.HistoryPoint) *pb.HistoryPoint {
	if h == nil {
		return nil
	}
	return &pb.HistoryPoint{
		Timestamp:      h.Timestamp.Format(time.RFC3339),
		CpuUsage:       h.CPUUsage,
		MemoryPercent:  h.MemoryPercent,
		DiskPercent:    h.DiskPercent,
		NetworkRxSpeed: h.NetworkRxSpeed,
		NetworkTxSpeed: h.NetworkTxSpeed,
	}
}

// retOK 构造成功 RetInfo。
func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

// retErr 构造错误 RetInfo。
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}
