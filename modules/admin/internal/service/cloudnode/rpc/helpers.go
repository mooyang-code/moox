// Package rpc 提供 cloudnode 对外的 trpc 普通 RPC 服务实现，
// 由统一 HTTP 转发层（/api/admin/cloudnode/{method}
// 与 /api/service/cloudnode/{method}）调度。
//
// 响应设计参考 storage/access 模块：每个响应首字段为 common.RetInfo（code/msg），
// code=SUCCESS(0) 表示成功，非 0 均为错误码；网关直接返回 PB JSON，不做加工。
package rpc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	cloudnodeconfig "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/config"
	cloudnodetypes "github.com/mooyang-code/moox/modules/admin/internal/service/cloudnode/types"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"google.golang.org/protobuf/types/known/structpb"
	"trpc.group/trpc-go/trpc-go/log"
)

// retOK 构造成功 RetInfo。
func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

// retErr 构造失败 RetInfo。
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	if code == pb.ErrorCode_SUCCESS {
		code = pb.ErrorCode_INNER_ERR
	}
	return &pb.RetInfo{Code: code, Msg: msg}
}

// regionInfo 地区信息。
type regionInfo struct {
	Code                     string
	Name                     string
	Tag                      string
	MaxNodes                 int
	MaxNamespacesPerRegion   int
	MaxFunctionsPerNamespace int
}

// getRegionsByProvider 根据云厂商获取地区列表（从配置读取）。
func getRegionsByProvider(provider string) []regionInfo {
	if provider != "tencent" {
		return nil
	}
	cfg := cloudnodeconfig.Get()
	regions := make([]regionInfo, 0, len(cfg.CloudRegions.Tencent))
	for _, r := range cfg.CloudRegions.Tencent {
		regions = append(regions, regionInfo{
			Code:                     r.Code,
			Name:                     r.Name,
			Tag:                      r.Tag,
			MaxNodes:                 r.MaxNodes,
			MaxNamespacesPerRegion:   r.MaxNamespacesPerRegion,
			MaxFunctionsPerNamespace: r.MaxFunctionsPerNamespace,
		})
	}
	return regions
}

// maskSecret 对凭证做简单脱敏：长度<=8 全隐藏，否则保留首3尾3。
func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "********"
	}
	return s[:3] + "********" + s[len(s)-3:]
}

// structToInterface 将 google.protobuf.Struct 转为 Go 任意值。
func structToInterface(s *structpb.Struct) interface{} {
	if s == nil {
		return nil
	}
	return s.AsMap()
}

// interfaceToStruct 将任意 Go 值转为 google.protobuf.Struct。
func interfaceToStruct(v interface{}) *structpb.Struct {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return nil
		}
		var parsed interface{}
		if err := json.Unmarshal([]byte(s), &parsed); err != nil {
			return nil
		}
		if m, ok := parsed.(map[string]interface{}); ok {
			st, err := structpb.NewStruct(m)
			if err == nil {
				return st
			}
		}
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	st, err := structpb.NewStruct(m)
	if err != nil {
		return nil
	}
	return st
}

// reportHeartbeatReqToTypes 将 pb.ReportHeartbeatReq 转为内部 types.ReportHeartbeatRequest。
// heartbeat 子服务仍以内部 types 为边界（牵动 collectmgr/dnsproxy/cli，暂不 PB 化）。
func reportHeartbeatReqToTypes(req *pb.ReportHeartbeatReq) *cloudnodetypes.ReportHeartbeatRequest {
	if req == nil {
		return nil
	}
	r := &cloudnodetypes.ReportHeartbeatRequest{
		NodeID:              req.GetNodeId(),
		NodeType:            req.GetNodeType(),
		RunningVersion:      req.GetRunningVersion(),
		SourceService:       req.GetSourceService(),
		Metrics:             asMapSafe(req.GetMetrics()),
		Metadata:            asMapSafe(req.GetMetadata()),
		SupportedCollectors: req.GetSupportedCollectors(),
		TasksMD5:            req.GetTasksMd5(),
	}
	if ts := req.GetTimestamp(); ts != "" {
		// 时间戳透传为空，types.Timestamp 为 *time.Time，由 service 内部按需处理；
		// 此处不强制解析，保持与旧 handler 一致（旧 handler 未用 Timestamp）。
		_ = ts
	}
	for _, item := range req.GetLocalDnsRecords() {
		r.LocalDNSRecords = append(r.LocalDNSRecords, &cloudnodetypes.LocalDNSReportItem{
			Domain: item.GetDomain(),
			IPList: item.GetIpList(),
		})
	}
	return r
}

// reportHeartbeatRspToPB 将内部 types.ReportHeartbeatResponse 转为 pb.ReportHeartbeatRsp。
func reportHeartbeatRspToPB(resp *cloudnodetypes.ReportHeartbeatResponse) *pb.ReportHeartbeatRsp {
	if resp == nil {
		return &pb.ReportHeartbeatRsp{RetInfo: retOK()}
	}
	tasks := make([]*pb.HeartbeatTaskInstance, 0, len(resp.TaskInstances))
	for _, t := range resp.TaskInstances {
		tasks = append(tasks, &pb.HeartbeatTaskInstance{
			Id:              int32(t.ID),
			TaskId:          t.TaskID,
			RuleId:          t.RuleID,
			PlannedExecNode: t.PlannedExecNode,
			DataType:        t.DataType,
			Symbol:          t.Symbol,
			Interval:        t.Interval,
			TaskParams:      t.TaskParams,
			Invalid:         int32(t.Invalid),
		})
	}
	return &pb.ReportHeartbeatRsp{
		RetInfo:        retOK(),
		PackageVersion: resp.PackageVersion,
		TaskInstances:  tasks,
		TasksMd5:       resp.TasksMD5,
	}
}

// asMapSafe 将 Struct 转为 map[string]interface{}，nil 返回 nil。
func asMapSafe(s *structpb.Struct) map[string]interface{} {
	if s == nil {
		return nil
	}
	if m, ok := structToInterface(s).(map[string]interface{}); ok {
		return m
	}
	return nil
}

// 保留 http/net/http/strings/fmt 引用占位（dispatcher 使用）。
var (
	_ = http.StatusOK
	_ = strings.TrimSpace
	_ = fmt.Sprintf
	_ = log.Info
)
