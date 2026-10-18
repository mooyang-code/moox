package tencent

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	scfEndpoint = "https://scf.tencentcloudapi.com"
	scfVersion  = "2018-04-16"
)

// DefaultProbeRegions is the region set used to resolve a public IP to a
// CVM or Lighthouse instance when moox.toml only records the address.
var DefaultProbeRegions = func() []string {
	regions := make([]string, 0, len(scfRegions)+4)
	for _, region := range scfRegions {
		regions = append(regions, region.Code)
	}
	return regions
}()

const (
	KindCVM        = "cvm"
	KindLighthouse = "lighthouse"
)

// CloudInstance is the non-secret network identity of a Tencent host.
type CloudInstance struct {
	Kind             string   `json:"kind"`
	Region           string   `json:"region"`
	Zone             string   `json:"zone,omitempty"`
	InstanceID       string   `json:"instance_id"`
	InstanceName     string   `json:"instance_name,omitempty"`
	State            string   `json:"state,omitempty"`
	VpcID            string   `json:"vpc_id,omitempty"`
	SubnetID         string   `json:"subnet_id,omitempty"`
	PublicIPs        []string `json:"public_ips,omitempty"`
	PrivateIPs       []string `json:"private_ips,omitempty"`
	SecurityGroupIDs []string `json:"security_group_ids,omitempty"`
	// CPU、MemoryGB、DiskGB 是实例规格；接口没返回时为 0。DiskGB 是系统盘与数据盘之和。
	CPU      int `json:"cpu,omitempty"`
	MemoryGB int `json:"memory_gb,omitempty"`
	DiskGB   int `json:"disk_gb,omitempty"`
}

type VpcInfo struct {
	VpcID     string
	Name      string
	CidrBlock string
	Region    string
}

type SubnetInfo struct {
	SubnetID  string
	VpcID     string
	Name      string
	CidrBlock string
	Zone      string
}

type CCNInfo struct {
	CcnID string
	Name  string
	State string
}

type CCNAttachment struct {
	CcnID          string   `json:"ccn_id"`
	InstanceID     string   `json:"instance_id"`
	InstanceRegion string   `json:"instance_region"`
	InstanceType   string   `json:"instance_type"`
	CidrBlock      string   `json:"cidr_block,omitempty"`
	CidrBlocks     []string `json:"cidr_blocks,omitempty"`
	State          string   `json:"state,omitempty"`
}

type SCFFunction struct {
	Region          string
	Namespace       string
	FunctionName    string
	Status          string
	VpcID           string
	SubnetID        string
	PublicNetStatus string
	Environment     map[string]string
}

// NetworkClient talks to CVM, VPC/CCN and SCF APIs with the same TC3 signer
// used by the existing CVM firewall client.
type NetworkClient struct {
	secretID   string
	secretKey  string
	region     string
	endpoint   string
	httpClient *http.Client
	now        func() time.Time
}

func NewNetworkClient(opts ClientOptions) (*NetworkClient, error) {
	cvm, err := NewCVMClient(opts)
	if err != nil {
		return nil, err
	}
	return &NetworkClient{
		secretID: cvm.secretID, secretKey: cvm.secretKey, region: cvm.region,
		endpoint: cvm.endpoint, httpClient: cvm.httpClient, now: cvm.now,
	}, nil
}

func (c *NetworkClient) ForRegion(region string) *NetworkClient {
	out := *c
	out.region = strings.TrimSpace(region)
	return &out
}

func (c *NetworkClient) endpointFor(service string) string {
	if c.endpoint != cvmEndpoint {
		return c.endpoint
	}
	switch service {
	case "vpc":
		return vpcEndpoint
	case "scf":
		return scfEndpoint
	default:
		return cvmEndpoint
	}
}

func (c *NetworkClient) do(ctx context.Context, service, version, action string, payload, out any) error {
	return tc3Do(ctx, c.httpClient, c.secretID, c.secretKey, c.region, service, version, action, c.endpointFor(service), c.now, payload, out)
}

