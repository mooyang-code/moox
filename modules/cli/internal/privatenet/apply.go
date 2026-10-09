package privatenet

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

// Cloud 是发现主机网络拓扑需要的腾讯云只读接口。
type Cloud interface {
	LookupHost(ctx context.Context, publicIP string, regions []string) (tencent.CloudInstance, error)
	DescribeVpc(ctx context.Context, region, vpcID string) (tencent.VpcInfo, error)
}

type Result struct {
	DryRun bool    `json:"dry_run"`
	Status string  `json:"status"`
	Plan   Plan    `json:"plan"`
	Probes []Probe `json:"probes,omitempty"`
}

// ResolveHosts 按公网地址查询主机对应的腾讯云实例，以及它所在 VPC 的网段。
func ResolveHosts(ctx context.Context, cloud Cloud, hosts []HostTarget, regions []string) ([]ResolvedHost, error) {
	resolved := make([]ResolvedHost, 0, len(hosts))
	for _, host := range hosts {
		instance, err := cloud.LookupHost(ctx, host.Address, regions)
		if err != nil {
			return nil, fmt.Errorf("lookup %s (%s): %w", host.Name, host.Address, err)
		}
		item := ResolvedHost{HostTarget: host, Instance: instance}
		if instance.Kind == tencent.KindCVM && instance.VpcID != "" {
			vpc, err := cloud.DescribeVpc(ctx, instance.Region, instance.VpcID)
			if err != nil {
				return nil, fmt.Errorf("describe vpc for %s: %w", host.Name, err)
			}
			item.CidrBlock = vpc.CidrBlock
		}
		if item.CidrBlock == "" && len(instance.PrivateIPs) > 0 {
			item.CidrBlock = tencent.InferPrivateCIDR(instance.PrivateIPs[0])
		}
		resolved = append(resolved, item)
	}
	return resolved, nil
}
