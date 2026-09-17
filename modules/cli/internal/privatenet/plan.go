package privatenet

import (
	"net"
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

// SCFStorageRoute is the effective Storage data-plane route for one SCF
// region.  A private route is selected only when Tencent reports that the
// function region is the same as Storage's region and Storage exposes a
// usable VPC/subnet/private address.  All other regions deliberately fall
// back to the public Storage gateway; no CCN is required.
type SCFStorageRoute struct {
	Region           string `json:"region"`
	SameRegion       bool   `json:"same_region"`
	Network          string `json:"network"`
	StorageRegion    string `json:"storage_region,omitempty"`
	StorageZone      string `json:"storage_zone,omitempty"`
	StoragePublicIP  string `json:"storage_public_ip,omitempty"`
	StoragePrivateIP string `json:"storage_private_ip,omitempty"`
	VpcID            string `json:"vpc_id,omitempty"`
	SubnetID         string `json:"subnet_id,omitempty"`
	Target           string `json:"storage_target"`
	Reason           string `json:"reason"`
}

// SCFRoutePlan is a side-effect-free deployment decision produced from the
// Storage host in moox.toml and Tencent's live instance metadata.
type SCFRoutePlan struct {
	Storage                 ResolvedHost      `json:"storage"`
	StorageRegionConfigured bool              `json:"storage_region_configured"`
	Routes                  []SCFStorageRoute `json:"routes"`
	Notes                   []string          `json:"notes"`
}

type RecommendedConfig struct {
	StoragePublicIP  string           `json:"storage_public_ip,omitempty"`
	StoragePrivateIP string           `json:"storage_private_ip,omitempty"`
	StorageRegion    string           `json:"storage_region,omitempty"`
	StorageZone      string           `json:"storage_zone,omitempty"`
	StorageVPCID     string           `json:"storage_vpc_id,omitempty"`
	StorageSubnetID  string           `json:"storage_subnet_id,omitempty"`
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
	SCFRoutes       []SCFStorageRoute   `json:"scf_routes"`
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
			plan.Recommended.StorageRegion = host.Instance.Region
			plan.Recommended.StorageZone = host.Instance.Zone
			plan.Recommended.StorageVPCID = host.Instance.VpcID
			plan.Recommended.StorageSubnetID = host.Instance.SubnetID
			if host.Address != "" {
				plan.Recommended.SCFGatewayTarget = "ip://" + host.Address + ":11003"
			}
		}
	}
	storage := ResolvedHost{}
	for _, host := range plan.Hosts {
		if hostHasRole(host.HostTarget, "storage") {
			storage = host
			break
		}
	}
	for _, target := range scf {
		plan.SCFRoutes = append(plan.SCFRoutes, BuildSCFStorageRoute(storage, target.Region))
	}

	if opts.RestoreSCFPublic {
		for _, target := range scf {
			plan.SCF = append(plan.SCF, PlannedSCFBind{SCFTarget: target, Area: tencent.NetworkArea(target.Region)})
		}
	}

	if publicIP := strings.TrimSpace(plan.Recommended.StoragePublicIP); publicIP != "" {
		plan.Recommended.Notes = append(plan.Recommended.Notes,
			"SCF 与 Storage 同地域且 VPC 信息完整时走私网；跨地域或缺少私网条件时走 Storage 公网 IP "+publicIP+"。",
			"不创建云联网（CCN）；SCF 的 VPC 绑定只用于同地域 Storage 数据面，公网出口由 public_net_status 独立控制。",
		)
	}
	plan.Recommended.Notes = append(plan.Recommended.Notes,
		"不会调用 ModifyInstancesVpcAttribute，因此不会重启现有机器。SSH 和控制台入口继续使用公网 IP。",
	)
	return plan
}

// BuildSCFStorageRoute computes one deterministic route without making cloud
// API calls.  Keeping this function pure makes the deployment plan testable
// and ensures cross-region traffic never accidentally receives a private IP.
func BuildSCFStorageRoute(storage ResolvedHost, region string) SCFStorageRoute {
	region = strings.ToLower(strings.TrimSpace(region))
	storageRegion := strings.ToLower(strings.TrimSpace(storage.Instance.Region))
	publicIP := strings.TrimSpace(storage.Address)
	if publicIP == "" && len(storage.Instance.PublicIPs) > 0 {
		publicIP = strings.TrimSpace(storage.Instance.PublicIPs[0])
	}
	privateIP := ""
	if len(storage.Instance.PrivateIPs) > 0 {
		privateIP = strings.TrimSpace(storage.Instance.PrivateIPs[0])
	}
	route := SCFStorageRoute{
		Region: region, Network: "public", StorageRegion: storageRegion,
		StorageZone: strings.TrimSpace(storage.Instance.Zone), StoragePublicIP: publicIP,
		StoragePrivateIP: privateIP, Target: "ip://" + net.JoinHostPort(publicIP, "11003"),
	}
	if publicIP == "" {
		route.Target = ""
	}
	if region == "" {
		route.Reason = "SCF region is empty; public route is required"
		return route
	}
	if region != storageRegion {
		route.Reason = "SCF and Storage are in different Tencent regions"
		return route
	}
	route.SameRegion = true
	if privateIP == "" || strings.TrimSpace(storage.Instance.VpcID) == "" || strings.TrimSpace(storage.Instance.SubnetID) == "" {
		route.Reason = "same region but Storage has no complete private VPC route"
		return route
	}
	route.Network = "vpc"
	route.VpcID = strings.TrimSpace(storage.Instance.VpcID)
	route.SubnetID = strings.TrimSpace(storage.Instance.SubnetID)
	route.Target = "ip://" + net.JoinHostPort(privateIP, "11003")
	route.Reason = "same Tencent region with Storage VPC/subnet/private IP"
	return route
}