func (c *NetworkClient) LookupCVM(ctx context.Context, publicIP string) (CloudInstance, bool, error) {
	publicIP = strings.TrimSpace(publicIP)
	if net.ParseIP(publicIP) == nil {
		return CloudInstance{}, false, fmt.Errorf("invalid public ip: %s", publicIP)
	}
	var resp struct {
		Response struct {
			Error       *apiError     `json:"Error,omitempty"`
			InstanceSet []cvmInstance `json:"InstanceSet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "cvm", cvmVersion, "DescribeInstances", map[string]any{
		"Filters": []map[string]any{{"Name": "public-ip-address", "Values": []string{publicIP}}},
		"Limit":   1,
	}, &resp); err != nil {
		return CloudInstance{}, false, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return CloudInstance{}, false, err
	}
	if len(resp.Response.InstanceSet) == 0 {
		return CloudInstance{}, false, nil
	}
	return cloudInstanceFromCVM(c.region, resp.Response.InstanceSet[0]), true, nil
}

func (c *NetworkClient) LookupCVMByEIP(ctx context.Context, publicIP string) (CloudInstance, bool, error) {
	publicIP = strings.TrimSpace(publicIP)
	if net.ParseIP(publicIP) == nil {
		return CloudInstance{}, false, fmt.Errorf("invalid public ip: %s", publicIP)
	}
	var eipResp struct {
		Response struct {
			Error      *apiError `json:"Error,omitempty"`
			AddressSet []struct {
				AddressIP  string `json:"AddressIp"`
				InstanceID string `json:"InstanceId"`
			} `json:"AddressSet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "DescribeAddresses", map[string]any{
		"Filters": []map[string]any{{"Name": "address-ip", "Values": []string{publicIP}}},
		"Limit":   1,
	}, &eipResp); err != nil {
		return CloudInstance{}, false, err
	}
	if err := apiCodeMessage(eipResp.Response.Error); err != nil {
		return CloudInstance{}, false, err
	}
	if len(eipResp.Response.AddressSet) == 0 || strings.TrimSpace(eipResp.Response.AddressSet[0].InstanceID) == "" {
		return CloudInstance{}, false, nil
	}
	instanceID := eipResp.Response.AddressSet[0].InstanceID
	var resp struct {
		Response struct {
			Error       *apiError     `json:"Error,omitempty"`
			InstanceSet []cvmInstance `json:"InstanceSet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "cvm", cvmVersion, "DescribeInstances", map[string]any{
		"InstanceIds": []string{instanceID},
	}, &resp); err != nil {
		return CloudInstance{}, false, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return CloudInstance{}, false, err
	}
	if len(resp.Response.InstanceSet) == 0 {
		return CloudInstance{}, false, nil
	}
	instance := cloudInstanceFromCVM(c.region, resp.Response.InstanceSet[0])
	instance.PublicIPs = uniqueNonEmpty(append(instance.PublicIPs, publicIP))
	return instance, true, nil
}

type cvmInstance struct {
	InstanceID         string   `json:"InstanceId"`
	InstanceName       string   `json:"InstanceName"`
	InstanceState      string   `json:"InstanceState"`
	PrivateIPAddresses []string `json:"PrivateIpAddresses"`
	PublicIPAddresses  []string `json:"PublicIpAddresses"`
	SecurityGroupIDs   []string `json:"SecurityGroupIds"`
	Placement          struct {
		Zone string `json:"Zone"`
	} `json:"Placement"`
	VirtualPrivateCloud struct {
		VpcID              string   `json:"VpcId"`
		SubnetID           string   `json:"SubnetId"`
		PrivateIPAddresses []string `json:"PrivateIpAddresses"`
	} `json:"VirtualPrivateCloud"`
	CPU        int        `json:"CPU"`
	Memory     int        `json:"Memory"`
	SystemDisk diskSize   `json:"SystemDisk"`
	DataDisks  []diskSize `json:"DataDisks"`
}

type diskSize struct {
	DiskSize int `json:"DiskSize"`
}

func cloudInstanceFromCVM(region string, item cvmInstance) CloudInstance {
	privateIPs := uniqueNonEmpty(append(item.PrivateIPAddresses, item.VirtualPrivateCloud.PrivateIPAddresses...))
	return CloudInstance{
		Kind: KindCVM, Region: region, Zone: item.Placement.Zone,
		InstanceID: item.InstanceID, InstanceName: item.InstanceName, State: item.InstanceState,
		VpcID: item.VirtualPrivateCloud.VpcID, SubnetID: item.VirtualPrivateCloud.SubnetID,
		PublicIPs: uniqueNonEmpty(item.PublicIPAddresses), PrivateIPs: privateIPs,
		SecurityGroupIDs: uniqueNonEmpty(item.SecurityGroupIDs),
		CPU:              item.CPU, MemoryGB: item.Memory, DiskGB: totalDisk(item.SystemDisk, item.DataDisks),
	}
}

func totalDisk(system diskSize, data []diskSize) int {
	total := system.DiskSize
	for _, disk := range data {
		total += disk.DiskSize
	}
	return total
}

func (c *NetworkClient) FindVpcByName(ctx context.Context, name string) (VpcInfo, bool, error) {
	name = strings.TrimSpace(name)
	var resp struct {
		Response struct {
			Error  *apiError `json:"Error,omitempty"`
			VpcSet []struct {
				VpcID     string `json:"VpcId"`
				VpcName   string `json:"VpcName"`
				CidrBlock string `json:"CidrBlock"`
			} `json:"VpcSet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "DescribeVpcs", map[string]any{
		"Filters": []map[string]any{{"Name": "vpc-name", "Values": []string{name}}},
	}, &resp); err != nil {
		return VpcInfo{}, false, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return VpcInfo{}, false, err
	}
	if len(resp.Response.VpcSet) == 0 {
		return VpcInfo{}, false, nil
	}
	item := resp.Response.VpcSet[0]
	return VpcInfo{VpcID: item.VpcID, Name: item.VpcName, CidrBlock: item.CidrBlock, Region: c.region}, true, nil
}

