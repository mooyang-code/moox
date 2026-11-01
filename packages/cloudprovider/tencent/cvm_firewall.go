package tencent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

const (
	cvmEndpoint = "https://cvm.tencentcloudapi.com"
	vpcEndpoint = "https://vpc.tencentcloudapi.com"
	cvmVersion  = "2017-03-12"
	vpcVersion  = "2017-03-12"
)

// CVMClient handles the Tencent CVM/VPC APIs needed for hosts that are not
// Lighthouse instances. A Tencent CVM uses VPC security groups rather than
// Lighthouse firewall rules.
type CVMClient struct {
	secretID   string
	secretKey  string
	region     string
	endpoint   string
	httpClient *http.Client
	now        func() time.Time
}

func NewCVMClient(opts ClientOptions) (*CVMClient, error) {
	if strings.TrimSpace(opts.SecretID) == "" {
		return nil, fmt.Errorf("secret id is required")
	}
	if strings.TrimSpace(opts.SecretKey) == "" {
		return nil, fmt.Errorf("secret key is required")
	}
	if strings.TrimSpace(opts.Region) == "" {
		return nil, fmt.Errorf("region is required")
	}
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = cvmEndpoint
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("parse endpoint: %w", err)
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &CVMClient{
		secretID: strings.TrimSpace(opts.SecretID), secretKey: strings.TrimSpace(opts.SecretKey),
		region: strings.TrimSpace(opts.Region), endpoint: endpoint, httpClient: httpClient, now: time.Now,
	}, nil
}

type cvmDescribeInstancesResponse struct {
	Response struct {
		Error       *apiError `json:"Error,omitempty"`
		InstanceSet []struct {
			SecurityGroupIDs []string `json:"SecurityGroupIds"`
		} `json:"InstanceSet"`
	} `json:"Response"`
}

type vpcDescribePoliciesResponse struct {
	Response struct {
		Error                  *apiError `json:"Error,omitempty"`
		SecurityGroupPolicySet struct {
			Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
		} `json:"SecurityGroupPolicySet"`
	} `json:"Response"`
}

type vpcSecurityGroupPolicy struct {
	Protocol          string `json:"Protocol"`
	Port              string `json:"Port"`
	CidrBlock         string `json:"CidrBlock"`
	Action            string `json:"Action"`
	PolicyDescription string `json:"PolicyDescription"`
}

type vpcCreatePoliciesRequest struct {
	SecurityGroupID        string `json:"SecurityGroupId"`
	SecurityGroupPolicySet struct {
		Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
	} `json:"SecurityGroupPolicySet"`
}

