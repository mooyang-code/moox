package rpc

import (
	"context"
	"errors"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/resolver"
	tradepb "github.com/mooyang-code/moox/modules/trade/proto/tradegen"
	"trpc.group/trpc-go/trpc-go/log"
)

type DNSResolverServer struct {
	Resolver *resolver.Resolver
}

func (s *DNSResolverServer) ResolveDomains(ctx context.Context, req *tradepb.ResolveDomainsReq) (*tradepb.ResolveDomainsRsp, error) {
	started := time.Now()
	if req == nil {
		return &tradepb.ResolveDomainsRsp{RetInfo: invalidInfo(errors.New("request is required"))}, nil
	}
	if s == nil || s.Resolver == nil {
		return &tradepb.ResolveDomainsRsp{RetInfo: errorInfo(errors.New("dns resolver is disabled"))}, nil
	}
	results, err := s.Resolver.Resolve(ctx, req.GetDomains(), int(req.GetMaxIpsPerDomain()))
	if err != nil {
		if errors.Is(err, resolver.ErrInvalidDomain) {
			return &tradepb.ResolveDomainsRsp{RetInfo: invalidInfo(err)}, nil
		}
		return &tradepb.ResolveDomainsRsp{RetInfo: errorInfo(err)}, nil
	}
	rsp := &tradepb.ResolveDomainsRsp{RetInfo: success()}
	for _, result := range results {
		if result.Unresolved || len(result.IPs) == 0 {
			rsp.UnresolvedDomains = append(rsp.UnresolvedDomains, result.Domain)
			continue
		}
		item := &tradepb.DomainResolution{Domain: result.Domain}
		for _, resolved := range result.IPs {
			item.Ips = append(item.Ips, &tradepb.ResolvedIP{
				Ip:                  resolved.IP,
				TcpConnectLatencyMs: uint32(resolved.TCPConnectLatencyMS),
			})
		}
		rsp.Resolutions = append(rsp.Resolutions, item)
	}
	log.InfoContextf(ctx, "trade_dns_resolve_domains domains=%d resolutions=%d unresolved=%d duration_ms=%d", len(req.GetDomains()), len(rsp.GetResolutions()), len(rsp.GetUnresolvedDomains()), time.Since(started).Milliseconds())
	return rsp, nil
}
