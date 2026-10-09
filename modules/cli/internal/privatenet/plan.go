package privatenet

import (
	"fmt"
	"slices"
	"strings"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type Options struct {
	HomeRegion   string
	ProbeRegions []string
	DryRun       bool
	SkipProbe    bool
}

type ResolvedHost struct {
	HostTarget
	Instance  tencent.CloudInstance `json:"instance"`
	CidrBlock string                `json:"cidr_block,omitempty"`
	Area      string                `json:"area,omitempty"`
}

// SCFAccessRoute 是一个地域的 SCF 采集函数访问外部接入的方式（设计文档 3.7）：
//   - vpc：与函数同地域的主机上有外部接入，函数绑定该主机所在的 VPC，访问它的私网地址；
//   - public：同地域没有外部接入，函数不绑定 VPC，访问兜底主机上外部接入的公网地址。
//
// Collector 按同一规则维护函数环境变量中的外部接入地址，所以发布时绑定的 VPC 与之一致。
type SCFAccessRoute struct {
	Region        string `json:"region"`
	Network       string `json:"network"`
	AccessHostID  string `json:"access_host_id"`
	AccessID      string `json:"access_id"`
	AccessAddress string `json:"access_address"`
	VpcID         string `json:"vpc_id,omitempty"`
	SubnetID      string `json:"subnet_id,omitempty"`
	Reason        string `json:"reason"`
}

// SCFRoutePlan 是各地域 SCF 采集函数访问外部接入的计划。
type SCFRoutePlan struct {
	Routes []SCFAccessRoute `json:"routes"`
	// PreferredRegion 是兜底外部接入（access@storage）所在的地域，也就是 Storage 所在地域；同地域优先发布时先占满它。
	PreferredRegion string   `json:"preferred_region,omitempty"`
	Notes           []string `json:"notes"`
}

// Route 返回某个地域的路由。
func (p SCFRoutePlan) Route(region string) (SCFAccessRoute, bool) {
	for _, route := range p.Routes {
		if strings.EqualFold(route.Region, strings.TrimSpace(region)) {
			return route, true
		}
	}
	return SCFAccessRoute{}, false
}

type RecommendedConfig struct {
	Hosts []map[string]any `json:"hosts"`
	Notes []string         `json:"notes"`
}

type Plan struct {
	Hosts       []ResolvedHost      `json:"hosts"`
	CIDRs       []string            `json:"cidrs"`
	CIDRsByArea map[string][]string `json:"cidrs_by_area"`
	Ports       []string            `json:"ports"`
	Recommended RecommendedConfig   `json:"recommended_config"`
}

// BuildPlan 汇总主机的网络拓扑。只读，不修改任何云资源。
func BuildPlan(hosts []ResolvedHost, ports []string) Plan {
	plan := Plan{
		Hosts: hosts, Ports: ports, CIDRsByArea: map[string][]string{"mainland": {}, "overseas": {}},
		Recommended: RecommendedConfig{Hosts: []map[string]any{}},
	}
	for i := range plan.Hosts {
		host := &plan.Hosts[i]
		host.Area = tencent.NetworkArea(host.Instance.Region)
		if host.CidrBlock == "" && len(host.Instance.PrivateIPs) > 0 {
			host.CidrBlock = tencent.InferPrivateCIDR(host.Instance.PrivateIPs[0])
		}
		if host.CidrBlock != "" {
			plan.CIDRs = appendUnique(plan.CIDRs, host.CidrBlock)
			plan.CIDRsByArea[host.Area] = appendUnique(plan.CIDRsByArea[host.Area], host.CidrBlock)
		}
		privateIP := ""
		if len(host.Instance.PrivateIPs) > 0 {
			privateIP = host.Instance.PrivateIPs[0]
		}
		plan.Recommended.Hosts = append(plan.Recommended.Hosts, map[string]any{
			"name": host.Name, "roles": host.Roles, "public_ip": host.Address,
			"private_ip": privateIP, "kind": host.Instance.Kind, "region": host.Instance.Region,
			"area": host.Area, "vpc_id": host.Instance.VpcID,
		})
	}
	plan.Recommended.Notes = append(plan.Recommended.Notes,
		"SCF 采集函数按地域访问外部接入：同地域主机上有外部接入时绑定其 VPC 走私网，否则走 access@storage 的公网地址（moox-cli setup scf-network-plan）。",
		"不会调用 ModifyInstancesVpcAttribute，因此不会重启现有机器。SSH 和控制台入口继续使用公网 IP。",
	)
	return plan
}

// PrivateAccessHosts 返回这些地域会走私网的外部接入主机，发布前需要查询它们的 VPC 和子网。
func PrivateAccessHosts(directory servicecatalog.Directory, regions []string) ([]HostTarget, error) {
	var hosts []HostTarget
	seen := map[string]bool{}
	for _, region := range regions {
		endpoint, err := directory.AccessEndpointForRegion(region, servicecatalog.AccessFallbackHostID)
		if err != nil {
			return nil, err
		}
		if !endpoint.Private || seen[endpoint.HostID] {
			continue
		}
		seen[endpoint.HostID] = true
		host, _ := directory.Host(endpoint.HostID)
		hosts = append(hosts, HostTarget{Name: endpoint.HostID, Address: host.Address, Roles: []string{servicecatalog.AccessComponentID}, Provider: "tencent"})
	}
	return hosts, nil
}

// BuildSCFAccessRoute 按服务目录为一个地域选择外部接入。选中同地域主机的私网地址时，hosts 中必须有该主机的
// 实例信息（VPC、子网、私网地址），否则报错：函数无法访问一个没有绑定 VPC 的私网地址。
func BuildSCFAccessRoute(directory servicecatalog.Directory, hosts map[string]ResolvedHost, region string) (SCFAccessRoute, error) {
	region = strings.ToLower(strings.TrimSpace(region))
	endpoint, err := directory.AccessEndpointForRegion(region, servicecatalog.AccessFallbackHostID)
	if err != nil {
		return SCFAccessRoute{}, err
	}
	route := SCFAccessRoute{
		Region: region, Network: "public", AccessHostID: endpoint.HostID, AccessID: endpoint.ID, AccessAddress: endpoint.Address,
		Reason: "函数所在地域没有外部接入，走 " + endpoint.ID + " 的公网地址",
	}
	if !endpoint.Private {
		return route, nil
	}
	host, ok := hosts[endpoint.HostID]
	if !ok {
		return SCFAccessRoute{}, fmt.Errorf("缺少外部接入主机 %s 的实例信息，地域 %s 的函数无法绑定它的 VPC", endpoint.HostID, region)
	}
	directoryHost, _ := directory.Host(endpoint.HostID)
	if strings.TrimSpace(host.Instance.VpcID) == "" || strings.TrimSpace(host.Instance.SubnetID) == "" {
		return SCFAccessRoute{}, fmt.Errorf("外部接入主机 %s 没有完整的 VPC 和子网信息，地域 %s 的函数无法走私网", endpoint.HostID, region)
	}
	if !slices.Contains(host.Instance.PrivateIPs, directoryHost.PrivateAddress) {
		return SCFAccessRoute{}, fmt.Errorf("外部接入主机 %s 登记的私网地址 %s 与腾讯云实例的私网地址 %v 不一致", endpoint.HostID, directoryHost.PrivateAddress, host.Instance.PrivateIPs)
	}
	route.Network = "vpc"
	route.VpcID = strings.TrimSpace(host.Instance.VpcID)
	route.SubnetID = strings.TrimSpace(host.Instance.SubnetID)
	route.Reason = "与外部接入主机 " + endpoint.HostID + " 同地域，绑定其 VPC 走私网"
	return route, nil
}
