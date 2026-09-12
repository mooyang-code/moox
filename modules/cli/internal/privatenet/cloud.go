package privatenet

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

type TencentCloud struct {
	Network    *tencent.NetworkClient
	Lighthouse *tencent.Client
}

func (c TencentCloud) LookupHost(ctx context.Context, publicIP string, regions []string) (tencent.CloudInstance, error) {
	var last error
	for _, region := range regions {
		if c.Lighthouse != nil {
			instance, found, err := c.Lighthouse.ForRegion(region).LookupInstance(ctx, publicIP)
			if err == nil && found {
				return instance, nil
			}
			if err != nil && !tencentIgnorable(err) {
				last = err
			}
		}
		if c.Network != nil {
			instance, found, err := c.Network.ForRegion(region).LookupCVM(ctx, publicIP)
			if err == nil && found {
				return instance, nil
			}
			if err != nil && !tencentIgnorable(err) {
				last = err
			}
			instance, found, err = c.Network.ForRegion(region).LookupCVMByEIP(ctx, publicIP)
			if err == nil && found {
				return instance, nil
			}
			if err != nil && !tencentIgnorable(err) {
				last = err
			}
		}
	}
	if last != nil {
		return tencent.CloudInstance{}, fmt.Errorf("lookup %s: %w", publicIP, last)
	}
	return tencent.CloudInstance{}, fmt.Errorf("tencent instance not found for public ip %s", publicIP)
}

func (c TencentCloud) DescribeVpc(ctx context.Context, region, vpcID string) (tencent.VpcInfo, error) {
	return c.Network.ForRegion(region).DescribeVpc(ctx, vpcID)
}

func (c TencentCloud) EnsureCCN(ctx context.Context, homeRegion, name string) (tencent.CCNInfo, bool, error) {
	return c.Network.ForRegion(homeRegion).EnsureCCN(ctx, name)
}

func (c TencentCloud) ListCCNAttachments(ctx context.Context, homeRegion, ccnID string) ([]tencent.CCNAttachment, error) {
	return c.Network.ForRegion(homeRegion).ListCCNAttachments(ctx, ccnID)
}

func (c TencentCloud) AttachVPC(ctx context.Context, homeRegion, ccnID, vpcRegion, vpcID string) error {
	return c.Network.ForRegion(homeRegion).AttachVPCToCCN(ctx, ccnID, vpcRegion, vpcID)
}

func (c TencentCloud) AcceptCCN(ctx context.Context, homeRegion, ccnID, instanceRegion, instanceType, instanceID string) error {
	return c.Network.ForRegion(homeRegion).AcceptCCNAttach(ctx, ccnID, instanceRegion, instanceType, instanceID)
}

func (c TencentCloud) AttachLighthouseCCN(ctx context.Context, region, ccnID string) error {
	return c.Lighthouse.ForRegion(region).AttachCCN(ctx, ccnID)
}

func (c TencentCloud) DescribeLighthouseCCN(ctx context.Context, region string) ([]tencent.CCNAttachment, error) {
	return c.Lighthouse.ForRegion(region).DescribeCCNAttachments(ctx)
}

func (c TencentCloud) EnsureVpc(ctx context.Context, region, name, cidr string) (tencent.VpcInfo, bool, error) {
	return c.Network.ForRegion(region).EnsureVpc(ctx, name, cidr)
}

func (c TencentCloud) EnsureSubnet(ctx context.Context, region, vpcID, name, cidr, zone string) (tencent.SubnetInfo, bool, error) {
	return c.Network.ForRegion(region).EnsureSubnet(ctx, vpcID, name, cidr, zone)
}

func (c TencentCloud) DescribeZones(ctx context.Context, region string) ([]string, error) {
	return c.Network.ForRegion(region).DescribeAvailableZones(ctx)
}

func (c TencentCloud) EnsureSecurityGroup(ctx context.Context, region string, groupIDs []string, rule tencent.CreateFirewallRulesOptions) (bool, error) {
	return c.Network.ForRegion(region).EnsureSecurityGroupIngress(ctx, groupIDs, rule)
}

func (c TencentCloud) EnsureLighthouseFirewall(ctx context.Context, region, publicIP string, rule tencent.CreateFirewallRulesOptions) (bool, error) {
	result, err := c.Lighthouse.ForRegion(region).EnsureFirewallRule(ctx, publicIP, rule)
	if err != nil {
		return false, err
	}
	return result.Created, nil
}

func (c TencentCloud) ListSCF(ctx context.Context, region, namespace string, prefixes []string) ([]tencent.SCFFunction, error) {
	return c.Network.ForRegion(region).ListSCFFunctions(ctx, namespace, prefixes)
}

func (c TencentCloud) GetSCF(ctx context.Context, region, namespace, name string) (tencent.SCFFunction, error) {
	return c.Network.ForRegion(region).GetSCFFunction(ctx, namespace, name)
}

func (c TencentCloud) UpdateSCF(ctx context.Context, region, namespace, name, vpcID, subnetID, publicNet string, env map[string]string) error {
	return c.Network.ForRegion(region).UpdateSCFNetwork(ctx, namespace, name, vpcID, subnetID, publicNet, env)
}

func tencentIgnorable(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if strings.Contains(text, "authfailure") || strings.Contains(text, "secret") {
		return false
	}
	return strings.Contains(text, "region") ||
		strings.Contains(text, "unsupported") ||
		strings.Contains(text, "invalidparameter") ||
		strings.Contains(text, "resourcenotfound") ||
		strings.Contains(text, "not found")
}
