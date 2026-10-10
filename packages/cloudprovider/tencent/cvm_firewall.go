package tencent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
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

// EnsureSecurityGroupRule makes the requested TCP/UDP ingress available on a
// CVM's VPC security group. An identical existing rule satisfies the request; a
// broad allow-all rule does not.
func (c *CVMClient) EnsureSecurityGroupRule(ctx context.Context, publicIP string, opts CreateFirewallRulesOptions) error {
	rule, err := NewCreateFirewallRulesRequest(CreateFirewallRulesOptions{
		InstanceID:    "cvm",
		Protocol:      opts.Protocol,
		Ports:         opts.Ports,
		CidrBlock:     opts.CidrBlock,
		IPv6CidrBlock: opts.IPv6CidrBlock,
		Action:        opts.Action,
		Description:   opts.Description,
	})
	if err != nil {
		return err
	}
	wanted := vpcSecurityGroupPolicy{
		Protocol: rule.FirewallRules[0].Protocol, Port: rule.FirewallRules[0].Port,
		CidrBlock: rule.FirewallRules[0].CidrBlock, Action: rule.FirewallRules[0].Action,
		PolicyDescription: rule.FirewallRules[0].FirewallRuleDescription,
	}
	groups, err := c.securityGroups(ctx, publicIP)
	if err != nil {
		return err
	}
	for _, groupID := range groups {
		var policies vpcDescribePoliciesResponse
		if err := c.do(ctx, "vpc", vpcVersion, "DescribeSecurityGroupPolicies", c.endpointFor("vpc"), map[string]any{
			"SecurityGroupId": groupID,
		}, &policies); err != nil {
			return err
		}
		if policies.Response.Error != nil {
			return fmt.Errorf("%s: %s", policies.Response.Error.Code, policies.Response.Error.Message)
		}
		for _, existing := range policies.Response.SecurityGroupPolicySet.Ingress {
			if coversCVMRule(existing, wanted) {
				return nil
			}
		}
	}
	// All groups are evaluated above. Adding to the first group is sufficient
	// for the common single-group topology and avoids silently duplicating a
	// rule across every attached group.
	groupID := groups[0]
	var created struct {
		Response struct {
			Error *apiError `json:"Error,omitempty"`
		} `json:"Response"`
	}
	if err := c.do(ctx, "vpc", vpcVersion, "CreateSecurityGroupPolicies", c.endpointFor("vpc"), vpcCreatePoliciesRequest{
		SecurityGroupID: groupID,
		SecurityGroupPolicySet: struct {
			Ingress []vpcSecurityGroupPolicy `json:"Ingress"`
		}{Ingress: []vpcSecurityGroupPolicy{wanted}},
	}, &created); err != nil {
		return err
	}
	if created.Response.Error != nil {
		return fmt.Errorf("%s: %s", created.Response.Error.Code, created.Response.Error.Message)
	}
	return nil
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

// coversCVMRule 判断已有规则是否正好是要创建的规则。宽泛的"全部协议、全部端口"放行规则不算：腾讯云安全组的默认规则
// 就是它，把它当作满足会让具体规则永远建不出来，也就没办法关掉这条放行一切的规则。
func coversCVMRule(existing, wanted vpcSecurityGroupPolicy) bool {
	if !strings.EqualFold(strings.TrimSpace(existing.Action), strings.TrimSpace(wanted.Action)) {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(existing.Protocol), strings.TrimSpace(wanted.Protocol)) {
		return false
	}
	if strings.TrimSpace(existing.Port) != strings.TrimSpace(wanted.Port) {
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
