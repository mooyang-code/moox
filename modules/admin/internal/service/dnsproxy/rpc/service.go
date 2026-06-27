// Package rpc 提供 dnsproxy 对外的 trpc 普通 RPC 服务实现，
// 由统一 HTTP 转发层（/api/admin/dnsproxy/{method}）调度。
package rpc

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"

	"trpc.group/trpc-go/trpc-go/log"
)

// Service 实现 pb.DnsService，承载 dnsproxy 业务逻辑。
type Service struct {
	pb.UnimplementedDns
}

// NewService 创建 DnsService RPC 实现。
func NewService() *Service {
	return &Service{}
}

// ListDNSRecords 列出所有 DNS 解析记录。
func (s *Service) ListDNSRecords(ctx context.Context, req *pb.ListDNSRecordsReq) (*pb.ListDNSRecordsRsp, error) {
	cfg := dnsproxy.GetConfig()
	if cfg == nil {
		log.WarnContext(ctx, "[DnsService] 配置未初始化，返回空列表")
		return &pb.ListDNSRecordsRsp{
			RetInfo:    retOK(),
			Records:    []*pb.DNSRecord{},
			PageResult: &pb.PageResult{},
		}, nil
	}

	domains := cfg.DNSProxy.Domains
	if len(domains) == 0 {
		return &pb.ListDNSRecordsRsp{
			RetInfo:    retOK(),
			Records:    []*pb.DNSRecord{},
			PageResult: &pb.PageResult{Total: 0},
		}, nil
	}

	records := make([]*pb.DNSRecord, 0, len(domains))
	for _, domain := range domains {
		merged, err := dnsproxy.GetMergedDNSResult(ctx, domain)
		if err != nil {
			// 缓存未命中，跳过该域名
			continue
		}
		if !merged.Success || len(merged.IPList) == 0 {
			continue
		}
		records = append(records, toDNSRecord(merged.Domain, merged.IPList, merged.ProbeAt, merged.Success))
	}

	return &pb.ListDNSRecordsRsp{
		RetInfo: retOK(),
		Records: records,
		PageResult: &pb.PageResult{
			Total: uint32(len(records)),
		},
	}, nil
}

// GetDNSRecord 获取指定域名的 DNS 解析记录详情。
func (s *Service) GetDNSRecord(ctx context.Context, req *pb.GetDNSRecordReq) (*pb.GetDNSRecordRsp, error) {
	if req.GetDomain() == "" {
		return &pb.GetDNSRecordRsp{
			RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, "domain 不能为空"),
		}, nil
	}

	merged, err := dnsproxy.GetMergedDNSResult(ctx, req.GetDomain())
	if err != nil {
		return &pb.GetDNSRecordRsp{
			RetInfo: retErr(pb.ErrorCode_NOT_FOUND, "DNS 解析记录不存在"),
		}, nil
	}

	return &pb.GetDNSRecordRsp{
		RetInfo: retOK(),
		Record:  toDNSRecord(merged.Domain, merged.IPList, merged.ProbeAt, merged.Success),
	}, nil
}

// toDNSRecord 把 service 层的合并探测结果转换为 PB DNSRecord。
func toDNSRecord(domain string, ipList []*dnsproxy.IPInfo, probeAt time.Time, success bool) *pb.DNSRecord {
	pbIPs := make([]*pb.IPInfo, 0, len(ipList))
	for _, ip := range ipList {
		pbIPs = append(pbIPs, &pb.IPInfo{
			Ip:        ip.IP,
			Latency:   ip.Latency,
			Available: ip.Available,
		})
	}
	return &pb.DNSRecord{
		Domain:    domain,
		IpList:    pbIPs,
		ResolveAt: probeAt.Format(time.RFC3339),
		Success:   success,
		BestIps:   buildBestIPs(ipList),
	}
}

// buildBestIPs 构建最优 IP 列表字符串：筛选 Available=true 的 IP，
// 按延迟升序，用 + 号连接。
func buildBestIPs(ipList []*dnsproxy.IPInfo) string {
	if len(ipList) == 0 {
		return ""
	}
	var available []*dnsproxy.IPInfo
	for _, ip := range ipList {
		if ip.Available {
			available = append(available, ip)
		}
	}
	if len(available) == 0 {
		return ""
	}
	sort.Slice(available, func(i, j int) bool {
		return available[i].Latency < available[j].Latency
	})
	ips := make([]string, 0, len(available))
	for _, ip := range available {
		ips = append(ips, ip.IP)
	}
	return strings.Join(ips, "+")
}

// retOK 构造成功 RetInfo。
func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

// retErr 构造错误 RetInfo。
func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}