// EnsureSecurityGroupRules 确保 CVM 的安全组放行这些入站规则：实例和策略各读一次，缺的规则一次写入。
// 已有更宽的 ACCEPT 规则也算满足。
func (c *CVMClient) EnsureSecurityGroupRules(ctx context.Context, publicIP string, opts []CreateFirewallRulesOptions) (EnsureFirewallRulesResult, error) {
	var result EnsureFirewallRulesResult
	publicIP = strings.TrimSpace(publicIP)
	if net.ParseIP(publicIP) == nil {
		return result, fmt.Errorf("invalid public ip: %s", publicIP)
	}
	wanted := make([]vpcSecurityGroupPolicy, 0, len(opts))
	for _, opt := range opts {
		rule, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
			InstanceID:    "cvm",
			Protocol:      opt.Protocol,
			Ports:         opt.Ports,
			CidrBlock:     opt.CidrBlock,
			IPv6CidrBlock: opt.IPv6CidrBlock,
			Action:        opt.Action,
			Description:   opt.Description,
		})
		if err != nil {
			return result, err
		}
		wanted = append(wanted, vpcSecurityGroupPolicy{
			Protocol: rule.FirewallRules[0].Protocol, Port: rule.FirewallRules[0].Port,
			CidrBlock: rule.FirewallRules[0].CidrBlock, Action: rule.FirewallRules[0].Action,
			PolicyDescription: rule.FirewallRules[0].FirewallRuleDescription,
		})
	}
	var instances cvmDescribeInstancesResponse
	if err := c.do(ctx, "cvm", cvmVersion, "DescribeInstances", c.endpointFor("cvm"), map[string]any{
		"Filters": []map[string]any{{"Name": "public-ip-address", "Values": []string{publicIP}}}, "Limit": 1,
	}, &instances); err != nil {
		return result, err
	}
	if instances.Response.Error != nil {
		return result, fmt.Errorf("%s: %s", instances.Response.Error.Code, instances.Response.Error.Message)
	}
	if len(instances.Response.InstanceSet) == 0 {
		return result, fmt.Errorf("cvm instance not found for public ip %s", publicIP)
	}
	groups := instances.Response.InstanceSet[0].SecurityGroupIDs
	if len(groups) == 0 {
		return result, fmt.Errorf("cvm instance has no security group for public ip %s", publicIP)
	}
	var ingress []vpcSecurityGroupPolicy
	for _, groupID := range groups {
		var policies vpcDescribePoliciesResponse
		if err := c.do(ctx, "vpc", vpcVersion, "DescribeSecurityGroupPolicies", c.endpointFor("vpc"), map[string]any{
			"SecurityGroupId": groupID,
		}, &policies); err != nil {
			return result, err
		}
		if policies.Response.Error != nil {
			return result, fmt.Errorf("%s: %s", policies.Response.Error.Code, policies.Response.Error.Message)
		}
		ingress = append(ingress, policies.Response.SecurityGroupPolicySet.Ingress...)
	}
	missing := make([]vpcSecurityGroupPolicy, 0, len(wanted))
	for _, rule := range wanted {
		if !slices.ContainsFunc(ingress, func(existing vpcSecurityGroupPolicy) bool { return coversCVMRule(existing, rule) }) {
			missing = append(missing, rule)
		}
	}
	result.AlreadyOpen = len(wanted) - len(missing)
	if len(missing) == 0 {
		return result, nil
	}
	// 写到第一个安全组就够了：单安全组是常见情况，也避免把规则重复写到每个组。
	var created struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "CreateSecurityGroupPolicies", c.endpointFor("vpc"), vpcCreatePoliciesRequest{
		SecurityGroupID: groups[0],
		SecurityGroupPolicySet: struct {
			Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
		}{Ingress: missing},
	}, &created); err != nil {
		return result, err
	}
	if created.Response.Error != nil {
		return result, fmt.Errorf("%s: %s", created.Response.Error.Code, created.Response.Error.Message)
	}
	result.Created = len(missing)
	return result, nil
}

// RebootInstance requests a hard reboot for a CVM instance. The operation is
// intentionally exposed separately from firewall management so recovery
// tooling can reboot an unhealthy host after resolving its actual region.
func (c *CVMClient) RebootInstance(ctx context.Context, instanceID string) (string, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return "", fmt.Errorf("instance id is required")
	}
	var response struct {
		Response struct {
			RequestID string    `json:"RequestId"`
			Error     *apiError `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "cvm", cvmVersion, "RebootInstances", c.endpointFor("cvm"), map[string]any{
		"InstanceIds": []string{instanceID},
		"StopType":    "HARD",
	}, &response); err != nil {
		return "", err
	}
	if response.Response.Error != nil {
		return "", fmt.Errorf("%s: %s", response.Response.Error.Code, response.Response.Error.Message)
	}
	return response.Response.RequestID, nil
}

func coversCVMRule(existing, wanted vpcSecurityGroupPolicy) bool {
	if !strings.EqualFold(strings.TrimSpace(existing.Action), strings.TrimSpace(wanted.Action)) {
		return false
	}
	protocol := strings.ToUpper(strings.TrimSpace(existing.Protocol))
	if protocol != strings.ToUpper(strings.TrimSpace(wanted.Protocol)) && protocol != "ALL" {
		return false
	}
	port := strings.TrimSpace(existing.Port)
	if port != strings.TrimSpace(wanted.Port) && !strings.EqualFold(port, "ALL") {
		return false
	}
	return strings.TrimSpace(existing.CidrBlock) == strings.TrimSpace(wanted.CidrBlock)
}

func (c *CVMClient) endpointFor(service string) string {
	if c.endpoint != cvmEndpoint {
		return c.endpoint
	}
	if service == "vpc" {
		return vpcEndpoint
	}
	return cvmEndpoint
}

func (c *CVMClient) ForRegion(region string) *CVMClient {
	out := *c
	out.region = strings.TrimSpace(region)
	return &out
}

func (c *CVMClient) do(ctx context.Context, service, version, action, endpoint string, payload, out any) error {
	return tc3Do(ctx, c.httpClient, c.secretID, c.secretKey, c.region, service, version, action, endpoint, c.now, payload, out)
}

func sha256HexCVM(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hmacCVM(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}