func (c *NetworkClient) CreateVpc(ctx context.Context, name, cidr string) (VpcInfo, error) {
	var resp struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
			Vpc   struct {
				VpcID     string `json:"VpcId"`
				VpcName   string `json:"VpcName"`
				CidrBlock string `json:"CidrBlock"`
			} `json:"Vpc"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "CreateVpc", map[string]any{
		"VpcName": name, "CidrBlock": cidr,
	}, &resp); err != nil {
		return VpcInfo{}, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return VpcInfo{}, err
	}
	item := resp.Response.Vpc
	return VpcInfo{VpcID: item.VpcID, Name: item.VpcName, CidrBlock: item.CidrBlock, Region: c.region}, nil
}

func (c *NetworkClient) FindSubnetByName(ctx context.Context, vpcID, name string) (SubnetInfo, bool, error) {
	var resp struct {
		Response struct {
			Error     *apiError `json:"Error,omitempty"`
			SubnetSet []struct {
				SubnetID   string `json:"SubnetId"`
				VpcID      string `json:"VpcId"`
				SubnetName string `json:"SubnetName"`
				CidrBlock  string `json:"CidrBlock"`
				Zone       string `json:"Zone"`
			} `json:"SubnetSet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "DescribeSubnets", map[string]any{
		"Filters": []map[string]any{
			{"Name": "vpc-id", "Values": []string{vpcID}},
			{"Name": "subnet-name", "Values": []string{name}},
		},
	}, &resp); err != nil {
		return SubnetInfo{}, false, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return SubnetInfo{}, false, err
	}
	if len(resp.Response.SubnetSet) == 0 {
		return SubnetInfo{}, false, nil
	}
	item := resp.Response.SubnetSet[0]
	return SubnetInfo{SubnetID: item.SubnetID, VpcID: item.VpcID, Name: item.SubnetName, CidrBlock: item.CidrBlock, Zone: item.Zone}, true, nil
}

