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

func (c TencentCloud) FindCCN(ctx context.Context, homeRegion, name string) (tencent.CCNInfo, bool, error) {
	return c.Network.ForRegion(homeRegion).FindCCNByName(ctx, name)
}

func (c TencentCloud) DetachVPC(ctx context.Context, homeRegion, ccnID, vpcRegion, vpcID string) error {
	return c.Network.ForRegion(homeRegion).DetachVPCFromCCN(ctx, ccnID, vpcRegion, vpcID)
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
