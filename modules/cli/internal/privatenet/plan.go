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
	UpdateSCFGateway bool
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
	usedCIDRs := make([]string, 0)
	vpcByRegion := map[string]ResolvedHost{}
	networks := map[string]map[string]struct{}{"mainland": {}, "overseas": {}}

	for i := range plan.Hosts {
		host := &plan.Hosts[i]
		host.Area = tencent.NetworkArea(host.Instance.Region)
		if host.Instance.Kind == tencent.KindLighthouse {
			networks[host.Area]["lighthouse:"+host.Instance.Region] = struct{}{}
			if host.CidrBlock == "" && len(host.Instance.PrivateIPs) > 0 {
				host.CidrBlock = tencent.InferPrivateCIDR(host.Instance.PrivateIPs[0])
			}
		}
		if host.Instance.Kind == tencent.KindCVM && host.Instance.VpcID != "" {
			networks[host.Area]["vpc:"+host.Instance.Region+"/"+host.Instance.VpcID] = struct{}{}
			if _, ok := vpcByRegion[host.Instance.Region]; !ok || hostHasRole(host.HostTarget, "storage") {
				vpcByRegion[host.Instance.Region] = *host
			}
		}
		if host.CidrBlock != "" {
			usedCIDRs = appendUnique(usedCIDRs, host.CidrBlock)
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
			if privateIP != "" {
				plan.Recommended.SCFGatewayTarget = "ip://" + privateIP + ":11003"
			}
		}
	}

	if opts.SkipSCF {
		scf = nil
	}
	for _, target := range scf {
		area := tencent.NetworkArea(target.Region)
		bind := PlannedSCFBind{SCFTarget: target, Area: area}
		if reused, ok := vpcByRegion[target.Region]; ok && reused.Instance.VpcID != "" {
			bind.VpcID = reused.Instance.VpcID
			bind.SubnetID = reused.Instance.SubnetID
			bind.VpcName = reused.Instance.VpcID
			plan.SCF = append(plan.SCF, bind)
			networks[area]["vpc:"+target.Region+"/"+reused.Instance.VpcID] = struct{}{}
			continue
		}
		cidr, err := tencent.FirstNonOverlappingCIDR(preferredSCFCIDR(target.Region), usedCIDRs, 80, 20)
		if err != nil {
			cidr = preferredSCFCIDR(target.Region)
		}
		if cidr != "" {
			usedCIDRs = appendUnique(usedCIDRs, cidr)
			plan.CIDRsByArea[area] = appendUnique(plan.CIDRsByArea[area], cidr)
		}
		plan.VPCs = append(plan.VPCs, PlannedVPC{
			Region: target.Region, Area: area, Name: vpcNameForRegion(target.Region), CidrBlock: cidr,
			Reason: "scf region has no existing CVM vpc; create dedicated vpc and attach to same-area ccn",
		})
		networks[area]["vpc:"+target.Region+"/"+vpcNameForRegion(target.Region)] = struct{}{}
		bind.VpcName = vpcNameForRegion(target.Region)
		plan.SCF = append(plan.SCF, bind)
	}

	plan.CIDRs = usedCIDRs
	plan.NeedMainlandCCN = len(networks["mainland"]) > 1
	plan.NeedOverseasCCN = len(networks["overseas"]) > 1
	plan.NeedCCN = plan.NeedMainlandCCN || plan.NeedOverseasCCN
	if plan.NeedCCN {
		plan.Recommended.Notes = append(plan.Recommended.Notes,
			"国内与海外必须使用两张云联网：账号未开通跨境流量白名单时，不能把南京 Storage 和香港/新加坡/东京放进同一张 CCN。",
		)
	}
	if plan.Recommended.StoragePrivateIP != "" && plan.Recommended.StorageArea == "mainland" {
		plan.Recommended.Notes = append(plan.Recommended.Notes,
			"国内 SCF（广州/上海/北京/成都）绑定 VPC 后，可把 storage_gateway_host 改成 Storage 内网 IP "+plan.Recommended.StoragePrivateIP+"。",
			"香港/新加坡/东京 SCF 在开通跨境云联网前继续走 Storage 公网 IP。",
		)
	}
	plan.Recommended.Notes = append(plan.Recommended.Notes,
		"不会调用 ModifyInstancesVpcAttribute，因此不会重启现有机器。SSH 和控制台入口继续使用公网 IP。",
	)
	return plan
}

func ccnNameForArea(plan Plan, area string) string {
	if area == "overseas" {
		return plan.OverseasCCN
	}
	return plan.MainlandCCN
}

func hostAreaCIDRs(plan Plan, area string) []string {
	if cidrs := plan.CIDRsByArea[area]; len(cidrs) > 0 {
		return cidrs
	}
	return nil
}