func (c *NetworkClient) CreateSubnet(ctx context.Context, vpcID, name, cidr, zone string) (SubnetInfo, error) {
	var resp struct {
		Response struct {
			Error  *apiError `json:"Error,omitempty"`
			Subnet struct {
				SubnetID   string `json:"SubnetId"`
				VpcID      string `json:"VpcId"`
				SubnetName string `json:"SubnetName"`
				CidrBlock  string `json:"CidrBlock"`
				Zone       string `json:"Zone"`
			} `json:"Subnet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "CreateSubnet", map[string]any{
		"VpcId": vpcID, "SubnetName": name, "CidrBlock": cidr, "Zone": zone,
	}, &resp); err != nil {
		return SubnetInfo{}, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return SubnetInfo{}, err
	}
	item := resp.Response.Subnet
	return SubnetInfo{SubnetID: item.SubnetID, VpcID: item.VpcID, Name: item.SubnetName, CidrBlock: item.CidrBlock, Zone: item.Zone}, nil
}

func (c *NetworkClient) FindCCNByName(ctx context.Context, name string) (CCNInfo, bool, error) {
	var resp struct {
		Response struct {
			Error  *apiError `json:"Error,omitempty"`
			CcnSet []struct {
				CcnID   string `json:"CcnId"`
				CcnName string `json:"CcnName"`
				State   string `json:"State"`
			} `json:"CcnSet"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "DescribeCcns", map[string]any{
		"Filters": []map[string]any{{"Name": "ccn-name", "Values": []string{name}}},
	}, &resp); err != nil {
		return CCNInfo{}, false, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return CCNInfo{}, false, err
	}
	if len(resp.Response.CcnSet) == 0 {
		return CCNInfo{}, false, nil
	}
	item := resp.Response.CcnSet[0]
	return CCNInfo{CcnID: item.CcnID, Name: item.CcnName, State: item.State}, true, nil
}

func (c *NetworkClient) CreateCCN(ctx context.Context, name string) (CCNInfo, error) {
	var resp struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
			Ccn   struct {
				CcnID   string `json:"CcnId"`
				CcnName string `json:"CcnName"`
				State   string `json:"State"`
			} `json:"Ccn"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "CreateCcn", map[string]any{
		"CcnName": name, "InstanceChargeType": "POSTPAID",
	}, &resp); err != nil {
		return CCNInfo{}, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return CCNInfo{}, err
	}
	item := resp.Response.Ccn
	return CCNInfo{CcnID: item.CcnID, Name: item.CcnName, State: item.State}, nil
}

func (c *NetworkClient) GetSCFFunction(ctx context.Context, namespace, name string) (SCFFunction, error) {
	var resp struct {
		Response struct {
			Error        *apiError `json:"Error,omitempty"`
			FunctionName string    `json:"FunctionName"`
			Status       string    `json:"Status"`
			VpcConfig    *struct {
				VpcID    string `json:"VpcId"`
				SubnetID string `json:"SubnetId"`
			} `json:"VpcConfig"`
			PublicNetConfig *struct {
				PublicNetStatus string `json:"PublicNetStatus"`
			} `json:"PublicNetConfig"`
			Environment *struct {
				Variables []struct {
					Key   string `json:"Key"`
					Value string `json:"Value"`
				} `json:"Variables"`
			} `json:"Environment"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "scf", scfVersion, "GetFunction", map[string]any{
		"FunctionName": name, "Namespace": namespace,
	}, &resp); err != nil {
		return SCFFunction{}, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return SCFFunction{}, err
	}
	out := SCFFunction{Region: c.region, Namespace: namespace, FunctionName: name, Status: resp.Response.Status, Environment: map[string]string{}}
	if resp.Response.VpcConfig != nil {
		out.VpcID = resp.Response.VpcConfig.VpcID
		out.SubnetID = resp.Response.VpcConfig.SubnetID
	}
	if resp.Response.PublicNetConfig != nil {
		out.PublicNetStatus = resp.Response.PublicNetConfig.PublicNetStatus
	}
	if resp.Response.Environment != nil {
		for _, variable := range resp.Response.Environment.Variables {
			if strings.TrimSpace(variable.Key) != "" {
				out.Environment[variable.Key] = variable.Value
			}
		}
	}
	return out, nil
}

