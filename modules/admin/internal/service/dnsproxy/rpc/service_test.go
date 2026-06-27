package rpc

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

func TestBuildBestIPs(t *testing.T) {
	tests := []struct {
		name   string
		ipList []*dnsproxy.IPInfo
		want   string
	}{
		{
			name:   "empty",
			ipList: nil,
			want:   "",
		},
		{
			name: "all unavailable",
			ipList: []*dnsproxy.IPInfo{
				{IP: "1.1.1.1", Latency: 100, Available: false},
			},
			want: "",
		},
		{
			name: "sorted by latency ascending, joined by +",
			ipList: []*dnsproxy.IPInfo{
				{IP: "3.3.3.3", Latency: 300, Available: true},
				{IP: "1.1.1.1", Latency: 100, Available: true},
				{IP: "2.2.2.2", Latency: 200, Available: true},
				{IP: "4.4.4.4", Latency: 50, Available: false},
			},
			want: "1.1.1.1+2.2.2.2+3.3.3.3",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := buildBestIPs(tc.ipList); got != tc.want {
				t.Fatalf("buildBestIPs() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestToDNSRecord(t *testing.T) {
	probeAt := time.Date(2026, 6, 26, 22, 0, 0, 0, time.UTC)
	ipList := []*dnsproxy.IPInfo{
		{IP: "1.1.1.1", Latency: 100, Available: true},
		{IP: "2.2.2.2", Latency: 200, Available: false},
	}
	rec := toDNSRecord("example.com", ipList, probeAt, true)
	if rec.Domain != "example.com" {
		t.Fatalf("Domain = %q, want example.com", rec.Domain)
	}
	if rec.Success != true {
		t.Fatalf("Success = false, want true")
	}
	if rec.ResolveAt != probeAt.Format(time.RFC3339) {
		t.Fatalf("ResolveAt = %q, want %q", rec.ResolveAt, probeAt.Format(time.RFC3339))
	}
	if len(rec.IpList) != 2 {
		t.Fatalf("IpList len = %d, want 2", len(rec.IpList))
	}
	if rec.IpList[0].Ip != "1.1.1.1" || rec.IpList[0].Latency != 100 || !rec.IpList[0].Available {
		t.Fatalf("IpList[0] = %+v, mismatch", rec.IpList[0])
	}
	if rec.BestIps != "1.1.1.1" {
		t.Fatalf("BestIps = %q, want 1.1.1.1", rec.BestIps)
	}
}

// TestGetDNSRecordRejectsEmptyDomain 验证空 domain 入参返回错误 ret_info。
func TestGetDNSRecordRejectsEmptyDomain(t *testing.T) {
	svc := NewService()
	rsp, err := svc.GetDNSRecord(nil, nil)
	if err != nil {
		t.Fatalf("GetDNSRecord should not return go error, got: %v", err)
	}
	if rsp.GetRetInfo().GetCode() == pb.ErrorCode_SUCCESS {
		t.Fatalf("empty domain should return non-success code")
	}
}
