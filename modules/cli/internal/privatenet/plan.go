package privatenet

import (
	"strings"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

type Options struct {
	HomeRegion       string
	CCNName          string
	ProbeRegions     []string
	DryRun           bool
	SkipSCF          bool
	SkipHosts        bool
	SkipProbe        bool
	ProbeOnly        bool
	RewriteRuntime   bool
	RestoreSCFPublic bool
	UnbindSCFVPC     bool
}

type ResolvedHost struct {
	HostTarget
	Instance  tencent.CloudInstance `json:"instance"`
	CidrBlock string                `json:"cidr_block,omitempty"`
	Area      string                `json:"area,omitempty"`
}

type PlannedVPC struct {
	Region    string `json:"region"`
	Area      string `json:"area"`
	Name      string `json:"name"`
	CidrBlock string `json:"cidr_block"`
	ReuseVpc  string `json:"reuse_vpc_id,omitempty"`
	ReuseSub  string `json:"reuse_subnet_id,omitempty"`
	Reason    string `json:"reason"`
}

type PlannedSCFBind struct {
	SCFTarget
	Area     string `json:"area"`
	VpcID    string `json:"vpc_id,omitempty"`
	SubnetID string `json:"subnet_id,omitempty"`
	VpcName  string `json:"vpc_name,omitempty"`
}

type RecommendedConfig struct {
	StoragePublicIP  string           `json:"storage_public_ip,omitempty"`
	StoragePrivateIP string           `json:"storage_private_ip,omitempty"`
	StorageArea      string           `json:"storage_area,omitempty"`
	SCFGatewayTarget string           `json:"scf_storage_rpc_gateway_target,omitempty"`
	Hosts            []map[string]any `json:"hosts"`
	Notes            []string         `json:"notes"`
}

type Plan struct {
	CCNName         string              `json:"ccn_name"`
	MainlandCCN     string              `json:"mainland_ccn"`
	OverseasCCN     string              `json:"overseas_ccn"`
	NeedCCN         bool                `json:"need_ccn"`
	NeedMainlandCCN bool                `json:"need_mainland_ccn"`
	NeedOverseasCCN bool                `json:"need_overseas_ccn"`
	Hosts           []ResolvedHost      `json:"hosts"`
	SCF             []PlannedSCFBind    `json:"scf"`
	VPCs            []PlannedVPC        `json:"vpcs"`
	CIDRs           []string            `json:"cidrs"`
	CIDRsByArea     map[string][]string `json:"cidrs_by_area"`
	Ports           []string            `json:"ports"`
	Recommended     RecommendedConfig   `json:"recommended_config"`
}

func CCNNames(base string) (mainland, overseas string) {
	base = strings.TrimSpace(base)
	if base == "" {
		base = DefaultCCNName
	}
	if base == DefaultCCNName {
		return DefaultCCNName, DefaultCCNName + "-global"
	}
	return base + "-mainland", base + "-overseas"
}

func BuildPlan(opts Options, hosts []ResolvedHost, scf []SCFTarget, ports []string) Plan {
	if strings.TrimSpace(opts.CCNName) == "" {
		opts.CCNName = DefaultCCNName
	}
	mainlandCCN, overseasCCN := CCNNames(opts.CCNName)
	plan := Plan{
		CCNName: opts.CCNName, MainlandCCN: mainlandCCN, OverseasCCN: overseasCCN,
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
		if hostHasRole(host.HostTarget, "storage") {
			plan.Recommended.StoragePublicIP = host.Address
			plan.Recommended.StoragePrivateIP = privateIP
			plan.Recommended.StorageArea = host.Area
			if host.Address != "" {
				plan.Recommended.SCFGatewayTarget = "ip://" + host.Address + ":11003"
			}
		}
	}

	if opts.RestoreSCFPublic {
		for _, target := range scf {
			plan.SCF = append(plan.SCF, PlannedSCFBind{SCFTarget: target, Area: tencent.NetworkArea(target.Region)})
		}
	}

	if publicIP := strings.TrimSpace(plan.Recommended.StoragePublicIP); publicIP != "" {
		plan.Recommended.Notes = append(plan.Recommended.Notes,
			"主机与 SCF 一律走 Storage 公网 IP "+publicIP+"，不再创建云联网或使用内网 IP。",
		)
	}
	plan.Recommended.Notes = append(plan.Recommended.Notes,
		"不会调用 ModifyInstancesVpcAttribute，因此不会重启现有机器。SSH 和控制台入口继续使用公网 IP。",
	)
	return plan
}