func (c *NetworkClient) UpdateSCFNetwork(ctx context.Context, namespace, name, vpcID, subnetID, publicNetStatus string, environment map[string]string) error {
	payload := map[string]any{
		"FunctionName": name,
		"Namespace":    namespace,
		"VpcConfig":    map[string]string{"VpcId": vpcID, "SubnetId": subnetID},
	}
	if status := strings.ToUpper(strings.TrimSpace(publicNetStatus)); status != "" {
		payload["PublicNetConfig"] = map[string]any{
			"PublicNetStatus": status,
			"EipConfig":       map[string]string{"EipStatus": "DISABLE"},
		}
	}
	if environment != nil {
		variables := make([]map[string]string, 0, len(environment))
		for key, value := range environment {
			variables = append(variables, map[string]string{"Key": key, "Value": value})
		}
		payload["Environment"] = map[string]any{"Variables": variables}
	}
	var resp struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "scf", scfVersion, "UpdateFunctionConfiguration", payload, &resp); err != nil {
		return err
	}
	return apiCodeMessage(resp.Response.Error)
}

type SCFInvokeResult struct {
	RequestID string
	Result    string
	Log       string
}

func (c *NetworkClient) InvokeSCF(ctx context.Context, namespace, name string, event any) (SCFInvokeResult, error) {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		namespace = "default"
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return SCFInvokeResult{}, err
	}
	payload := map[string]any{
		"FunctionName":   name,
		"Namespace":      namespace,
		"InvocationType": "RequestResponse",
		"LogType":        "Tail",
		"ClientContext":  string(raw),
	}
	httpClient := c.httpClient
	if httpClient == nil || httpClient.Timeout < 90*time.Second {
		next := &http.Client{Timeout: 90 * time.Second}
		if httpClient != nil {
			next.Transport = httpClient.Transport
		}
		httpClient = next
	}
	var resp struct {
		Response struct {
			Error             *apiError       `json:"Error,omitempty"`
			Result            json.RawMessage `json:"Result"`
			Log               json.RawMessage `json:"Log"`
			FunctionRequestID string          `json:"FunctionRequestId"`
		} `json:"Response"`
	}
	if err := tc3Do(ctx, httpClient, c.secretID, c.secretKey, c.region, "scf", scfVersion, "Invoke", c.endpointFor("scf"), c.now, payload, &resp); err != nil {
		return SCFInvokeResult{}, err
	}
	if err := apiCodeMessage(resp.Response.Error); err != nil {
		return SCFInvokeResult{}, err
	}
	return SCFInvokeResult{
		RequestID: resp.Response.FunctionRequestID,
		Result:    rawJSONString(resp.Response.Result),
		Log:       rawJSONString(resp.Response.Log),
	}, nil
}

func rawJSONString(raw json.RawMessage) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil {
			return text
		}
	}
	return string(raw)
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func CIDRsOverlap(left, right string) bool {
	_, leftNet, leftErr := net.ParseCIDR(strings.TrimSpace(left))
	_, rightNet, rightErr := net.ParseCIDR(strings.TrimSpace(right))
	if leftErr != nil || rightErr != nil {
		return false
	}
	return leftNet.Contains(rightNet.IP) || rightNet.Contains(leftNet.IP)
}

func ProbeRegions(preferred string, extra ...string) []string {
	regions := make([]string, 0, len(DefaultProbeRegions)+len(extra)+1)
	regions = append(regions, preferred)
	regions = append(regions, extra...)
	regions = append(regions, DefaultProbeRegions...)
	return uniqueNonEmpty(regions)
}

func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &http.Client{Timeout: timeout}
}
